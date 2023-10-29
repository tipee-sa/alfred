package main

import (
	"context"
	"fmt"

	"github.com/gammadia/alfred/proto"
)

type server struct {
	proto.UnimplementedAlfredServer
}

func (s *server) ScheduleJob(ctx context.Context, in *proto.ScheduleJobRequest) (*proto.ScheduleJobResponse, error) {
	return &proto.ScheduleJobResponse{}, fmt.Errorf("i do not know how to schedule a job")
}

func (s *server) Ping(ctx context.Context, in *proto.PingRequest) (*proto.PingResponse, error) {
	return &proto.PingResponse{
		Version: version,
		Commit:  commit,
	}, nil
}
