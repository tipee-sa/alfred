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

		nextNodeNumber: 0,
	}, nil
}

func (p *Provisioner) MaxNodes() int {
	return p.config.MaxNodes
}

func (p *Provisioner) MaxTasksPerNode() int {
	return p.config.MaxTasksPerNode
}

func (p *Provisioner) Provision() (scheduler.Node, error) {
	p.nextNodeNumber += 1

	ctx, cancel := context.WithCancel(p.ctx)

	return &Node{
		ctx:    ctx,
		cancel: cancel,
		docker: p.docker,

		nodeNumber: p.nextNodeNumber,
		log:        p.config.Logger.With(slog.Group("node", "name", fmt.Sprintf("local-%d", p.nextNodeNumber))),
	}, nil
}

func (p *Provisioner) Shutdown() {
	// TODO
}

func (p *Provisioner) Wait() {
	// TODO
}
