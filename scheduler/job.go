package scheduler

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/gammadia/alfred/proto"
)

type Job struct {
	*proto.Job

	id string
	// When Schedule() accepted it, for the job's duration
	scheduledAt time.Time

	tasksCompleted atomic.Uint32

	Jobfile     string
	CommandLine string
	StartedBy   string
}

func (j *Job) FQN() string {
	return fmt.Sprintf("%s-%s", j.Name, j.id)
}
