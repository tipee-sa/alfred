package main

import (
	"fmt"
	"io"
	"os"
	"path"

	"github.com/gammadia/alfred/proto"
	"github.com/gammadia/alfred/server/log"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *server) DownloadArtifact(req *proto.DownloadArtifactRequest, srv proto.Alfred_DownloadArtifactServer) error {
	if err := validateJobTask(req.Job, req.Task); err != nil {
		return err
	}

	artifact := path.Join(dataRoot, "artifacts", req.Job, fmt.Sprintf("%s.tar.zst", req.Task))

	var reader io.ReadCloser
	source := "finalized"

	if _, err := os.Stat(artifact); err == nil {
		// Completed task: read from finalized artifact file
		file, err := os.Open(artifact)
		if err != nil {
			log.Error("Failed to open artifact file", "job", req.Job, "task", req.Task, "artifact", artifact, "error", err)
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
		log.Debug("Live artifact reader obtained, starting read loop", "job", req.Job, "task", req.Task)
		reader = liveReader
	}
	defer func() {
		if err := reader.Close(); err != nil {
			log.Warn("Error closing artifact reader", "job", req.Job, "task", req.Task, "source", source, "error", err)
		}
	}()

	totalBytes := 0
	chunk := make([]byte, 256*1024)
	for {
		n, err := reader.Read(chunk)
		if n > 0 {
			totalBytes += n
			if sendErr := srv.Send(&proto.DownloadArtifactChunk{
				Data:   chunk[:n],
				Length: uint32(n),
			}); sendErr != nil {
				log.Error("Failed to send artifact chunk", "job", req.Job, "task", req.Task, "source", source, "totalBytes", totalBytes, "error", sendErr)
				return fmt.Errorf("failed to send artifact chunk: %w", sendErr)
			}
		}
		if err == io.EOF {
			if totalBytes == 0 {
				log.Warn("Empty artifact stream", "job", req.Job, "task", req.Task, "source", source)
				return status.Errorf(codes.Internal, "artifact stream was empty (archive command may have failed)")
			}
			log.Debug("Artifact download complete", "job", req.Job, "task", req.Task, "source", source, "totalBytes", totalBytes)
			return nil
		}
		if err != nil {
			log.Error("Failed to read artifact chunk", "job", req.Job, "task", req.Task, "source", source, "totalBytes", totalBytes, "error", err)
			return fmt.Errorf("failed to read artifact chunk: %w", err)
		}
	}
}
