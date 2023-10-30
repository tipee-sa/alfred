package main

import (
	"context"

	"github.com/gammadia/alfred/proto"
	schedulerpkg "github.com/gammadia/alfred/scheduler"
)

type server struct {
	proto.UnimplementedAlfredServer
}

func (s *server) ScheduleJob(ctx context.Context, in *proto.ScheduleJobRequest) (*proto.ScheduleJobResponse, error) {
	scheduler.Schedule(&schedulerpkg.Job{Job: in.Job})
	return &proto.ScheduleJobResponse{}, nil
}

func (s *server) Ping(ctx context.Context, in *proto.PingRequest) (*proto.PingResponse, error) {
	return &proto.PingResponse{
		Version: version,
		Commit:  commit,
	}, nil
}
