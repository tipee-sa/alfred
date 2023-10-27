package scheduler

type NodeStatus string

const (
	NodeStatusProvisioning NodeStatus = "provisioning"
	NodeStatusOnline       NodeStatus = "online"
	NodeStatusTerminating  NodeStatus = "terminating"
	NodeStatusFailed       NodeStatus = "failed"
)

type Node interface {
	Name() string
	Run(task *Task) error // TODO: we need cancellation
	Terminate() error
}

type nodeState struct {
	node   Node
	status NodeStatus
	tasks  []*Task
}
