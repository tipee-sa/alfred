package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"

	"github.com/gammadia/alfred/proto"
	"github.com/gammadia/alfred/server/config"
	"github.com/gammadia/alfred/server/log"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *server) DownloadArtifact(req *proto.DownloadArtifactRequest, srv proto.Alfred_DownloadArtifactServer) error {
	artifact := path.Join(dataRoot, "artifacts", req.Job, fmt.Sprintf("%s.tar.zst", req.Task))

	var reader io.ReadCloser
	source := "finalized"

	if _, err := os.Stat(artifact); err == nil {
		// Completed task: read from finalized artifact file
		file, err := os.Open(artifact)
		if err != nil {
			return fmt.Errorf("failed to open artifact file: %w", err)
		}
		reader = file
	} else {
		// Running task: stream live snapshot from workspace
		source = "live"
		log.Debug("Attempting live artifact download", "job", req.Job, "task", req.Task)
		liveReader, liveErr := scheduler.ArchiveLiveArtifact(req.Job, req.Task)
		if liveErr != nil {
			log.Warn("Artifact not found", "job", req.Job, "task", req.Task, "liveError", liveErr)
			return status.Errorf(codes.NotFound, "artifact not found")
		}
		reader = liveReader
	}
	defer func() {
		if err := reader.Close(); err != nil {
			log.Warn("Error closing artifact reader", "job", req.Job, "task", req.Task, "source", source, "error", err)
		}
	}()

	totalBytes := 0
	chunk := make([]byte, config.MaxPacketSize-1024*1024 /* leave 1MB margin */)
	for {
		n, err := io.ReadFull(reader, chunk)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			if err == io.EOF {
				if totalBytes == 0 {
					log.Warn("Empty artifact stream", "job", req.Job, "task", req.Task, "source", source)
					return status.Errorf(codes.Internal, "artifact stream was empty (archive command may have failed)")
				}
				log.Debug("Artifact download complete", "job", req.Job, "task", req.Task, "source", source, "totalBytes", totalBytes)
				return nil
			} else {
				return fmt.Errorf("failed to read artifact chunk: %w", err)
			}
		} else {
			totalBytes += n
			if err = srv.Send(&proto.DownloadArtifactChunk{
				Data:   chunk[:n],
				Length: uint32(n),
			}); err != nil {
				return fmt.Errorf("failed to request for artifact chunk: %w", err)
			}
		}
	}
}
