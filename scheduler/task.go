package scheduler

type TaskStatus string

const (
	TaskStatusPending   TaskStatus = "pending"
	TaskStatusRunning   TaskStatus = "running"
	TaskStatusAborted   TaskStatus = "aborted"
	TaskStatusFailed    TaskStatus = "failed"
	TaskStatusCompleted TaskStatus = "completed"
)

type Task struct {
	Job  *Job
	Name string

	Status TaskStatus
}
