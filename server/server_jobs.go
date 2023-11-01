package main

import (
	"context"

	"github.com/gammadia/alfred/proto"
	schedulerpkg "github.com/gammadia/alfred/scheduler"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *server) ScheduleJob(ctx context.Context, in *proto.ScheduleJobRequest) (*proto.ScheduleJobResponse, error) {
	jobName, err := scheduler.Schedule(&schedulerpkg.Job{Job: in.Job})
	return &proto.ScheduleJobResponse{Name: jobName}, err
}

func (s *server) WatchJob(req *proto.WatchJobRequest, srv proto.Alfred_WatchJobServer) error {
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
	defer cancel()

	sync := func() (error, bool) {
		for _, job := range serverStatus.Jobs {
			if job.Name == req.Name {
				if err := srv.Send(job); err != nil {
					return err, true
				}
				if job.CompletedAt != nil {
					return nil, true
				}
				return nil, false
			}
		}
		return status.Errorf(codes.NotFound, "job '%s' not found", req.Name), true
	}

	if err, ret := sync(); ret {
		return err
	}

	for {
		select {
		case <-srv.Context().Done():
			return nil
		case <-channel:
			if err, ret := sync(); ret {
				return err
			}
		}
	}
}

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
		return srv.Send(&proto.JobsList{Jobs: serverStatus.Jobs})
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
