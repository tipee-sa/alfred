package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/gammadia/alfred/proto"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func exitCode(code int32) *int32 {
	return &code
}

func decodeJSONLines(t *testing.T, output string) []jobStatusJSON {
	t.Helper()
	var statuses []jobStatusJSON
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		var status jobStatusJSON
		require.NoError(t, json.Unmarshal([]byte(line), &status), "each line must be one JSON object: %q", line)
		statuses = append(statuses, status)
	}
	return statuses
}

func TestNewJobStatusJSON_StatusesAndCounts(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	msg := &proto.JobStatus{
		ScheduledAt: timestamppb.New(now.Add(-time.Hour)),
		Tasks: []*proto.TaskStatus{
			{Name: "a", Status: proto.TaskStatus_QUEUED},
			{Name: "b", Status: proto.TaskStatus_RUNNING, StartedAt: timestamppb.New(now)},
			{Name: "c", Status: proto.TaskStatus_FAILED, ExitCode: exitCode(42)},
			{Name: "d", Status: proto.TaskStatus_FAILED, ExitCode: exitCode(1)},
			{Name: "e", Status: proto.TaskStatus_SKIPPED, ExitCode: exitCode(43)},
			{Name: "f", Status: proto.TaskStatus_TIMED_OUT},
			{Name: "g", Status: proto.TaskStatus_COMPLETED, ExitCode: exitCode(0)},
		},
	}

	status := newJobStatusJSON("job", msg)
	tasks := *status.Tasks

	assert.Equal(t, "running", status.State)
	assert.Nil(t, status.CompletedAt)
	assert.Equal(t, []string{"queued", "running", "warning", "failed", "skipped", "timed_out", "completed"},
		lo.Map(tasks, func(task taskJSON, _ int) string { return task.Status }))
	assert.Equal(t, map[string]int{
		"total": 7, "queued": 1, "running": 1, "aborted": 0, "skipped": 1,
		"timed_out": 1, "failed": 1, "warning": 1, "completed": 1,
	}, status.Counts)
	assert.Equal(t, now, *tasks[1].StartedAt)
	assert.Nil(t, tasks[0].ExitCode)
	assert.Equal(t, int32(42), *tasks[2].ExitCode)
}

func TestNewJobStatusJSON_Completed(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	status := newJobStatusJSON("job", completedJobStatus(now))

	assert.Equal(t, "completed", status.State)
	assert.Equal(t, now, *status.CompletedAt)
}

func TestNewJobStatusJSON_EmptyTasksEncodeAsArray(t *testing.T) {
	out, err := json.Marshal(newJobStatusJSON("job", &proto.JobStatus{ScheduledAt: timestamppb.Now()}))

	require.NoError(t, err)
	assert.Contains(t, string(out), `"tasks":[]`)
	assert.Contains(t, string(out), `"completed_at":null`)
}

func TestWatchJSON_FullThenChangedThenFull(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	job := func(statuses ...proto.TaskStatus_Status) *proto.JobStatus {
		msg := &proto.JobStatus{ScheduledAt: timestamppb.New(now.Add(-5 * time.Minute))}
		for i, s := range statuses {
			msg.Tasks = append(msg.Tasks, &proto.TaskStatus{Name: fmt.Sprintf("task-%d", i+1), Status: s})
		}
		return msg
	}
	completed := job(proto.TaskStatus_COMPLETED, proto.TaskStatus_COMPLETED, proto.TaskStatus_COMPLETED)
	completed.CompletedAt = timestamppb.New(now)

	msgCh := make(chan recvResult, 5)
	msgCh <- recvResult{msg: job(proto.TaskStatus_QUEUED, proto.TaskStatus_QUEUED, proto.TaskStatus_QUEUED)}
	msgCh <- recvResult{msg: job(proto.TaskStatus_RUNNING, proto.TaskStatus_QUEUED, proto.TaskStatus_QUEUED)}
	msgCh <- recvResult{msg: job(proto.TaskStatus_RUNNING, proto.TaskStatus_QUEUED, proto.TaskStatus_QUEUED)}
	msgCh <- recvResult{msg: completed}
	msgCh <- recvResult{err: io.EOF}

	var buf bytes.Buffer
	var seen int
	err := runWatchJSON(context.Background(), msgCh, "test-job", &buf, false, func(*proto.JobStatus) { seen++ })

	require.NoError(t, err)
	assert.NotContains(t, buf.String(), "\033", "JSON output must carry no terminal escape codes")
	assert.Equal(t, 4, seen, "onMessage runs for every message (abort-on-* logic)")
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	require.Len(t, lines, 4)
	assert.NotContains(t, lines[1], `"tasks"`, "a line between the first and the completed one is a delta")
	assert.NotContains(t, lines[3], `"changed"`)

	statuses := decodeJSONLines(t, buf.String())
	assert.Len(t, *statuses[0].Tasks, 3, "first line: every task")
	assert.Nil(t, statuses[0].Changed)
	assert.Equal(t, []taskJSON{{Name: "task-1", Status: "running"}}, *statuses[1].Changed)
	assert.Empty(t, *statuses[2].Changed, "nothing changed: an empty list, not a missing one")
	assert.Contains(t, lines[2], `"changed":[]`)
	assert.Equal(t, "completed", statuses[3].State)
	assert.Len(t, *statuses[3].Tasks, 3, "completed line: every task, so the last line stands alone")
	assert.Equal(t, 1, statuses[2].Counts["running"], "counts always cover every task")
}

func TestWatchJSON_OnceStopsAfterFirstMessage(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	running := &proto.JobStatus{
		ScheduledAt: timestamppb.New(now.Add(-5 * time.Minute)),
		Tasks:       []*proto.TaskStatus{{Name: "task-1", Status: proto.TaskStatus_RUNNING}},
	}

	// Only one message and no EOF: --once must not wait for the stream to end.
	msgCh := make(chan recvResult, 1)
	msgCh <- recvResult{msg: running}

	var buf bytes.Buffer
	err := runWatchJSON(context.Background(), msgCh, "test-job", &buf, true, nil)

	require.NoError(t, err)
	statuses := decodeJSONLines(t, buf.String())
	require.Len(t, statuses, 1)
	assert.Len(t, *statuses[0].Tasks, 1)
}

func TestWatchJSON_StreamErrorIsReturned(t *testing.T) {
	msgCh := make(chan recvResult, 1)
	msgCh <- recvResult{err: errors.New("job 'x' not found")}

	var buf bytes.Buffer
	err := runWatchJSON(context.Background(), msgCh, "x", &buf, false, nil)

	assert.EqualError(t, err, "job 'x' not found")
	assert.Empty(t, buf.String())
}

func TestWatchJSON_EOFBeforeCompletionIsAnError(t *testing.T) {
	// The server also ends the stream cleanly when it shuts down.
	msgCh := make(chan recvResult, 2)
	msgCh <- recvResult{msg: &proto.JobStatus{ScheduledAt: timestamppb.Now()}}
	msgCh <- recvResult{err: io.EOF}

	var buf bytes.Buffer
	err := runWatchJSON(context.Background(), msgCh, "test-job", &buf, false, nil)

	assert.EqualError(t, err, "stream ended before job 'test-job' completed")
}

func TestWatchJSON_InterruptionIsAnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var buf bytes.Buffer
	err := runWatchJSON(ctx, make(chan recvResult), "test-job", &buf, false, nil)

	assert.EqualError(t, err, "interrupted before job 'test-job' completed")
}
