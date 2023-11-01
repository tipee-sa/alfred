package local

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/docker/docker/client"
	"github.com/gammadia/alfred/scheduler"
)

type Provisioner struct {
	config Config
	ctx    context.Context
	cancel context.CancelFunc
	docker *client.Client
	fs     *fs

	nextNodeNumber int
}

// Provisioner implements scheduler.Provisioner
var _ scheduler.Provisioner = (*Provisioner)(nil)

func New(config Config) (*Provisioner, error) {
	docker, err := client.NewClientWithOpts()
	if err != nil {
		return nil, fmt.Errorf("failed to init docker client: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Provisioner{
		config: config,
		ctx:    ctx,
		cancel: cancel,
		docker: docker,
		fs:     newFs(config.Workspace),

		nextNodeNumber: 0,
	}, nil
}

func (p *Provisioner) Provision(nodeName string) (scheduler.Node, error) {
	p.nextNodeNumber += 1

	ctx, cancel := context.WithCancel(p.ctx)

	node := &Node{
		name:        nodeName,
		provisioner: p,

		ctx:    ctx,
		cancel: cancel,
		docker: p.docker,
	}
	node.log = p.config.Logger.With(slog.Group("node", "name", nodeName))

	return node, nil
}

func (p *Provisioner) Shutdown() {
	// TODO
}

func (p *Provisioner) Wait() {
	// TODO
}
