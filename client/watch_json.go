package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gammadia/alfred/proto"
	"github.com/samber/lo"
)

// jobStatusJSON is the machine-readable form of a job status, written by `watch --json`.
// It is a stable contract for scripts: field names and task statuses only ever get added to.
//
// A line carries either every task (tasks) or only the tasks that changed since the previous
// line (changed), never both: a 2000-task job is ~200 KB in full, and it changes thousands of
// times per run. The first line and the completed line are full; the ones between are not.
type jobStatusJSON struct {
	Job string `json:"job"`
	// "running", then "completed" once every task has ended, however it ended: a cancelled
	// job (`alfred cancel`, --abort-on-*) completes too, its unfinished tasks "aborted".
	State       string         `json:"state"`
	ScheduledAt time.Time      `json:"scheduled_at"`
	CompletedAt *time.Time     `json:"completed_at"`
	Counts      map[string]int `json:"counts"`
	Tasks       *[]taskJSON    `json:"tasks,omitempty"`
	Changed     *[]taskJSON    `json:"changed,omitempty"`
}

type taskJSON struct {
	Name      string     `json:"name"`
	Status    string     `json:"status"`
	ExitCode  *int32     `json:"exit_code"`
	StartedAt *time.Time `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at"`
}

// taskStatuses lists every status `watch --json` reports, in display order. They mirror the
// text display's groups, so a FAILED task is split by exit code: 42 is "warning" (⚠️), any
// other code is "failed" (💥). Every status appears in counts, even at zero.
var taskStatuses = []string{"queued", "running", "aborted", "skipped", "timed_out", "failed", "warning", "completed"}

func taskStatusName(t *proto.TaskStatus) string {
	if t.Status == proto.TaskStatus_FAILED && t.ExitCode != nil && *t.ExitCode == 42 {
		return "warning"
	}
	return strings.ToLower(t.Status.String())
}

func newJobStatusJSON(jobName string, msg *proto.JobStatus) jobStatusJSON {
	out := jobStatusJSON{
		Job:         jobName,
		State:       "running",
		ScheduledAt: msg.ScheduledAt.AsTime(),
		Counts:      map[string]int{"total": len(msg.Tasks)},
	}
	tasks := make([]taskJSON, 0, len(msg.Tasks))
	if msg.CompletedAt != nil {
		out.State = "completed"
		out.CompletedAt = timePtr(msg.CompletedAt.AsTime())
	}
	for _, s := range taskStatuses {
		out.Counts[s] = 0
	}

	for _, t := range msg.Tasks {
		task := taskJSON{
			Name:     t.Name,
			Status:   taskStatusName(t),
			ExitCode: t.ExitCode,
		}
		if t.StartedAt != nil {
			task.StartedAt = timePtr(t.StartedAt.AsTime())
		}
		if t.EndedAt != nil {
			task.EndedAt = timePtr(t.EndedAt.AsTime())
		}
		out.Counts[task.Status]++
		tasks = append(tasks, task)
	}
	out.Tasks = &tasks
	return out
}

func timePtr(t time.Time) *time.Time {
	return &t
}

// runWatchJSON writes one JSON line per job status received from msgCh, until the stream ends.
// Unlike the text display, nothing is re-rendered on a timer: each line is a status change.
// With once, it returns after the first line.
//
// It returns nil only once it has written the job's final state (or, with once, its first
// line), so scripts can trust exit code 0. An interruption or a broken stream is an error.
func runWatchJSON(
	ctx context.Context,
	msgCh <-chan recvResult,
	jobName string,
	w io.Writer,
	once bool,
	onMessage func(*proto.JobStatus),
) error {
	encoder := json.NewEncoder(w)
	var previous map[string]string // task name → its JSON on the previous line; nil before the first
	completed := false
	for {
		var result recvResult
		select {
		case result = <-msgCh:
		case <-ctx.Done():
			return fmt.Errorf("interrupted before job '%s' completed", jobName)
		}
		if result.err == io.EOF {
			// The server ends the stream right after sending the completed state, but also
			// when it shuts down, so only the former is a success.
			if completed {
				return nil
			}
			return fmt.Errorf("stream ended before job '%s' completed", jobName)
		}
		if result.err != nil {
			return result.err
		}

		status := newJobStatusJSON(jobName, result.msg)
		completed = status.State == "completed"
		current := make(map[string]string, len(*status.Tasks))
		for _, task := range *status.Tasks {
			current[task.Name] = string(lo.Must(json.Marshal(task)))
		}
		if previous != nil && !completed {
			changed := lo.Filter(*status.Tasks, func(task taskJSON, _ int) bool {
				return previous[task.Name] != current[task.Name]
			})
			status.Tasks, status.Changed = nil, &changed
		}
		previous = current

		if err := encoder.Encode(status); err != nil {
			return err
		}
		if once {
			return nil
		}
		if onMessage != nil {
			onMessage(result.msg)
		}
	}
}
