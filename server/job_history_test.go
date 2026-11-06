package main

import (
	"fmt"
	"os"
	"path"
	"testing"
	"time"

	"github.com/gammadia/alfred/proto"
	schedulerpkg "github.com/gammadia/alfred/scheduler"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	goproto "google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// withJobHistory gives the test an empty data root and job list, restored afterwards.
func withJobHistory(t *testing.T) {
	oldDataRoot, oldJobs := dataRoot, serverStatus.Jobs
	dataRoot = t.TempDir()
	serverStatus.Jobs = nil
	t.Cleanup(func() { dataRoot, serverStatus.Jobs = oldDataRoot, oldJobs })
}

func TestJobHistory_ACompletedJobSurvivesARestart(t *testing.T) {
	withJobHistory(t)

	applyEvents(
		schedulerpkg.EventJobScheduled{Job: "job-abc", About: "about", Tasks: []string{"a", "b"}, StartedBy: "dorian"},
		schedulerpkg.EventTaskQueued{Job: "job-abc", Task: "a"},
		schedulerpkg.EventTaskQueued{Job: "job-abc", Task: "b"},
		schedulerpkg.EventTaskRunning{Job: "job-abc", Task: "a"},
		schedulerpkg.EventTaskFailed{Job: "job-abc", Task: "a", ExitCode: 42},
		schedulerpkg.EventTaskAborted{Job: "job-abc", Task: "b"},
		schedulerpkg.EventJobCompleted{Job: "job-abc"},
		schedulerpkg.EventJobScheduled{Job: "job-running", Tasks: []string{"a"}},
	)
	require.Len(t, serverStatus.Jobs, 2)
	completed := goproto.Clone(serverStatus.Jobs[0]).(*proto.JobStatus)

	// The restart: memory is gone, the running job with it
	serverStatus.Jobs = nil
	loadJobHistory()

	require.Len(t, serverStatus.Jobs, 1)
	assert.True(t, goproto.Equal(completed, serverStatus.Jobs[0]), "restored %v, want %v", serverStatus.Jobs[0], completed)
}

func TestJobHistory_LoadsOldestFirstAndSkipsWhatItCannotRead(t *testing.T) {
	withJobHistory(t)

	base := time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)
	for i, name := range []string{"job-z", "job-a", "job-m"} { // directory order is not job order
		data, err := marshalJob(&proto.JobStatus{
			Name:        name,
			ScheduledAt: timestamppb.New(base.Add(time.Duration(i) * time.Hour)),
			CompletedAt: timestamppb.New(base.Add(time.Duration(i)*time.Hour + time.Minute)),
		})
		require.NoError(t, err)
		saveJob(name, data)
	}
	artifacts := path.Join(dataRoot, "artifacts")
	require.NoError(t, os.MkdirAll(path.Join(artifacts, "job-old"), 0755)) // from before the history
	require.NoError(t, os.MkdirAll(path.Join(artifacts, "job-broken"), 0755))
	require.NoError(t, os.WriteFile(path.Join(artifacts, "job-broken", jobHistoryFile), []byte("{"), 0644))

	loadJobHistory()

	names := make([]string, len(serverStatus.Jobs))
	for i, job := range serverStatus.Jobs {
		names[i] = job.Name
	}
	assert.Equal(t, []string{"job-z", "job-a", "job-m"}, names)
}

func TestJobHistory_NoArtifactsDirectoryIsNoHistory(t *testing.T) {
	withJobHistory(t)

	loadJobHistory()

	assert.Empty(t, serverStatus.Jobs)
}

func TestJobsForList_LatestJobsAndTheRunningOnesWithoutTheirJobfile(t *testing.T) {
	done := timestamppb.Now()
	jobs := []*proto.JobStatus{
		{Name: "old-running", Jobfile: "tasks: [a]", CommandLine: "alfred run"},
		{Name: "old-done", CompletedAt: done},
		{Name: "recent-done", CompletedAt: done, Jobfile: "tasks: [b]"},
		{Name: "recent-running"},
	}

	names := func(list []*proto.JobStatus) []string {
		out := make([]string, len(list))
		for i, job := range list {
			out[i] = job.Name
		}
		return out
	}
	assert.Equal(t, []string{"old-running", "recent-done", "recent-running"}, names(jobsForList(jobs, 2)))
	assert.Equal(t, []string{"old-running", "old-done", "recent-done", "recent-running"}, names(jobsForList(jobs, 0)))

	list := jobsForList(jobs, 0)
	assert.Empty(t, list[0].Jobfile)
	assert.Empty(t, list[0].CommandLine)
	assert.Equal(t, "tasks: [a]", jobs[0].Jobfile, "the server's own copy keeps it")
}

// With every artifact kept (--artifact-retention 0), startup reads the newest directories
// until it has maxCompletedJobs jobs, never the whole history; ones without a usable
// job.json take no place.
func TestJobHistory_ReadsOnlyTheNewestDirectories(t *testing.T) {
	withJobHistory(t)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i <= maxCompletedJobs; i++ {
		name := fmt.Sprintf("job-%04d", i)
		at := base.Add(time.Duration(i) * time.Minute)
		data, err := marshalJob(&proto.JobStatus{Name: name, ScheduledAt: timestamppb.New(at), CompletedAt: timestamppb.New(at)})
		require.NoError(t, err)
		saveJob(name, data)
		require.NoError(t, os.Chtimes(path.Join(dataRoot, "artifacts", name), at, at))
	}
	newest := base.Add(time.Duration(maxCompletedJobs+1) * time.Minute)
	for _, name := range []string{"job-without-history", "job-broken"} {
		dir := path.Join(dataRoot, "artifacts", name)
		require.NoError(t, os.MkdirAll(dir, 0755))
		if name == "job-broken" {
			require.NoError(t, os.WriteFile(path.Join(dir, jobHistoryFile), []byte("{"), 0644))
		}
		require.NoError(t, os.Chtimes(dir, newest, newest))
	}

	loadJobHistory()

	require.Len(t, serverStatus.Jobs, maxCompletedJobs)
	assert.Equal(t, "job-0001", serverStatus.Jobs[0].Name, "the oldest directory is left out")
	assert.Equal(t, fmt.Sprintf("job-%04d", maxCompletedJobs), serverStatus.Jobs[maxCompletedJobs-1].Name)
}
