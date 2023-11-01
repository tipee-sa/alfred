package scheduler

import (
	"log/slog"
	"time"

	"github.com/gammadia/alfred/proto"
)

type NodeStatus proto.NodeStatus_Status

const (
	NodeStatusPending      NodeStatus = NodeStatus(proto.NodeStatus_PENDING)
	NodeStatusProvisioning NodeStatus = NodeStatus(proto.NodeStatus_PROVISIONING)
	NodeStatusFailed       NodeStatus = NodeStatus(proto.NodeStatus_FAILED)
	NodeStatusOnline       NodeStatus = NodeStatus(proto.NodeStatus_ONLINE)
	NodeStatusTerminating  NodeStatus = NodeStatus(proto.NodeStatus_TERMINATING)
)

func (ns NodeStatus) AsProto() proto.NodeStatus_Status {
	return proto.NodeStatus_Status(ns)
}

type Node interface {
	Name() string
	RunTask(task *Task) (int, error) // TODO: we need cancellation
	Terminate() error
}

type nodeState struct {
	scheduler *Scheduler

	node   Node
	status NodeStatus
	tasks  []*Task
	log    *slog.Logger

	nodeName      string
	earliestStart time.Time
}

func (ns *nodeState) UpdateStatus(status NodeStatus) {
	ns.status = status
	ns.scheduler.broadcast(EventNodeStatusUpdated{Node: ns.nodeName, Status: status})
}
