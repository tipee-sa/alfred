package openstack

import (
	"fmt"
	"io"
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

func (f *fs) MkDir(path string) error {
	return fmt.Errorf("not implemented")
}

func (f *fs) Archive(path string) (rc io.ReadCloser, err error) {
	return nil, fmt.Errorf("not implemented")
}

func (f *fs) Delete(path string) error {
	return fmt.Errorf("not implemented")
}

func (f *fs) Scope(path string) internal.WorkspaceFS {
	return &internal.ScopedFS{Parent: f, Prefix: path}
}
