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

// safeJobNameRegex matches valid job FQNs (e.g. "my-job-sunny-fox").
// Must start with a lowercase letter.
var safeJobNameRegex = regexp.MustCompile(`^[a-z][a-z0-9_-]+$`)

// safeTaskNameRegex matches valid task names (e.g. "task-1", "2ndfloor").
// Can start with a letter or digit. Rejects path traversal characters like ".." and "/".
var safeTaskNameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]+$`)

func validateJobTask(job, task string) error {
	if !safeJobNameRegex.MatchString(job) {
		return status.Errorf(codes.InvalidArgument, "invalid job name %q", job)
	}
	if !safeTaskNameRegex.MatchString(task) {
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
