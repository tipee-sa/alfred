package main

import (
	"testing"
	"time"

	"github.com/fatih/color"
	"github.com/gammadia/alfred/proto"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func psTestJob(completed bool, statuses ...proto.TaskStatus_Status) *proto.JobStatus {
	j := &proto.JobStatus{
		Name:        "my-job-x7k2",
		StartedBy:   "dorian",
		ScheduledAt: timestamppb.New(time.Date(2026, 11, 3, 9, 12, 45, 0, time.UTC)),
	}
	for i, s := range statuses {
		j.Tasks = append(j.Tasks, &proto.TaskStatus{Name: string(rune('a' + i)), Status: s})
	}
	if completed {
		j.CompletedAt = timestamppb.New(time.Date(2026, 11, 3, 9, 20, 0, 0, time.UTC))
	}
	return j
}

func TestPsState(t *testing.T) {
	assert.Equal(t, "queued", psState(psTestJob(false, proto.TaskStatus_QUEUED, proto.TaskStatus_QUEUED)))
	assert.Equal(t, "running", psState(psTestJob(false, proto.TaskStatus_QUEUED, proto.TaskStatus_RUNNING)))
	assert.Equal(t, "running", psState(psTestJob(false, proto.TaskStatus_QUEUED, proto.TaskStatus_COMPLETED)), "a task has ended, so the job has started")
	assert.Equal(t, "completed", psState(psTestJob(true, proto.TaskStatus_COMPLETED, proto.TaskStatus_FAILED)))
}

func TestPsLine(t *testing.T) {
	defer func(noColor bool) { color.NoColor = noColor }(color.NoColor)
	color.NoColor = true

	job := psTestJob(false, proto.TaskStatus_QUEUED, proto.TaskStatus_RUNNING, proto.TaskStatus_COMPLETED)
	assert.Equal(t, "2026-11-03 09:12:45 +0000 UTC  dorian        my-job-x7k2  running        1/3  1 queued, 1 running, 1 completed", psLine(job, 11))

	job = psTestJob(true, proto.TaskStatus_COMPLETED, proto.TaskStatus_FAILED)
	job.Tasks[1].ExitCode = lo.ToPtr[int32](42)
	assert.Equal(t, "2026-11-03 09:12:45 +0000 UTC  dorian        my-job-x7k2      completed      2/2  1 warning, 1 completed", psLine(job, 15), "padded to the longest name")
}
