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
	"time"

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

	// Try finalized artifact first (completed task)
	artifact := path.Join(dataRoot, "artifacts", req.Job, fmt.Sprintf("%s.tar.zst", req.Task))
	if _, err := os.Stat(artifact); err == nil {
		log.Debug("Reading logs from finalized artifact", "job", req.Job, "task", req.Task)
		r, err := extractLogsFromArtifact(artifact, tailLines)
		if err != nil {
			log.Error("Failed to extract logs from artifact", "job", req.Job, "task", req.Task, "artifact", artifact, "error", err)
			return fmt.Errorf("failed to extract logs from artifact: %w", err)
		}
		defer r.Close()
		return streamReader(srv, r, req.Job, req.Task)
	}

	if req.Follow {
		return s.followLiveLogs(srv, req.Job, req.Task, tailLines)
	}

	// Non-follow mode: try live, return
	log.Debug("Attempting live log read", "job", req.Job, "task", req.Task)
	liveReader, liveErr := scheduler.ReadLiveTaskLogs(req.Job, req.Task, tailLines)
	if liveErr != nil {
		log.Warn("Logs not found", "job", req.Job, "task", req.Task, "liveError", liveErr)
		return status.Errorf(codes.NotFound, "no logs (yet) for task %s/%s", req.Job, req.Task)
	}
	log.Debug("Live log reader obtained, starting read loop", "job", req.Job, "task", req.Task)
	defer func() {
		if err := liveReader.Close(); err != nil {
			log.Warn("Error closing log reader", "job", req.Job, "task", req.Task, "error", err)
		}
	}()
	return streamReader(srv, liveReader, req.Job, req.Task)
}

// followLiveLogs implements the follow (-f) mode for live task logs.
// It sends the initial tail output, then polls every 2 seconds for new content
// using byte-offset delta detection (log output is append-only because steps
// run sequentially and log files are append-only).
func (s *server) followLiveLogs(srv proto.Alfred_StreamTaskLogsServer, job, task string, tailLines int) error {
	// Get ALL content to establish the current byte offset
	allContent, err := readAllLiveLogs(job, task)
	if err != nil {
		log.Warn("Logs not found for follow", "job", job, "task", task, "error", err)
		return status.Errorf(codes.NotFound, "no logs (yet) for task %s/%s", job, task)
	}

	// Send only the last tailLines lines as initial output
	tail := lastNLines(allContent, tailLines)
	if len(tail) > 0 {
		if sendErr := srv.Send(&proto.StreamTaskLogsChunk{
			Data:   tail,
			Length: uint32(len(tail)),
		}); sendErr != nil {
			log.Error("Failed to send initial follow chunk", "job", job, "task", task, "error", sendErr)
			return fmt.Errorf("failed to send initial follow chunk: %w", sendErr)
		}
	}
	knownBytes := len(allContent)
	log.Debug("Follow mode started", "job", job, "task", task, "initialBytes", knownBytes, "sentBytes", len(tail))

	// Poll for new content
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-srv.Context().Done():
			log.Debug("Follow mode: client disconnected", "job", job, "task", task)
			return nil
		case <-ticker.C:
			content, err := readAllLiveLogs(job, task)
			if err != nil {
				// Live reader gone — task has finished and workspace is being torn down.
				log.Debug("Follow mode: live reader gone, ending stream", "job", job, "task", task, "knownBytes", knownBytes)
				return nil
			}
			if len(content) > knownBytes {
				delta := content[knownBytes:]
				if sendErr := srv.Send(&proto.StreamTaskLogsChunk{
					Data:   delta,
					Length: uint32(len(delta)),
				}); sendErr != nil {
					log.Error("Failed to send follow delta", "job", job, "task", task, "error", sendErr)
					return fmt.Errorf("failed to send follow delta: %w", sendErr)
				}
				knownBytes = len(content)
			}
		}
	}
}

// readAllLiveLogs reads the complete live log content for a task.
func readAllLiveLogs(job, task string) ([]byte, error) {
	reader, err := scheduler.ReadLiveTaskLogs(job, task, 1000000)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

// lastNLines returns the last n lines from data. If data has fewer than n lines,
// all of data is returned.
func lastNLines(data []byte, n int) []byte {
	if n <= 0 || len(data) == 0 {
		return data
	}

	// Walk backwards, counting newlines
	count := 0
	pos := len(data)
	// If data ends with a newline, skip it (don't count trailing newline as a line)
	if pos > 0 && data[pos-1] == '\n' {
		pos--
	}
	for pos > 0 {
		pos--
		if data[pos] == '\n' {
			count++
			if count == n {
				return data[pos+1:]
			}
		}
	}
	return data
}

// streamReader sends the contents of reader as StreamTaskLogsChunks.
func streamReader(srv proto.Alfred_StreamTaskLogsServer, reader io.Reader, job, task string) error {
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
				log.Error("Failed to send log chunk", "job", job, "task", task, "totalBytes", totalBytes, "error", sendErr)
				return fmt.Errorf("failed to send log chunk: %w", sendErr)
			}
		}
		if err == io.EOF {
			log.Debug("Log stream complete", "job", job, "task", task, "totalBytes", totalBytes)
			return nil
		}
		if err != nil {
			log.Error("Failed to read log chunk", "job", job, "task", task, "totalBytes", totalBytes, "error", err)
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
	args := append([]string{"-v", "-n", fmt.Sprintf("%d", tailLines)}, logFiles...)
	tailCmd := exec.Command("tail", args...)
	output, err := tailCmd.Output()
	os.RemoveAll(tmpDir)
	if err != nil {
		return nil, fmt.Errorf("tail command failed: %w", err)
	}

	return io.NopCloser(bytes.NewReader(output)), nil
}
