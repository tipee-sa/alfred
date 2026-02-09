package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"

	"github.com/gammadia/alfred/proto"
	"github.com/gammadia/alfred/server/config"
	"github.com/gammadia/alfred/server/log"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *server) StreamTaskLogs(req *proto.StreamTaskLogsRequest, srv proto.Alfred_StreamTaskLogsServer) error {
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
		reader = liveReader
	}
	defer func() {
		if err := reader.Close(); err != nil {
			log.Warn("Error closing log reader", "job", req.Job, "task", req.Task, "error", err)
		}
	}()

	chunk := make([]byte, config.MaxPacketSize-1024*1024)
	for {
		n, err := io.ReadFull(reader, chunk)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("failed to read log chunk: %w", err)
		}

		if err = srv.Send(&proto.StreamTaskLogsChunk{
			Data:   chunk[:n],
			Length: uint32(n),
		}); err != nil {
			return fmt.Errorf("failed to send log chunk: %w", err)
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

	// Tail the log files
	args := append([]string{"-n", fmt.Sprintf("%d", tailLines)}, logFiles...)
	tailCmd := exec.Command("tail", args...)
	var tailStderr bytes.Buffer
	tailCmd.Stderr = &tailStderr
	stdout, err := tailCmd.StdoutPipe()
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, err
	}
	if err = tailCmd.Start(); err != nil {
		os.RemoveAll(tmpDir)
		return nil, err
	}

	return &artifactLogReader{Reader: stdout, cmd: tailCmd, stderr: &tailStderr, tmpDir: tmpDir}, nil
}

// artifactLogReader wraps a tail command's stdout pipe. Close() waits for the
// command to finish and cleans up the temp directory.
type artifactLogReader struct {
	io.Reader
	cmd    *exec.Cmd
	stderr *bytes.Buffer
	tmpDir string
}

func (r *artifactLogReader) Close() error {
	err := r.cmd.Wait()
	os.RemoveAll(r.tmpDir)
	if err != nil {
		return fmt.Errorf("tail command failed: %w: %s", err, r.stderr.String())
	}
	return nil
}
