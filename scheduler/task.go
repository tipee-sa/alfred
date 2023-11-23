package scheduler

import (
	"fmt"
	"log/slog"

	"github.com/gammadia/alfred/proto"
)

type TaskStatus proto.TaskStatus_Status

const (
	TaskStatusQueued    = TaskStatus(proto.TaskStatus_QUEUED)
	TaskStatusRunning   = TaskStatus(proto.TaskStatus_RUNNING)
	TaskStatusAborted   = TaskStatus(proto.TaskStatus_ABORTED)
	TaskStatusFailed    = TaskStatus(proto.TaskStatus_FAILED)
	TaskStatusCompleted = TaskStatus(proto.TaskStatus_COMPLETED)
)

func (ts TaskStatus) AsProto() proto.TaskStatus_Status {
	return proto.TaskStatus_Status(ts)
}

type Task struct {
	Job  *Job
	Name string
	Log  *slog.Logger
}

func (t *Task) FQN() string {
	return fmt.Sprintf("%s-%s", t.Job.FQN(), t.Name)
}
