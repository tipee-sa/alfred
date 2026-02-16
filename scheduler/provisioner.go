package scheduler

import "context"

type Provisioner interface {
	Provision(ctx context.Context, nodeName string) (Node, error)
	// Shutdown shuts down the provisioner, terminating all nodes.
	Shutdown()
	// Wait blocks until the provisioner has fully shut down.
	// It must not return before Shutdown has been called.
	Wait()
}
