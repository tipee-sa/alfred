package scheduler

import "github.com/gammadia/alfred/namegen"

type Provisioner interface {
	Provision(nodeName namegen.ID) (Node, error) // TODO: we need cancellation
	// Shutdown shuts down the provisioner, terminating all nodes.
	Shutdown()
	// Wait blocks until the provisioner has fully shut down.
	// It must not return before Shutdown has been called.
	Wait()
}
