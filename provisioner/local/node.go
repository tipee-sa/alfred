package local

import (
	"context"
	"fmt"

	"github.com/docker/docker/client"
	"github.com/gammadia/alfred/provisioner/internal"
	"github.com/gammadia/alfred/scheduler"
)

type Node struct {
	ctx    context.Context
	cancel context.CancelFunc
	docker *client.Client

	nodeNumber int
}

// Node implements scheduler.Node
var _ scheduler.Node = (*Node)(nil)

func (n *Node) Name() string {
	return fmt.Sprintf("local-%d", n.nodeNumber)
}

func (n *Node) RunTask(task *scheduler.Task) error {
	return internal.RunContainer(n.ctx, n.docker, task)
}

func (*Node) Terminate() error {
	return nil
}
