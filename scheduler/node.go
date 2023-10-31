package scheduler

import (
	"github.com/gammadia/alfred/namegen"
	"log/slog"
	"time"
)

type NodeStatus string

const (
	NodeStatusPending      NodeStatus = "pending"
	NodeStatusProvisioning NodeStatus = "provisioning"
	NodeStatusOnline       NodeStatus = "online"
	NodeStatusTerminating  NodeStatus = "terminating"
	NodeStatusFailed       NodeStatus = "failed"
)

type Node interface {
	Name() string
	RunTask(task *Task) error // TODO: we need cancellation
	Terminate() error
}

type nodeState struct {
	node   Node
	status NodeStatus
	tasks  []*Task
	log    *slog.Logger

	nodeName      namegen.ID
	earliestStart time.Time
}
