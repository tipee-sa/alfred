package openstack

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/alessio/shellescape"
	"github.com/gammadia/alfred/provisioner/internal"
	"github.com/samber/lo"
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

// Archive returns a .tar.zst of the given path
func (f *fs) Archive(p string) (io.ReadCloser, error) {
	session, err := f.ssh.NewSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create SSH session: %w", err)
	}

	out := lo.Must(session.StdoutPipe())
	if err = session.Start(fmt.Sprintf(
		"tar --create --use-compress-program=%s --file - --directory %s %s",
		shellescape.Quote("zstd --compress --adapt=min=5,max=8"),
		shellescape.Quote(f.root),
		shellescape.Quote(strings.TrimLeft(p, "/")),
	)); err != nil {
		return nil, err
	}

	return splitReadCloser{out, session}, nil
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

type splitReadCloser struct {
	io.Reader
	io.Closer
}
