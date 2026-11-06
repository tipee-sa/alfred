package main

import (
	"context"
	"time"

	"github.com/gammadia/alfred/proto"
	schedulerpkg "github.com/gammadia/alfred/scheduler"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	goproto "google.golang.org/protobuf/proto"
)

func (s *server) CancelJob(ctx context.Context, req *proto.CancelJobRequest) (*proto.CancelJobResponse, error) {
	if !safeJobNameRegex.MatchString(req.Name) {
		return nil, status.Errorf(codes.InvalidArgument, "invalid job name %q", req.Name)
	}
	return &proto.CancelJobResponse{}, scheduler.CancelJob(req.Name)
}

func (s *server) CancelTask(ctx context.Context, req *proto.CancelTaskRequest) (*proto.CancelTaskResponse, error) {
	if err := validateJobTask(req.Job, req.Task); err != nil {
		return nil, err
	}
	return &proto.CancelTaskResponse{}, scheduler.CancelTask(req.Job, req.Task)
}

func (s *server) ScheduleJob(ctx context.Context, in *proto.ScheduleJobRequest) (*proto.ScheduleJobResponse, error) {
	if in.Job == nil {
		return nil, status.Errorf(codes.InvalidArgument, "job is required")
	}
	if !safeJobNameRegex.MatchString(in.Job.Name) {
		return nil, status.Errorf(codes.InvalidArgument, "invalid job name %q", in.Job.Name)
	}
	for _, task := range in.Job.Tasks {
		if !safeTaskNameRegex.MatchString(task.Name) {
			return nil, status.Errorf(codes.InvalidArgument, "invalid task name %q", task.Name)
		}
	}
	jobName, err := scheduler.Schedule(&schedulerpkg.Job{
		Job:         in.Job,
		Jobfile:     in.Jobfile,
		CommandLine: in.CommandLine,
		StartedBy:   in.StartedBy,
	})
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%s", err)
	}
	return &proto.ScheduleJobResponse{Name: jobName}, nil
}

// WatchJob is a server-streaming gRPC handler (one goroutine per connected client).
// It registers a filtered event listener, sends the current state, then loops:
// on each event, re-reads the full job state and sends it to the client.
//
// The select blocks until either:
//   - The client disconnects (srv.Context().Done())
//   - An event arrives for this job (channel)
//
// The handler automatically exits (and cleans up via defer cancel) when the job completes
// or the client disconnects.
func (s *server) WatchJob(req *proto.WatchJobRequest, srv proto.Alfred_WatchJobServer) error {
	// Register a listener that only receives events for the requested job
	channel, cancel := addClientListener(func(event schedulerpkg.Event) bool {
		switch event := event.(type) {
		case schedulerpkg.EventJobScheduled:
			return event.Job == req.Name
		case schedulerpkg.EventJobCompleted:
			return event.Job == req.Name
		case schedulerpkg.EventTaskQueued:
			return event.Job == req.Name
		case schedulerpkg.EventTaskRunning:
			return event.Job == req.Name
		case schedulerpkg.EventTaskAborted:
			return event.Job == req.Name
		case schedulerpkg.EventTaskFailed:
			return event.Job == req.Name
		case schedulerpkg.EventTaskSkipped:
			return event.Job == req.Name
		case schedulerpkg.EventTaskTimedOut:
			return event.Job == req.Name
		case schedulerpkg.EventTaskCompleted:
			return event.Job == req.Name
		default:
			return false
		}
	})
	defer cancel() // always unregister listener on exit (prevents leak)

	// sync reads the current state under read lock, deep-copies it (so Send doesn't
	// hold the lock), and sends to the client. Returns (err, shouldReturn).
	sync := func() (error, bool) {
		serverStatusMutex.RLock()

		var jobToSend *proto.JobStatus
		for _, job := range serverStatus.Jobs {
			if job.Name == req.Name {
				jobToSend = goproto.Clone(job).(*proto.JobStatus) // deep copy to avoid races
				break
			}
		}

		serverStatusMutex.RUnlock() // release BEFORE Send (which may block on network I/O)

		if jobToSend == nil {
			return status.Errorf(codes.NotFound, "job '%s' not found", req.Name), true
		}
		if err := srv.Send(jobToSend); err != nil {
			return err, true
		}
		if jobToSend.CompletedAt != nil {
			return nil, true // job is done, end the stream
		}
		return nil, false
	}

	// Send initial state immediately
	if err, ret := sync(); ret {
		return err
	}

	// Event loop: wait for new events or client disconnect
	for {
		select {
		case <-srv.Context().Done(): // client disconnected or deadline exceeded
			return nil
		case <-channel: // event for this job received from listenEvents
			if err, ret := sync(); ret {
				return err
			}
		}
	}
}

// jobsListLimitForTop is how many of the latest jobs WatchStatus sends besides the running
// ones: what `top` shows (50).
const jobsListLimitForTop = 50

// jobsForList returns copies of the jobs a list sends: the latest `limit` (0: all) and every
// one still running, in order, without the jobfile and command line, which only `show` reads
// (through WatchJob). The job history makes the full list months long, megabytes in all.
// Must be called with serverStatusMutex held.
func jobsForList(jobs []*proto.JobStatus, limit int) []*proto.JobStatus {
	first := 0
	if limit > 0 && len(jobs) > limit {
		first = len(jobs) - limit
	}
	var list []*proto.JobStatus
	for i, job := range jobs {
		if i < first && job.CompletedAt != nil {
			continue
		}
		copied := goproto.Clone(job).(*proto.JobStatus) // deep copy to avoid races
		copied.Jobfile, copied.CommandLine = "", ""
		list = append(list, copied)
	}
	return list
}

// WatchJobs is similar to WatchJob but streams the jobs list (see jobsForList), filtered to
// job-level events only (scheduled/completed). Used by the `ps` command.
// Same select pattern: blocks until event or client disconnect.
func (s *server) WatchJobs(req *proto.WatchJobsRequest, srv proto.Alfred_WatchJobsServer) error {
	channel, cancel := addClientListener(func(event schedulerpkg.Event) bool {
		switch event.(type) {
		case schedulerpkg.EventJobScheduled, schedulerpkg.EventJobCompleted:
			return true
		default:
			return false
		}
	})
	defer cancel()

	sync := func() error {
		serverStatusMutex.RLock()
		listToSend := &proto.JobsList{Jobs: jobsForList(serverStatus.Jobs, int(req.Limit))}
		serverStatusMutex.RUnlock()

		return srv.Send(listToSend)
	}

	if err := sync(); err != nil {
		return err
	}

	for {
		select {
		case <-srv.Context().Done():
			return nil
		case <-channel:
			if err := sync(); err != nil {
				return err
			}
		}
	}
}

// WatchStatus streams the server status (nodes, jobs as jobsForList, scheduler config).
// Used by the `top` command. Events are throttled: incoming events set a dirty flag,
// and the status is sent at most once per second to avoid overwhelming slow clients.
func (s *server) WatchStatus(req *proto.WatchStatusRequest, srv proto.Alfred_WatchStatusServer) error {
	channel, cancel := addClientListener(nil)
	defer cancel()

	sync := func() error {
		serverStatusMutex.RLock()
		statusToSend := &proto.Status{ // every field of Status, its jobs limited
			Server:    goproto.Clone(serverStatus.Server).(*proto.Status_Server),
			Scheduler: goproto.Clone(serverStatus.Scheduler).(*proto.Status_Scheduler),
			Jobs:      jobsForList(serverStatus.Jobs, jobsListLimitForTop),
		}
		for _, node := range serverStatus.Nodes {
			statusToSend.Nodes = append(statusToSend.Nodes, goproto.Clone(node).(*proto.NodeStatus))
		}
		serverStatusMutex.RUnlock()

		return srv.Send(statusToSend)
	}

	// Send initial state immediately
	if err := sync(); err != nil {
		return err
	}

	dirty := false
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-srv.Context().Done():
			return nil
		case <-channel:
			dirty = true
			// Drain any additional queued events to coalesce into a single update
		drain:
			for {
				select {
				case <-channel:
				default:
					break drain
				}
			}
		case <-ticker.C:
			if !dirty {
				continue
			}
			dirty = false
			if err := sync(); err != nil {
				return err
			}
		}
	}
}
