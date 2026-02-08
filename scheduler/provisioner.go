package scheduler

type Provisioner interface {
	Provision(nodeName string, flavor string) (Node, error) // TODO: we need cancellation
	// Shutdown shuts down the provisioner, terminating all nodes.
	Shutdown()
	// Wait blocks until the provisioner has fully shut down.
	// It must not return before Shutdown has been called.
	Wait()
}
