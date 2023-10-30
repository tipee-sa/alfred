package local

import (
	"context"
	"fmt"

	"github.com/docker/docker/client"
	"github.com/gammadia/alfred/provisioner/internal"
	"github.com/gammadia/alfred/scheduler"
)

type LocalNode struct {
	ctx    context.Context
	cancel context.CancelFunc
	docker *client.Client

	nodeNumber int
}

// LocalNode implements scheduler.Node
var _ scheduler.Node = (*LocalNode)(nil)

func (ln *LocalNode) Name() string {
	return fmt.Sprintf("local-%d", ln.nodeNumber)
}

func (ln *LocalNode) Run(task *scheduler.Task) error {
	return internal.RunContainer(ln.ctx, ln.docker, task)
}

func (*LocalNode) Terminate() error {
	return nil
}
