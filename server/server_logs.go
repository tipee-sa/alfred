package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"

	"github.com/gammadia/alfred/proto"
	"github.com/gammadia/alfred/server/log"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *server) StreamTaskLogs(req *proto.StreamTaskLogsRequest, srv proto.Alfred_StreamTaskLogsServer) error {
	if err := validateJobTask(req.Job, req.Task); err != nil {
		return err
	}

	tailLines := int(req.TailLines)
	if tailLines <= 0 {
		tailLines = 100
	}

	var reader io.ReadCloser

	// Try finalized artifact first (completed task)
	artifact := path.Join(dataRoot, "artifacts", req.Job, fmt.Sprintf("%s.tar.zst", req.Task))
	if _, err := os.Stat(artifact); err == nil {
		log.Debug("Reading logs from finalized artifact", "job", req.Job, "task", req.Task)
		r, err := extractLogsFromArtifact(artifact, tailLines)
		if err != nil {
			log.Error("Failed to extract logs from artifact", "job", req.Job, "task", req.Task, "artifact", artifact, "error", err)
			return fmt.Errorf("failed to extract logs from artifact: %w", err)
		}
		reader = r
	} else {
		// Running task: try live log reader
		log.Debug("Attempting live log read", "job", req.Job, "task", req.Task)
		liveReader, liveErr := scheduler.ReadLiveTaskLogs(req.Job, req.Task, tailLines)
		if liveErr != nil {
			log.Warn("Logs not found", "job", req.Job, "task", req.Task, "liveError", liveErr)
			return status.Errorf(codes.NotFound, "logs not found for task %s/%s", req.Job, req.Task)
		}
		log.Debug("Live log reader obtained, starting read loop", "job", req.Job, "task", req.Task)
		reader = liveReader
	}
	defer func() {
		if err := reader.Close(); err != nil {
			log.Warn("Error closing log reader", "job", req.Job, "task", req.Task, "error", err)
		}
	}()

	totalBytes := 0
	chunk := make([]byte, 32*1024)
	for {
		n, err := reader.Read(chunk)
		if n > 0 {
			totalBytes += n
			if sendErr := srv.Send(&proto.StreamTaskLogsChunk{
				Data:   chunk[:n],
				Length: uint32(n),
			}); sendErr != nil {
				log.Error("Failed to send log chunk", "job", req.Job, "task", req.Task, "totalBytes", totalBytes, "error", sendErr)
				return fmt.Errorf("failed to send log chunk: %w", sendErr)
			}
		}
		if err == io.EOF {
			log.Debug("Log stream complete", "job", req.Job, "task", req.Task, "totalBytes", totalBytes)
			return nil
		}
		if err != nil {
			log.Error("Failed to read log chunk", "job", req.Job, "task", req.Task, "totalBytes", totalBytes, "error", err)
			return fmt.Errorf("failed to read log chunk: %w", err)
		}
	}
}

func extractLogsFromArtifact(artifactPath string, tailLines int) (io.ReadCloser, error) {
	tmpDir, err := os.MkdirTemp("", "alfred-logs-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}

	// Extract only .log files from the tar.zst archive
	extractCmd := exec.Command("tar",
		"--extract",
		"--use-compress-program", "zstd --decompress",
		"--file", artifactPath,
		"--directory", tmpDir,
		"--strip-components=2",
		"--wildcards", "*.log",
	)
	var extractStderr bytes.Buffer
	extractCmd.Stderr = &extractStderr
	if err := extractCmd.Run(); err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("failed to extract logs: %w: %s", err, extractStderr.String())
	}

	// Find and sort .log files
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("failed to read temp dir: %w", err)
	}

	var logFiles []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".log") {
			logFiles = append(logFiles, path.Join(tmpDir, entry.Name()))
		}
	}
	if len(logFiles) == 0 {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("no log files found in artifact")
	}
	sort.Strings(logFiles)

	// Tail the log files and collect output
	args := append([]string{"-n", fmt.Sprintf("%d", tailLines)}, logFiles...)
	tailCmd := exec.Command("tail", args...)
	output, err := tailCmd.Output()
	os.RemoveAll(tmpDir)
	if err != nil {
		return nil, fmt.Errorf("tail command failed: %w", err)
	}

	return io.NopCloser(bytes.NewReader(output)), nil
}
