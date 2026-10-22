package internal

import (
	"context"
	"io"
	"path"
	"strconv"
)

type WorkspaceFS interface {
	HostPath(p string) string
	MkDir(p string) error
	SaveContainerLogs(containerId, p string) error
	StreamContainerLogs(ctx context.Context, containerId, p string) error
	Archive(p string) (io.ReadCloser, error)
	// TailLogs returns the *.log files of dir, in name order, formatted like
	// `tail -v`: of each, its last lines lines, or with lines <= 0 its last
	// MaxLogBytes bytes, which is the whole file unless it is larger (see
	// TailArgs).
	TailLogs(dir string, lines int) (io.ReadCloser, error)
	Delete(p string) error
	Scope(p string) WorkspaceFS
}

type ScopedFS struct {
	Parent WorkspaceFS
	Prefix string
}

// ScopedFS implements WorkspaceFS
var _ WorkspaceFS = (*ScopedFS)(nil)

func (f *ScopedFS) HostPath(p string) string {
	return f.Parent.HostPath(path.Join(f.Prefix, p))
}

func (f *ScopedFS) MkDir(p string) error {
	return f.Parent.MkDir(path.Join(f.Prefix, p))
}

func (f *ScopedFS) SaveContainerLogs(containerId, p string) error {
	return f.Parent.SaveContainerLogs(containerId, path.Join(f.Prefix, p))
}

func (f *ScopedFS) StreamContainerLogs(ctx context.Context, containerId, p string) error {
	return f.Parent.StreamContainerLogs(ctx, containerId, path.Join(f.Prefix, p))
}

func (f *ScopedFS) Archive(p string) (io.ReadCloser, error) {
	return f.Parent.Archive(path.Join(f.Prefix, p))
}

func (f *ScopedFS) TailLogs(dir string, lines int) (io.ReadCloser, error) {
	return f.Parent.TailLogs(path.Join(f.Prefix, dir), lines)
}

func (f *ScopedFS) Delete(p string) error {
	return f.Parent.Delete(path.Join(f.Prefix, p))
}

func (f *ScopedFS) Scope(p string) WorkspaceFS {
	return &ScopedFS{Parent: f, Prefix: path.Join(f.Prefix, p)}
}

// MaxLogBytes bounds each step log read whole (TailLogs with lines <= 0): its
// last MaxLogBytes bytes. Logs are read into memory, on the node and on the
// server, so they are bounded; the server applies the same bound to logs read
// from an artifact.
const MaxLogBytes = 10 << 20

// TailArgs are tail's arguments selecting what TailLogs returns of each file:
// its last lines lines, or with lines <= 0 its last MaxLogBytes bytes.
func TailArgs(lines int) []string {
	if lines <= 0 {
		return []string{"-c", strconv.Itoa(MaxLogBytes)}
	}
	return []string{"-n", strconv.Itoa(lines)}
}
