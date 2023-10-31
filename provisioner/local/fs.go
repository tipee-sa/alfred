package local

import (
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

func (f *fs) Archive(p string) (rc io.ReadCloser, err error) {
	cmd := exec.Command("tar", "-c", "-f", "-", "-z", "-C", f.root, strings.TrimLeft(p, "/"))
	if rc, err = cmd.StdoutPipe(); err != nil {
		return
	}
	err = cmd.Start()
	return
}

func (f *fs) Delete(p string) error {
	return os.RemoveAll(f.HostPath(p))
}

func (f *fs) Scope(p string) internal.WorkspaceFS {
	return &internal.ScopedFS{Parent: f, Prefix: p}
}
