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

// ImagePreparer is a Provisioner that can get a job's images ready for its nodes ahead of
// time, while the nodes are still being provisioned. The scheduler calls it when a job arrives.
type ImagePreparer interface {
	// PrepareImages starts the work in the background and returns at once.
	PrepareImages(images []string)
}
