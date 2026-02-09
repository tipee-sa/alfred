package local

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strings"

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
	entries, err := os.ReadDir(f.HostPath(dir))
	if err != nil {
		return nil, fmt.Errorf("failed to read directory '%s': %w", dir, err)
	}

	var logFiles []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".log") {
			logFiles = append(logFiles, path.Join(f.HostPath(dir), entry.Name()))
		}
	}
	if len(logFiles) == 0 {
		return nil, fmt.Errorf("no log files found in '%s'", dir)
	}

	args := append([]string{"-n", fmt.Sprintf("%d", lines)}, logFiles...)
	cmd := exec.Command("tail", args...)
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
		return fmt.Errorf("archive command failed: %w: %s", err, r.stderr.String())
	}
	return nil
}

func (f *fs) Delete(p string) error {
	return os.RemoveAll(f.HostPath(p))
}

func (f *fs) Scope(p string) internal.WorkspaceFS {
	return &internal.ScopedFS{Parent: f, Prefix: p}
}
