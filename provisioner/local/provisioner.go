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
	docker *client.Client
	fs     *fs
}

// Provisioner implements scheduler.Provisioner
var _ scheduler.Provisioner = (*Provisioner)(nil)

func New(config Config) (*Provisioner, error) {
	docker, err := client.NewClientWithOpts(client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to initialize docker client: %w", err)
	}

	return &Provisioner{
		config: config,
		docker: docker,
		fs:     newFs(config.Workspace),
	}, nil
}

func (p *Provisioner) Provision(_ context.Context, nodeName string) (scheduler.Node, error) {
	node := &Node{
		name:        nodeName,
		provisioner: p,

		docker: p.docker,
	}
	node.log = p.config.Logger.With(slog.Group("node", "name", nodeName))

	return node, nil
}

func (p *Provisioner) Shutdown() {}
func (p *Provisioner) Wait()     {}
