package local

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/docker/docker/client"
	"github.com/gammadia/alfred/provisioner/internal"
	"github.com/gammadia/alfred/scheduler"
)

type Node struct {
	provisioner *Provisioner

	ctx    context.Context
	cancel context.CancelFunc
	docker *client.Client

	nodeNumber int
	log        *slog.Logger
}

// Node implements scheduler.Node
var _ scheduler.Node = (*Node)(nil)

func (n *Node) Name() string {
	return fmt.Sprintf("local-%d", n.nodeNumber)
}

func (n *Node) RunTask(task *scheduler.Task) (int, error) {
	return internal.RunContainer(n.ctx, n.log, n.docker, task, n.provisioner.fs)
}

func (*Node) Terminate() error {
	return nil
}
