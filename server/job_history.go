package main

import (
	"os"
	"path"
	"slices"
	"sort"

	"github.com/gammadia/alfred/proto"
	"github.com/gammadia/alfred/server/log"
	"google.golang.org/protobuf/encoding/protojson"
)

// The server keeps its jobs in memory. So that a restart (a deploy, the host's reboots) does
// not forget them, each completed job is saved beside its artifacts, in
// <server-data>/artifacts/<job>/job.json, and the saved jobs are loaded back at startup. They
// share the artifacts' retention (--artifact-retention deletes the whole directory).
const jobHistoryFile = "job.json"

// marshalJob encodes a job for saveJob. Must be called with serverStatusMutex held.
func marshalJob(job *proto.JobStatus) ([]byte, error) {
	return protojson.MarshalOptions{Multiline: true}.Marshal(job)
}

// saveJob writes a completed job's status to disk, atomically: a crash midway leaves the
// previous file or none, never a truncated one. A failure costs only the job's history.
func saveJob(name string, data []byte) {
	dir := path.Join(dataRoot, "artifacts", name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Warn("Failed to save job history", "job", name, "error", err)
		return
	}
	file := path.Join(dir, jobHistoryFile)
	if err := os.WriteFile(file+".tmp", data, 0644); err != nil {
		log.Warn("Failed to save job history", "job", name, "error", err)
		return
	}
	if err := os.Rename(file+".tmp", file); err != nil {
		log.Warn("Failed to save job history", "job", name, "error", err)
	}
}

// loadJobHistory puts the saved jobs into serverStatus, oldest first, keeping the latest
// maxCompletedJobs. Runs at startup, before the scheduler emits any event.
func loadJobHistory() {
	artifactsDir := path.Join(dataRoot, "artifacts")
	entries, err := os.ReadDir(artifactsDir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Warn("Failed to read job history", "error", err)
		}
		return
	}

	var jobs []*proto.JobStatus
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(path.Join(artifactsDir, entry.Name(), jobHistoryFile))
		if err != nil {
			if !os.IsNotExist(err) { // jobs from before the history have none
				log.Warn("Failed to read saved job", "job", entry.Name(), "error", err)
			}
			continue
		}
		job := &proto.JobStatus{}
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, job); err != nil {
			log.Warn("Failed to decode saved job", "job", entry.Name(), "error", err)
			continue
		}
		if job.Name != entry.Name() || job.CompletedAt == nil {
			log.Warn("Ignoring inconsistent saved job", "job", entry.Name(), "savedName", job.Name)
			continue
		}
		jobs = append(jobs, job)
	}

	sort.SliceStable(jobs, func(i, j int) bool {
		return jobs[i].ScheduledAt.AsTime().Before(jobs[j].ScheduledAt.AsTime())
	})
	if len(jobs) > maxCompletedJobs {
		// A copy, not a reslice: the backing array would keep the dropped jobs alive
		jobs = slices.Clone(jobs[len(jobs)-maxCompletedJobs:])
	}

	serverStatusMutex.Lock()
	serverStatus.Jobs = append(jobs, serverStatus.Jobs...)
	serverStatusMutex.Unlock()
	log.Info("Loaded job history", "jobs", len(jobs))
}
