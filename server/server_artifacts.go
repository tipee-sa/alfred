package main

import (
	"errors"
	"fmt"
	"github.com/gammadia/alfred/server/config"
	"io"
	"os"
	"path"

	"github.com/gammadia/alfred/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *server) DownloadArtifact(req *proto.DownloadArtifactRequest, srv proto.Alfred_DownloadArtifactServer) error {
	artifact := path.Join(dataRoot, "artifacts", req.Job, fmt.Sprintf("%s.tar.gz", req.Task))

	if _, err := os.Stat(artifact); err != nil {
		return status.Errorf(codes.NotFound, "artifact not found")
	}

	file, err := os.Open(artifact)
	if err != nil {
		return fmt.Errorf("failed to open artifact file: %w", err)
	}

	chunk := make([]byte, config.MaxPacketSize-1024*1024 /* leave 1MB margin */)
	for {
		n, err := io.ReadFull(file, chunk)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			if err == io.EOF {
				return nil
			} else {
				return fmt.Errorf("read: %w", err)
			}
		} else {
			if err = srv.Send(&proto.DownloadArtifactChunk{
				Data:   chunk[:n],
				Length: uint32(n),
			}); err != nil {
				return fmt.Errorf("send: %w", err)
			}
		}
	}
}
