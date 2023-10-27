package scheduler

import "log"

type Provisioner interface {
	SetLogger(logger *log.Logger)

	// MaxNodes returns the maximum number of nodes that can be provisioned
	// This is a hard limit. Nodes in the Terminating state are still counted.
	MaxNodes() int
	MaxTasksPerNode() int

	Provision() (Node, error) // TODO: we need cancellation

	Shutdown()
	Wait()
}
