package main

import (
	"context"

	"github.com/gammadia/alfred/proto"
	schedulerpkg "github.com/gammadia/alfred/scheduler"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	goproto "google.golang.org/protobuf/proto"
)

func (s *server) ScheduleJob(ctx context.Context, in *proto.ScheduleJobRequest) (*proto.ScheduleJobResponse, error) {
	jobName, err := scheduler.Schedule(&schedulerpkg.Job{
		Job:         in.Job,
		Jobfile:     in.Jobfile,
		CommandLine: in.CommandLine,
		StartedBy:   in.StartedBy,
	})
	return &proto.ScheduleJobResponse{Name: jobName}, err
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

// WatchJobs is similar to WatchJob but streams the full jobs list, filtered to
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
		listToSend := goproto.Clone(&proto.JobsList{Jobs: serverStatus.Jobs}).(*proto.JobsList)
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
