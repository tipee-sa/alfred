package main

import (
	"context"

	"github.com/gammadia/alfred/proto"
)

type server struct {
	proto.UnimplementedAlfredServer
}

func (s *server) Ping(ctx context.Context, in *proto.PingRequest) (*proto.PingResponse, error) {
	return &proto.PingResponse{
		Version: version,
		Commit:  commit,
	}, nil
}
