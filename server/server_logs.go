package main

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/gammadia/alfred/proto"
	"github.com/gammadia/alfred/server/log"
	"github.com/klauspost/compress/zstd"
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
// It waits for logs to become available, sends the initial tail output,
// then polls every 10 seconds for new content using byte-offset delta
// detection (log output is append-only because steps run sequentially
// and log files are append-only).
func (s *server) followLiveLogs(srv proto.Alfred_StreamTaskLogsServer, job, task string, tailLines int) error {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	// Wait for logs to become available (task may be queued or starting up)
	var allContent []byte
	for {
		content, err := readLiveLogs(job, task, tailLines)
		if err == nil {
			allContent = content
			break
		}

		// Also check if the task already completed while we were waiting
		artifact := path.Join(dataRoot, "artifacts", job, fmt.Sprintf("%s.tar.zst", task))
		if _, statErr := os.Stat(artifact); statErr == nil {
			log.Debug("Follow mode: task completed while waiting for logs", "job", job, "task", task)
			r, extractErr := extractLogsFromArtifact(artifact, tailLines)
			if extractErr != nil {
				return fmt.Errorf("failed to extract logs from artifact: %w", extractErr)
			}
			defer r.Close()
			return streamReader(srv, r, job, task)
		}

		log.Debug("Follow mode: waiting for logs", "job", job, "task", task)
		select {
		case <-srv.Context().Done():
			log.Debug("Follow mode: client disconnected while waiting", "job", job, "task", task)
			return nil
		case <-ticker.C:
		}
	}

	// Send only the last tailLines lines as initial output
	if len(allContent) > 0 {
		if sendErr := srv.Send(&proto.StreamTaskLogsChunk{
			Data:   allContent,
			Length: uint32(len(allContent)),
		}); sendErr != nil {
			log.Error("Failed to send initial follow chunk", "job", job, "task", task, "error", sendErr)
			return fmt.Errorf("failed to send initial follow chunk: %w", sendErr)
		}
	}
	log.Debug("Follow mode started", "job", job, "task", task, "sentBytes", len(allContent))

	// Track last known content for delta detection.
	// On each poll we read the last N lines. Since logs are append-only, new content
	// appears at the end. We find the overlap with the previous read and send only
	// the new bytes.
	lastContent := allContent

	// Poll for new content
	for {
		select {
		case <-srv.Context().Done():
			log.Debug("Follow mode: client disconnected", "job", job, "task", task)
			return nil
		case <-ticker.C:
			// Read a generous window: enough to cover new output plus overlap with last read.
			// 10,000 lines covers ~10s of output for even the most verbose tasks.
			content, err := readLiveLogs(job, task, 10000)
			if err != nil {
				// Live reader gone — task has finished and workspace is being torn down.
				// Try to send any remaining content from the finalized artifact.
				log.Debug("Follow mode: live reader gone, checking artifact for final content", "job", job, "task", task)
				artifact := path.Join(dataRoot, "artifacts", job, fmt.Sprintf("%s.tar.zst", task))
				if _, statErr := os.Stat(artifact); statErr == nil {
					if r, extractErr := extractLogsFromArtifact(artifact, 1000000); extractErr == nil {
						finalContent, _ := io.ReadAll(r)
						r.Close()
						delta := findNewContent(lastContent, finalContent)
						if len(delta) > 0 {
							_ = srv.Send(&proto.StreamTaskLogsChunk{
								Data:   delta,
								Length: uint32(len(delta)),
							})
						}
					}
				}
				return nil
			}
			delta := findNewContent(lastContent, content)
			if len(delta) > 0 {
				if sendErr := srv.Send(&proto.StreamTaskLogsChunk{
					Data:   delta,
					Length: uint32(len(delta)),
				}); sendErr != nil {
					log.Error("Failed to send follow delta", "job", job, "task", task, "error", sendErr)
					return fmt.Errorf("failed to send follow delta: %w", sendErr)
				}
				lastContent = content
			}
		}
	}
}

// findNewContent compares two snapshots of tailed log output and returns only the
// bytes that are new in `current` relative to `previous`. Since logs are append-only,
// the end of `previous` should appear somewhere in `current`, and everything after
// that overlap is new content.
func findNewContent(previous, current []byte) []byte {
	if len(previous) == 0 {
		return current
	}
	if bytes.Equal(previous, current) {
		return nil
	}

	// If current contains all of previous (same prefix), the delta is the suffix.
	// This is the common case when the tail window is large enough.
	if len(current) >= len(previous) && bytes.Equal(current[:len(previous)], previous) {
		return current[len(previous):]
	}

	// The tail window shifted: previous content is no longer a prefix of current.
	// Find where the end of previous appears in current by searching for progressively
	// shorter suffixes of previous. Start with the last few lines (robust against
	// partial line matches).
	prevStr := string(previous)
	lastNewline := strings.LastIndex(prevStr, "\n")
	if lastNewline > 0 {
		// Try last 3 lines as fingerprint for reliable matching
		lines := strings.Split(prevStr[:lastNewline], "\n")
		for take := min(len(lines), 3); take >= 1; take-- {
			fingerprint := strings.Join(lines[len(lines)-take:], "\n") + "\n"
			idx := bytes.Index(current, []byte(fingerprint))
			if idx >= 0 {
				return current[idx+len(fingerprint):]
			}
		}
	}

	// No overlap found — the gap between polls was too large. Send everything as new
	// content (may cause some duplication on the client, but no data loss).
	return current
}

// readLiveLogs reads live log content for a task, tailing up to the given number of lines.
func readLiveLogs(job, task string, lines int) ([]byte, error) {
	reader, err := scheduler.ReadLiveTaskLogs(job, task, lines)
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

// extractLogsFromArtifact reads .log files from a tar.zst artifact archive,
// applies tail -n to each file, and returns a reader with the combined output
// (matching the format of `tail -v -n <lines> file1.log file2.log ...`).
func extractLogsFromArtifact(artifactPath string, tailLines int) (io.ReadCloser, error) {
	f, err := os.Open(artifactPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open artifact %q: %w", artifactPath, err)
	}

	zr, err := zstd.NewReader(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("failed to create zstd reader for %q: %w", artifactPath, err)
	}

	tr := tar.NewReader(zr)

	// Collect all .log entries from the archive. We buffer them in memory
	// because tar entries must be read sequentially (no random access).
	type logEntry struct {
		name    string // basename, e.g. "taskname-step-0.log"
		content []byte
	}
	var logEntries []logEntry

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			zr.Close()
			f.Close()
			return nil, fmt.Errorf("failed to read tar entry in %q: %w", artifactPath, err)
		}

		// Skip directories and non-.log files
		if header.Typeflag == tar.TypeDir {
			continue
		}
		basename := path.Base(header.Name)
		if !strings.HasSuffix(basename, ".log") {
			continue
		}

		// Read the log content; cap at 10MB per file as a safety limit
		const maxLogSize = 10 * 1024 * 1024
		if header.Size > maxLogSize {
			log.Warn("Log file exceeds 10MB, truncating", "file", header.Name, "size", header.Size, "artifact", artifactPath)
		}
		limitedReader := io.LimitReader(tr, maxLogSize)
		content, err := io.ReadAll(limitedReader)
		if err != nil {
			zr.Close()
			f.Close()
			return nil, fmt.Errorf("failed to read log entry %q from %q: %w", header.Name, artifactPath, err)
		}

		logEntries = append(logEntries, logEntry{name: basename, content: content})
	}

	zr.Close()
	f.Close()

	if len(logEntries) == 0 {
		return nil, fmt.Errorf("no .log files found in artifact %q", artifactPath)
	}

	// Sort by filename to get step order (step-0.log, step-1.log, ...)
	sort.Slice(logEntries, func(i, j int) bool {
		return logEntries[i].name < logEntries[j].name
	})

	// Format output like `tail -v -n <lines>`: each file gets a header line,
	// followed by its last N lines.
	var buf bytes.Buffer
	for i, entry := range logEntries {
		if i > 0 {
			buf.WriteByte('\n')
		}
		fmt.Fprintf(&buf, "==> %s <==\n", entry.name)
		tail := lastNLines(entry.content, tailLines)
		buf.Write(tail)
		// Ensure each file ends with a newline
		if len(tail) > 0 && tail[len(tail)-1] != '\n' {
			buf.WriteByte('\n')
		}
	}

	return io.NopCloser(bytes.NewReader(buf.Bytes())), nil
}
