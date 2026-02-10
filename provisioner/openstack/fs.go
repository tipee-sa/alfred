package openstack

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/alessio/shellescape"
	"github.com/gammadia/alfred/provisioner/internal"
	"golang.org/x/crypto/ssh"
)

type fs struct {
	root string
	ssh  *ssh.Client
}

// fs implements internal.WorkspaceFS
var _ internal.WorkspaceFS = (*fs)(nil)

func newFs(root string, ssh *ssh.Client) *fs {
	return &fs{
		root: strings.TrimRight(root, "/"),
		ssh:  ssh,
	}
}

func (f *fs) through(thunk func(ssh.Session) error) error {
	session, err := f.ssh.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create SSH session: %w", err)
	}
	defer session.Close()

	return thunk(*session)
}

func (f *fs) HostPath(p string) string {
	return path.Join(f.root, p)
}

func (f *fs) MkDir(p string) error {
	return f.through(func(session ssh.Session) error {
		if err := session.Run("mkdir -p " + shellescape.Quote(f.HostPath(p))); err != nil {
			return fmt.Errorf("failed to create directory '%s': %w", p, err)
		}

		return nil
	})
}

func (f *fs) SaveContainerLogs(containerId, p string) error {
	session, err := f.ssh.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create SSH session: %w", err)
	}
	defer session.Close()

	return session.Run(fmt.Sprintf(
		"docker logs --timestamps %s > %s 2>&1",
		shellescape.Quote(containerId),
		shellescape.Quote(f.HostPath(p)),
	))
}

func (f *fs) StreamContainerLogs(ctx context.Context, containerId, p string) error {
	session, err := f.ssh.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create SSH session: %w", err)
	}
	defer session.Close()

	go func() {
		<-ctx.Done()
		session.Close()
	}()

	return session.Run(fmt.Sprintf(
		"docker logs --follow --timestamps %s > %s 2>&1",
		shellescape.Quote(containerId),
		shellescape.Quote(f.HostPath(p)),
	))
}

func (f *fs) TailLogs(dir string, lines int) (io.ReadCloser, error) {
	session, err := f.ssh.NewSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create SSH session: %w", err)
	}
	defer session.Close()

	var stderr bytes.Buffer
	session.Stderr = &stderr

	// Collect all output at once (tail output is bounded by -n lines).
	// Using session.Output avoids SSH stdout pipe EOF delivery issues that cause
	// the streaming read loop to hang.
	output, err := session.Output(fmt.Sprintf(
		`cd %s && files=$(ls -1 *.log 2>/dev/null | sort) && test -n "$files" && echo "$files" | xargs tail -v -n %d`,
		shellescape.Quote(f.HostPath(dir)),
		lines,
	))
	if err != nil {
		return nil, fmt.Errorf("failed to read logs: %w: %s", err, stderr.String())
	}

	return io.NopCloser(bytes.NewReader(output)), nil
}

// Archive returns a .tar.zst of the given path.
// Uses io.Pipe + session.Stdout instead of session.StdoutPipe() because
// StdoutPipe() has the caller read directly from the SSH channel, which
// hangs on long-lived connections. Setting session.Stdout lets the SSH
// library manage channel reading via an internal goroutine.
func (f *fs) Archive(p string) (io.ReadCloser, error) {
	session, err := f.ssh.NewSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create SSH session: %w", err)
	}

	var stderr bytes.Buffer
	session.Stderr = &stderr

	pr, pw := io.Pipe()
	session.Stdout = pw

	if err = session.Start(fmt.Sprintf(
		"tar --create --use-compress-program=%s --file - --directory %s %s",
		shellescape.Quote("zstd --compress --adapt=min=5,max=8"),
		shellescape.Quote(f.root),
		shellescape.Quote(strings.TrimLeft(p, "/")),
	)); err != nil {
		session.Close()
		return nil, err
	}

	// Background goroutine waits for the command to complete (which also waits
	// for the library's internal stdout copy goroutine to finish), then closes
	// the pipe writer so the reader gets EOF.
	go func() {
		if err := session.Wait(); err != nil {
			pw.CloseWithError(fmt.Errorf("remote archive command failed: %w: %s", err, stderr.String()))
		} else {
			pw.Close()
		}
		session.Close()
	}()

	return pr, nil
}

func (f *fs) Delete(p string) error {
	return f.through(func(session ssh.Session) error {
		if err := session.Run("rm -rf " + shellescape.Quote(f.HostPath(p))); err != nil {
			return fmt.Errorf("failed to remove file '%s': %w", p, err)
		}

		return nil
	})
}

func (f *fs) Scope(p string) internal.WorkspaceFS {
	return &internal.ScopedFS{Parent: f, Prefix: p}
}

// sshReadCloser wraps an SSH session's stdout pipe so that Close() waits for
// the remote command and surfaces errors from stderr.
type sshReadCloser struct {
	io.Reader
	session *ssh.Session
	stderr  *bytes.Buffer
	label   string
}

func (r *sshReadCloser) Close() error {
	err := r.session.Wait()
	r.session.Close()
	if err != nil {
		return fmt.Errorf("remote %s command failed: %w: %s", r.label, err, r.stderr.String())
	}
	return nil
}
