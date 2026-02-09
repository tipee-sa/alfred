package main

import (
	"context"
	"regexp"

	"github.com/gammadia/alfred/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type server struct {
	proto.UnimplementedAlfredServer
}

// safeNameRegex matches valid job FQNs (e.g. "my-job-sunny-fox") and task names
// (e.g. "task-1"). Rejects path traversal characters like ".." and "/".
var safeNameRegex = regexp.MustCompile(`^[a-z][a-z0-9_-]+$`)

func validateJobTask(job, task string) error {
	if !safeNameRegex.MatchString(job) {
		return status.Errorf(codes.InvalidArgument, "invalid job name %q", job)
	}
	if !safeNameRegex.MatchString(task) {
		return status.Errorf(codes.InvalidArgument, "invalid task name %q", task)
	}
	return nil
}

func (s *server) Ping(ctx context.Context, in *proto.PingRequest) (*proto.PingResponse, error) {
	return &proto.PingResponse{
		Version: version,
		Commit:  commit,
	}, nil
}
