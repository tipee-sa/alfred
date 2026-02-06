package internal

import (
	"context"
	"io"
	"path"
)

type WorkspaceFS interface {
	HostPath(p string) string
	MkDir(p string) error
	SaveContainerLogs(containerId, p string) error
	StreamContainerLogs(ctx context.Context, containerId, p string) error
	Archive(p string) (io.ReadCloser, error)
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

func (f *ScopedFS) Delete(p string) error {
	return f.Parent.Delete(path.Join(f.Prefix, p))
}

func (f *ScopedFS) Scope(p string) WorkspaceFS {
	return &ScopedFS{Parent: f, Prefix: path.Join(f.Prefix, p)}
}
