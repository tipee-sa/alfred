package local

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"

	"github.com/gammadia/alfred/provisioner/internal"
)

type fs struct {
	root string
}

// fs implements internal.WorkspaceFS
var _ internal.WorkspaceFS = (*fs)(nil)

func newFs(root string) *fs {
	return &fs{strings.TrimRight(root, "/")}
}

func (f *fs) HostPath(p string) string {
	return path.Join(f.root, p)
}

func (f *fs) MkDir(p string) error {
	return os.MkdirAll(f.HostPath(p), 0777)
}

func (f *fs) SaveContainerLogs(containerId, p string) error {
	cmd := exec.Command("docker", "logs", "--timestamps", containerId)
	if out, err := os.Create(f.HostPath(p)); err != nil {
		return err
	} else {
		cmd.Stdout = out
		cmd.Stderr = out
	}
	return cmd.Run()
}

func (f *fs) StreamContainerLogs(ctx context.Context, containerId, p string) error {
	out, err := os.Create(f.HostPath(p))
	if err != nil {
		return err
	}
	defer out.Close()

	cmd := exec.CommandContext(ctx, "docker", "logs", "--follow", "--timestamps", containerId)
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}

func (f *fs) TailLogs(dir string, lines int) (io.ReadCloser, error) {
	hostDir := f.HostPath(dir)
	entries, err := os.ReadDir(hostDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory '%s': %w", dir, err)
	}

	var logFiles []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".log") {
			logFiles = append(logFiles, entry.Name())
		}
	}
	if len(logFiles) == 0 {
		return nil, fmt.Errorf("no log files found in '%s'", dir)
	}

	// Streamed rather than buffered: with lines <= 0 the output is the whole logs.
	args := append([]string{"-v", "-n", internal.TailLinesArg(lines)}, logFiles...)
	cmd := exec.Command("tail", args...)
	cmd.Dir = hostDir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to tail log files: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to tail log files: %w", err)
	}

	// Wait for the first byte, so that a failure before any output (the log
	// files gone since they were listed: the workspace torn down) is returned
	// here, as it was when the output was buffered. Follow mode then falls back
	// to the finalized artifact. (OpenStack does not wait: over SSH the wait
	// could stall out of reach of the RPC's cancellation.)
	r := &cmdReader{stdout: stdout, cmd: cmd, stderr: &stderr}
	br := bufio.NewReader(r)
	if _, err := br.Peek(1); err != nil && err != io.EOF {
		r.Close()
		return nil, err
	}
	return struct {
		io.Reader
		io.Closer
	}{br, r}, nil
}

// cmdReader reads a command's stdout. At EOF it waits for the command and
// returns its failure, if any, in place of EOF.
type cmdReader struct {
	stdout   io.ReadCloser
	cmd      *exec.Cmd
	stderr   *bytes.Buffer
	waitOnce sync.Once
	err      error
}

func (r *cmdReader) Read(p []byte) (int, error) {
	n, err := r.stdout.Read(p)
	if err == io.EOF {
		if waitErr := r.wait(); waitErr != nil {
			return n, waitErr
		}
	}
	return n, err
}

// wait waits for the command once, however many times it is called, from
// Read at EOF and from Close, which may run at the same time.
func (r *cmdReader) wait() error {
	r.waitOnce.Do(func() {
		if err := r.cmd.Wait(); err != nil {
			r.err = fmt.Errorf("failed to tail log files: %w (stderr: %s)", err, strings.TrimSpace(r.stderr.String()))
		}
	})
	return r.err
}

// Close stops reading early if need be: closing the pipe makes a command
// still writing to it fail (SIGPIPE), so the wait does not block, and a Read
// blocked on the pipe returns. It may be called during a Read.
func (r *cmdReader) Close() error {
	r.stdout.Close()
	_ = r.wait()
	return nil
}

func (f *fs) Archive(p string) (io.ReadCloser, error) {
	cmd := exec.Command("tar", "--create", "--use-compress-program", "zstd --compress --adapt=min=5,max=8", "--file", "-", "--directory", f.root, strings.TrimLeft(p, "/"))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	return &archiveReader{Reader: stdout, cmd: cmd, stderr: &stderr}, nil
}

// archiveReader wraps a stdout pipe so that Close() waits for the subprocess
// and surfaces any tar/zstd errors (which would otherwise be lost as a silent EOF).
type archiveReader struct {
	io.Reader
	cmd    *exec.Cmd
	stderr *bytes.Buffer
}

func (r *archiveReader) Close() error {
	if err := r.cmd.Wait(); err != nil {
		return fmt.Errorf("failed to archive: %w (stderr: %s)", err, r.stderr.String())
	}
	return nil
}

func (f *fs) Delete(p string) error {
	return os.RemoveAll(f.HostPath(p))
}

func (f *fs) Scope(p string) internal.WorkspaceFS {
	return &internal.ScopedFS{Parent: f, Prefix: p}
}
