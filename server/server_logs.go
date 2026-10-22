package main

import (
	"archive/tar"
	"bytes"
	"errors"
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

	tailLines := requestedTailLines(req)

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
	liveReader, liveErr := readLiveTaskLogs(req.Job, req.Task, tailLines)
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

const (
	// defaultTailLines applies when a request does not set TailLines: proto3
	// does not tell 0 from unset, so 0 cannot mean anything else.
	defaultTailLines = 100

	// allLines asks for whole logs, wherever a number of lines to tail is
	// expected (lastNLines, extractLogsFromArtifact, ReadLiveTaskLogs).
	allLines = -1
)

// requestedTailLines is the number of lines to tail per step log that req asks
// for: a positive count, or allLines for any negative TailLines (the client's
// `logs -n all`). A server older than allLines clamped negative values to the
// default, which is what an old server still does with a new client's "all".
func requestedTailLines(req *proto.StreamTaskLogsRequest) int {
	switch {
	case req.TailLines > 0:
		return int(req.TailLines)
	case req.TailLines < 0:
		return allLines
	default:
		return defaultTailLines
	}
}

// readLiveTaskLogs reads a running task's logs; tests replace it.
var readLiveTaskLogs = func(job, task string, lines int) (io.ReadCloser, error) {
	return scheduler.ReadLiveTaskLogs(job, task, lines)
}

// followPollInterval is how often follow mode looks for new output.
var followPollInterval = 10 * time.Second

// followWindowLines is how many lines per step log follow mode reads on each
// poll: enough to cover new output plus overlap with the last read. 10,000
// lines covers ~10s of output for even the most verbose tasks. It also caps
// the initial output: follow mode holds what it read last, so it never reads
// whole logs (`-f -n all` starts with these last 10,000 lines).
const followWindowLines = 10000

// followMaxBytes caps each read of follow mode, for logs of very long lines.
const followMaxBytes = 16 << 20

// followLines is how many lines per step log follow mode starts with when
// asked for tailLines: at most followWindowLines, which is also what allLines
// gets.
func followLines(tailLines int) int {
	if tailLines <= 0 || tailLines > followWindowLines {
		return followWindowLines
	}
	return tailLines
}

// followLiveLogs implements the follow (-f) mode for live task logs.
// It waits for logs to become available, sends the initial tail output,
// then polls every 10 seconds for new content using byte-offset delta
// detection (log output is append-only because steps run sequentially
// and log files are append-only).
func (s *server) followLiveLogs(srv proto.Alfred_StreamTaskLogsServer, job, task string, tailLines int) error {
	ticker := time.NewTicker(followPollInterval)
	defer ticker.Stop()

	// Wait for logs to become available (task may be queued or starting up).
	// The initial output is read whole before any of it is sent, so that a
	// read failing midway (the workspace torn down) still falls back to the
	// artifact below, with nothing sent twice.
	//
	// lastContent is the last known content, for delta detection. On each poll
	// we read the last followWindowLines lines. Since logs are append-only, new
	// content appears at the end. We find the overlap with the previous read
	// and send only the new bytes.
	var lastContent []byte
	for {
		content, err := readLiveLogs(job, task, followLines(tailLines))
		if err == nil {
			if err := sendLogData(srv, content); err != nil {
				log.Error("Failed to send initial follow output", "job", job, "task", task, "error", err)
				return fmt.Errorf("failed to send initial follow output: %w", err)
			}
			log.Debug("Follow mode started", "job", job, "task", task, "sentBytes", len(content))
			lastContent = content
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

	// Poll for new content
	for {
		select {
		case <-srv.Context().Done():
			log.Debug("Follow mode: client disconnected", "job", job, "task", task)
			return nil
		case <-ticker.C:
			content, err := readLiveLogs(job, task, followWindowLines)
			if err != nil {
				// Live reader gone — task has finished and workspace is being torn down.
				// Try to send any remaining content from the finalized artifact.
				log.Debug("Follow mode: live reader gone, checking artifact for final content", "job", job, "task", task)
				artifact := path.Join(dataRoot, "artifacts", job, fmt.Sprintf("%s.tar.zst", task))
				if _, statErr := os.Stat(artifact); statErr != nil {
					return nil
				}
				if err := sendArtifactDelta(srv, artifact, lastContent, job, task); err != nil {
					log.Error("Follow mode: failed to send final logs from artifact", "job", job, "task", task, "artifact", artifact, "error", err)
					return fmt.Errorf("failed to send final logs from artifact: %w", err)
				}
				return nil
			}
			delta := findNewContent(lastContent, content)
			if len(delta) > 0 {
				if err := sendLogData(srv, delta); err != nil {
					return fmt.Errorf("failed to send follow delta: %w", err)
				}
				lastContent = content
			}
		}
	}
}

// findNewContent compares two snapshots of tailed log output and returns only the
// bytes that are new in `current` relative to `previous` (see newContentOffset).
func findNewContent(previous, current []byte) []byte {
	// Reading from memory cannot fail.
	offset, _ := newContentOffset(previous, bytes.NewReader(current))
	if offset == int64(len(current)) {
		return nil
	}
	return current[offset:]
}

// newContentOffset returns the offset in the content of r from which it is
// new relative to previous, an earlier snapshot of the end of the same logs.
// Since logs are append-only, the end of previous should appear somewhere in
// the content, and everything after that overlap is new:
//   - if the content starts with previous (the tail window is large enough,
//     or nothing changed), what follows it;
//   - else the tail window shifted: what follows the first occurrence of the
//     last 3 lines of previous, or 2, or 1 (robust against partial line
//     matches). A last line without newline, still being written, counts as
//     a line, so that what is appended to it is all that is new;
//   - else, the gap between reads was too large: everything (0). This may
//     duplicate some output on the client, but loses none.
//
// It reads r once, holding about len(previous) bytes rather than all of it.
func newContentOffset(previous []byte, r io.Reader) (int64, error) {
	if len(previous) == 0 {
		return 0, nil
	}

	fingerprints := lastLines(previous, 3)
	ends := make([]int64, len(fingerprints)) // after each fingerprint's first occurrence
	keep := 0                                // of the bytes searched, for an occurrence spanning reads
	for i, fp := range fingerprints {
		ends[i] = -1
		keep = max(keep, len(fp)-1)
	}

	var read int64
	isPrefix := true // so far
	var window []byte
	var windowStart int64 // offset of window[0]
	chunk := make([]byte, logChunkSize)
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			data := chunk[:n]
			if isPrefix && read < int64(len(previous)) {
				m := min(int64(n), int64(len(previous))-read)
				isPrefix = bytes.Equal(data[:m], previous[read:read+m])
			}
			read += int64(n)
			if isPrefix && read >= int64(len(previous)) {
				return int64(len(previous)), nil
			}

			window = append(window, data...)
			for i, fp := range fingerprints {
				if ends[i] >= 0 {
					continue
				}
				// Only where an occurrence could end in data: earlier ones were searched.
				from := max(0, len(window)-n-len(fp)+1)
				if idx := bytes.Index(window[from:], fp); idx >= 0 {
					ends[i] = windowStart + int64(from+idx+len(fp))
				}
			}
			// No fingerprint at all when previous holds no line (just "\n").
			if !isPrefix && len(ends) > 0 && ends[0] >= 0 {
				return ends[0], nil
			}
			if drop := len(window) - keep; drop > 0 {
				window = window[:copy(window, window[drop:])]
				windowStart += int64(drop)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
	}

	for _, end := range ends {
		if end >= 0 {
			return end, nil
		}
	}
	return 0, nil
}

// lastLines returns the suffixes of b holding its last n lines, then n-1, ...
// down to 1 (fewer if b has fewer lines). A last line without newline counts.
func lastLines(b []byte, n int) [][]byte {
	end := len(b)
	if end > 0 && b[end-1] == '\n' {
		end-- // the newline ending the last line, not an empty line after it
	}
	var starts []int // of the last line, then the one before, ...
	for pos := end; len(starts) < n && pos > 0; {
		i := bytes.LastIndexByte(b[:pos], '\n')
		starts = append(starts, i+1)
		if i < 0 {
			break
		}
		pos = i
	}
	suffixes := make([][]byte, 0, len(starts))
	for k := len(starts) - 1; k >= 0; k-- {
		suffixes = append(suffixes, b[starts[k]:])
	}
	return suffixes
}

// sendArtifactDelta sends what the whole logs of a finalized artifact add to
// previous (see newContentOffset). It streams them twice rather than hold
// them: once to find where the new content starts, once to send it.
func sendArtifactDelta(srv proto.Alfred_StreamTaskLogsServer, artifact string, previous []byte, job, task string) error {
	r, err := extractLogsFromArtifact(artifact, allLines)
	if err != nil {
		return err
	}
	offset, err := newContentOffset(previous, r)
	r.Close()
	if err != nil {
		return err
	}

	r, err = extractLogsFromArtifact(artifact, allLines)
	if err != nil {
		return err
	}
	defer r.Close()
	if _, err := io.CopyN(io.Discard, r, offset); err != nil {
		return err
	}
	return streamReader(srv, r, job, task)
}

// readLiveLogs reads live log content for a task, tailing up to the given number
// of lines per step log, for follow mode: past followMaxBytes, only the last
// lines that fit are kept.
func readLiveLogs(job, task string, lines int) ([]byte, error) {
	reader, err := readLiveTaskLogs(job, task, lines)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return readLastBytes(reader, followMaxBytes)
}

// readLastBytes reads r whole and returns its last lines that fit in limit
// bytes (all of r if it does), holding at most about twice that.
func readLastBytes(r io.Reader, limit int) ([]byte, error) {
	var buf []byte
	trimmed := false
	chunk := make([]byte, logChunkSize)
	for {
		n, err := r.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if len(buf) > 2*limit {
			buf = buf[:copy(buf, buf[len(buf)-limit:])]
			trimmed = true
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if len(buf) > limit {
		buf = buf[len(buf)-limit:]
		trimmed = true
	}
	if trimmed {
		// Drop the line cut in its middle.
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		}
	}
	return buf, nil
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

// logChunkSize bounds each StreamTaskLogsChunk: one message for whole logs
// could exceed what the client accepts (config.MaxPacketSize).
const logChunkSize = 32 * 1024

// sendLogData sends data in chunks of at most logChunkSize.
func sendLogData(srv proto.Alfred_StreamTaskLogsServer, data []byte) error {
	for len(data) > 0 {
		n := min(len(data), logChunkSize)
		if err := srv.Send(&proto.StreamTaskLogsChunk{
			Data:   data[:n],
			Length: uint32(n),
		}); err != nil {
			return fmt.Errorf("failed to send log chunk: %w", err)
		}
		data = data[n:]
	}
	return nil
}

// streamReader sends the contents of reader as StreamTaskLogsChunks.
func streamReader(srv proto.Alfred_StreamTaskLogsServer, reader io.Reader, job, task string) error {
	totalBytes := 0
	chunk := make([]byte, logChunkSize)
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

// artifactLog is a .log file found in an artifact archive.
type artifactLog struct {
	path string // path in the archive
	name string // basename, e.g. "taskname-step-0.log": header and sort key
}

// errStopWalk ends walkArtifactLogs early, without error.
var errStopWalk = errors.New("stop walking the artifact")

// extractLogsFromArtifact reads .log files from a tar.zst artifact archive,
// applies tail -n to each file (none for allLines), and returns a reader with
// the combined output, matching the format of
// `tail -v -n <lines> file1.log file2.log ...`.
//
// The output is streamed, never held whole: a tail keeps the last lines of one
// file at a time, and allLines copies each file straight from the archive.
// Files are output in name order (step order); tar has no random access and
// lists them in whatever order they were archived, so this takes one pass over
// the archive per run of files found out of order (a single pass when sorted).
func extractLogsFromArtifact(artifactPath string, tailLines int) (io.ReadCloser, error) {
	var logs []artifactLog
	if err := walkArtifactLogs(artifactPath, func(header *tar.Header, _ io.Reader) error {
		logs = append(logs, artifactLog{path: header.Name, name: path.Base(header.Name)})
		return nil
	}); err != nil {
		return nil, err
	}
	if len(logs) == 0 {
		return nil, fmt.Errorf("no .log files found in artifact %q", artifactPath)
	}

	// Sort by filename to get step order (step-0.log, step-1.log, ...)
	sort.SliceStable(logs, func(i, j int) bool {
		return logs[i].name < logs[j].name
	})

	pr, pw := io.Pipe()
	go func() {
		// A reader closed early (client gone) fails the next write, ending this.
		pw.CloseWithError(writeArtifactLogs(pw, artifactPath, logs, tailLines))
	}()
	return pr, nil
}

// writeArtifactLogs writes logs, in order, from the archive to w.
func writeArtifactLogs(w io.Writer, artifactPath string, logs []artifactLog, tailLines int) error {
	next := 0
	for next < len(logs) {
		before := next
		err := walkArtifactLogs(artifactPath, func(header *tar.Header, r io.Reader) error {
			if header.Name != logs[next].path {
				return nil
			}
			if err := writeArtifactLog(w, next > 0, logs[next].name, r, tailLines); err != nil {
				return err
			}
			next++
			if next == len(logs) {
				return errStopWalk
			}
			return nil
		})
		if err != nil {
			return err
		}
		if next == before {
			return fmt.Errorf("log %q vanished from artifact %q", logs[next].path, artifactPath)
		}
	}
	return nil
}

// writeArtifactLog writes one file like `tail -v` does: a header line, the
// file's last tailLines lines (all of it for allLines), a final newline if the
// file lacks one, and a blank line before the header of any file but the first.
func writeArtifactLog(w io.Writer, separate bool, name string, r io.Reader, tailLines int) error {
	header := fmt.Sprintf("==> %s <==\n", name)
	if separate {
		header = "\n" + header
	}
	if _, err := io.WriteString(w, header); err != nil {
		return err
	}

	lw := &lastByteWriter{w: w}
	if tailLines > 0 {
		tail, err := readTail(r, tailLines)
		if err != nil {
			return fmt.Errorf("failed to read log %q: %w", name, err)
		}
		if _, err := lw.Write(tail); err != nil {
			return err
		}
	} else if _, err := io.Copy(lw, r); err != nil {
		return fmt.Errorf("failed to copy log %q: %w", name, err)
	}

	if lw.written && lw.last != '\n' {
		_, err := io.WriteString(w, "\n")
		return err
	}
	return nil
}

// tailTrimThreshold is how much readTail buffers before dropping the lines
// that cannot be among the last ones.
const tailTrimThreshold = 1 << 20

// readTail returns the last n lines of r, holding about tailTrimThreshold
// bytes (or twice the size of those lines, if larger) rather than all of r.
func readTail(r io.Reader, n int) ([]byte, error) {
	var buf []byte
	limit := tailTrimThreshold
	chunk := make([]byte, 32*1024)
	for {
		k, err := r.Read(chunk)
		buf = append(buf, chunk[:k]...)
		if len(buf) > limit {
			// The last n lines of what is read so far hold all of the last n lines
			// of r that it overlaps: those are all that is kept.
			buf = buf[:copy(buf, lastNLines(buf, n))]
			limit = max(tailTrimThreshold, 2*len(buf))
		}
		if err == io.EOF {
			return lastNLines(buf, n), nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// lastByteWriter remembers the last byte written through it.
type lastByteWriter struct {
	w       io.Writer
	last    byte
	written bool
}

func (lw *lastByteWriter) Write(p []byte) (int, error) {
	n, err := lw.w.Write(p)
	if n > 0 {
		lw.last = p[n-1]
		lw.written = true
	}
	return n, err
}

// walkArtifactLogs calls fn for each regular .log file of a tar.zst archive, in
// archive order, with a reader of its content. fn returning errStopWalk ends the
// walk without error.
func walkArtifactLogs(artifactPath string, fn func(header *tar.Header, r io.Reader) error) error {
	f, err := os.Open(artifactPath)
	if err != nil {
		return fmt.Errorf("failed to open artifact %q: %w", artifactPath, err)
	}
	defer f.Close()

	zr, err := zstd.NewReader(f)
	if err != nil {
		return fmt.Errorf("failed to create zstd reader for %q: %w", artifactPath, err)
	}
	defer zr.Close()

	tr := tar.NewReader(zr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("failed to read tar entry in %q: %w", artifactPath, err)
		}

		// Skip directories, links and non-.log files
		if header.Typeflag != tar.TypeReg || !strings.HasSuffix(header.Name, ".log") {
			continue
		}

		if err := fn(header, tr); err != nil {
			if err == errStopWalk {
				return nil
			}
			return err
		}
	}
}
