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

func (f *fs) Archive(p string) (rc io.ReadCloser, err error) {
	cmd := exec.Command("tar", "--create", "--use-compress-program", "zstd --compress --adapt=min=5,max=8", "--file", "-", "--directory", f.root, strings.TrimLeft(p, "/"))
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
