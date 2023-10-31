package scheduler

import (
	"fmt"
	"log/slog"
)

type TaskStatus string

const (
	TaskStatusPending   TaskStatus = "pending"
	TaskStatusRunning   TaskStatus = "running"
	TaskStatusAborted   TaskStatus = "aborted"
	TaskStatusFailed    TaskStatus = "failed"
	TaskStatusCompleted TaskStatus = "completed"
)

type Task struct {
	Job    *Job
	Name   string
	Status TaskStatus

	log *slog.Logger
}

func (t *Task) FQN() string {
	return fmt.Sprintf("%s-%s", t.Job.FQN(), t.Name)
}
