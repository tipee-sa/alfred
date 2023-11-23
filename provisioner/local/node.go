package local

import (
	"context"
	"log/slog"

	"github.com/docker/docker/client"
	"github.com/gammadia/alfred/provisioner/internal"
	"github.com/gammadia/alfred/scheduler"
)

type Node struct {
	name string

	provisioner *Provisioner

	ctx    context.Context
	cancel context.CancelFunc
	docker *client.Client

	log *slog.Logger
}

// Node implements scheduler.Node
var _ scheduler.Node = (*Node)(nil)

func (n *Node) Name() string {
	return n.name
}

func (n *Node) RunTask(task *scheduler.Task, runConfig scheduler.RunTaskConfig) (int, error) {
	return internal.RunContainer(n.ctx, n.docker, task, n.provisioner.fs, runConfig)
}

func (*Node) Terminate() error {
	return nil
}
