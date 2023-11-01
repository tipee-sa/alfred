package openstack

import (
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

func (f *fs) Archive(p string) (rc io.ReadCloser, err error) {
	return nil, fmt.Errorf("not implemented")
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
