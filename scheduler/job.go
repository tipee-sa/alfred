package scheduler

import (
	"fmt"

	"github.com/gammadia/alfred/namegen"
	"github.com/gammadia/alfred/proto"
)

type Job struct {
	*proto.Job

	id namegen.ID
}

func (j *Job) FQN() string {
	return fmt.Sprintf("%s-%s", j.Name, j.id)
}
