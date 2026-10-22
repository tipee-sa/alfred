package main

import (
	"errors"
	"testing"

	"github.com/gammadia/alfred/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestArtifactCmd_UsageNamesJobAndTasks(t *testing.T) {
	assert.Equal(t, "artifact JOB [TASK...]", artifactCmd.Use)
}

func artifactTestJob() *proto.JobStatus {
	return &proto.JobStatus{Name: "job-abc", Tasks: []*proto.TaskStatus{
		{Name: "done", Status: proto.TaskStatus_COMPLETED},
		{Name: "broken", Status: proto.TaskStatus_FAILED},
		{Name: "busy", Status: proto.TaskStatus_RUNNING},
		{Name: "waiting", Status: proto.TaskStatus_QUEUED},
	}}
}

func TestSelectArtifactTasks_All(t *testing.T) {
	downloadable, unknown, partial := selectArtifactTasks(artifactTestJob(), nil)
	assert.Equal(t, []string{"done", "broken", "busy"}, downloadable)
	assert.Empty(t, unknown)
	assert.True(t, partial)
}

func TestSelectArtifactTasks_RequestedWithUnknown(t *testing.T) {
	downloadable, unknown, partial := selectArtifactTasks(artifactTestJob(), []string{"nope", "broken", "done", "zzz", "nope"})
	assert.Equal(t, []string{"done", "broken"}, downloadable)
	assert.Equal(t, []string{"nope", "zzz"}, unknown, "in the order requested, once each")
	assert.False(t, partial)
}

func TestSelectArtifactTasks_RequestedRunningIsPartial(t *testing.T) {
	downloadable, unknown, partial := selectArtifactTasks(artifactTestJob(), []string{"busy"})
	assert.Equal(t, []string{"busy"}, downloadable)
	assert.Empty(t, unknown)
	assert.True(t, partial)
}

// A job cancelled with tasks still queued completes with them aborted, never
// run: they have no artifact, so asking for one would fail the command.
func TestSelectArtifactTasks_AbortedBeforeStartingHasNoArtifact(t *testing.T) {
	job := &proto.JobStatus{Name: "job-abc", Tasks: []*proto.TaskStatus{
		{Name: "cut-short", Status: proto.TaskStatus_ABORTED, StartedAt: timestamppb.Now()},
		{Name: "never-ran", Status: proto.TaskStatus_ABORTED},
	}}
	downloadable, unknown, partial := selectArtifactTasks(job, nil)
	assert.Equal(t, []string{"cut-short"}, downloadable)
	assert.Empty(t, unknown)
	assert.False(t, partial)

	downloadable, unknown, _ = selectArtifactTasks(job, []string{"never-ran"})
	assert.Empty(t, downloadable)
	assert.Empty(t, unknown, "it exists, it just has no artifact")
}

func TestDownloadArtifacts_GoesOnPastFailures(t *testing.T) {
	var progress []int
	var attempted []string
	files, errs := downloadArtifacts([]string{"a", "b", "c"}, "out/dir", func(task, file string) error {
		attempted = append(attempted, file)
		if task == "b" {
			return errors.New("failed to download artifact for task 'b'")
		}
		return nil
	}, func(done int) { progress = append(progress, done) })

	assert.Equal(t, []string{"out/dir/a.tar.zst", "out/dir/b.tar.zst", "out/dir/c.tar.zst"}, attempted)
	assert.Equal(t, []string{"out/dir/a.tar.zst", "out/dir/c.tar.zst"}, files, "only the files written")
	assert.Equal(t, []string{"failed to download artifact for task 'b'"}, errs)
	assert.Equal(t, []int{1, 2, 3}, progress)
}

func TestArtifactProblems(t *testing.T) {
	assert.NoError(t, artifactProblems(0, 0))

	err := artifactProblems(2, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "2 requested task(s) not found")

	err = artifactProblems(0, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 download(s) failed")

	err = artifactProblems(1, 3)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 requested task(s) not found, 3 download(s) failed")
}
