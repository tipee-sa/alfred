package scheduler

import "github.com/gammadia/alfred/namegen"

type Provisioner interface {
	// MaxNodes returns the maximum number of nodes that can be provisioned
	// This is a hard limit. Nodes in the Terminating state are still counted.
	MaxNodes() int
	MaxTasksPerNode() int

	Provision(nodeName namegen.ID) (Node, error) // TODO: we need cancellation

	// Shutdown shuts down the provisioner, terminating all nodes.
	Shutdown()

	// Wait blocks until the provisioner has fully shut down.
	// It must not return before Shutdown has been called.
	Wait()
}
