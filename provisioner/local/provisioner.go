package local

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"runtime"

	"github.com/docker/docker/client"
	"github.com/gammadia/alfred/scheduler"
)

type LocalProvisioner struct {
	log    *slog.Logger
	ctx    context.Context
	cancel context.CancelFunc
	docker *client.Client

	nextNodeNumber int
}

// LocalProvisioner implements Provisioner
var _ scheduler.Provisioner = (*LocalProvisioner)(nil)

func NewProvisioner(logger *slog.Logger) (*LocalProvisioner, error) {
	docker, err := client.NewClientWithOpts()
	if err != nil {
		return nil, fmt.Errorf("failed to init docker client: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &LocalProvisioner{
		log:    logger,
		ctx:    ctx,
		cancel: cancel,
		docker: docker,

		nextNodeNumber: 0,
	}, nil
}

func (lp *LocalProvisioner) SetLogger(logger *log.Logger) {
	// No-op
}

func (lp *LocalProvisioner) MaxNodes() int {
	return (runtime.NumCPU() + 1) / 2
}

func (lp *LocalProvisioner) MaxTasksPerNode() int {
	return 2
}

func (lp *LocalProvisioner) Provision() (scheduler.Node, error) {
	lp.nextNodeNumber += 1

	ctx, cancel := context.WithCancel(lp.ctx)

	return &LocalNode{
		ctx:    ctx,
		cancel: cancel,
		docker: lp.docker,

		nodeNumber: lp.nextNodeNumber,
	}, nil
}

func (lp *LocalProvisioner) Shutdown() {
	// TODO
}

func (lp *LocalProvisioner) Wait() {
	// TODO
}
