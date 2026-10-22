package main

import (
	"archive/tar"
	"bytes"
	"context"
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
	// Follow mode reads the window it compares against whole, so it takes a
	// number of lines: whole logs are for `logs -n all` without -f.
	if req.Follow && tailLines == allLines {
		return status.Errorf(codes.InvalidArgument, "whole logs (-n all) cannot be followed: use -n all without -f, or -f with -n N")
	}

	// Try finalized artifact first (completed task)
	artifact := path.Join(dataRoot, "artifacts", req.Job, fmt.Sprintf("%s.tar.zst", req.Task))
	if _, err := os.Stat(artifact); err == nil {
		log.Debug("Reading logs from finalized artifact", "job", req.Job, "task", req.Task)
		r, err := extractLogsFromArtifact(srv.Context(), artifact, tailLines)
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
			r, extractErr := extractLogsFromArtifact(srv.Context(), artifact, tailLines)
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
					// Whole logs: streamed (or spooled to disk), never buffered per file.
					if r, extractErr := extractLogsFromArtifact(srv.Context(), artifact, allLines); extractErr == nil {
						delta, readErr := finalFollowDelta(lastContent, r)
						r.Close()
						if readErr == nil {
							// In chunks: the delta may exceed what the client accepts in one message.
							_ = sendLogData(srv, delta)
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
	reader, err := readLiveTaskLogs(job, task, lines)
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
// Files are output in name order (step order). tar has no random access and
// lists them in whatever order they were archived, so this takes two passes
// over the archive, whatever that order: one to list the logs, one to output
// them, setting aside those archived before their turn (see writeArtifactLogs).
func extractLogsFromArtifact(ctx context.Context, artifactPath string, tailLines int) (io.ReadCloser, error) {
	var logs []artifactLog
	if err := walkArtifactLogs(ctx, artifactPath, func(header *tar.Header, _ io.Reader) error {
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

	// ctx done (the RPC cancelled, the client gone) or the reader closed stops
	// the walk at its next read from the archive, even while it sets aside
	// out-of-order logs and writes nothing to the pipe: a read waiting on the
	// pipe then gets the walk's error. Waiting for the next write would not do,
	// as the RPC only notices the client is gone when it next sends.
	ctx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	go func() {
		defer cancel()
		pw.CloseWithError(writeArtifactLogs(ctx, pw, artifactPath, logs, tailLines))
	}()
	return &cancelOnClose{PipeReader: pr, cancel: cancel}, nil
}

// cancelOnClose is a pipe reader whose Close also cancels the writing side.
type cancelOnClose struct {
	*io.PipeReader
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	c.cancel()
	return c.PipeReader.Close()
}

// writeArtifactLogs writes logs, in order, from the archive to w, in one pass
// over it. A log archived before its turn is set aside until the logs before it
// are written: its tail in memory, or all of it in a temporary file shared by
// every log set aside, so that one request holds one file open, however many.
func writeArtifactLogs(ctx context.Context, w io.Writer, artifactPath string, logs []artifactLog, tailLines int) error {
	turns := make(map[string][]int, len(logs)) // archive path → its indexes in logs, in order
	for i, l := range logs {
		turns[l.path] = append(turns[l.path], i)
	}
	setAside := make(map[int]*setAsideLog)
	var spool *os.File // created at the first whole log set aside
	defer func() {
		if spool != nil {
			spool.Close()
		}
	}()

	next := 0
	writeSetAside := func() error {
		for l, ok := setAside[next]; ok; l, ok = setAside[next] {
			delete(setAside, next)
			// Read back under ctx too: a whole log set aside makes no archive read.
			if err := writeArtifactLog(w, next > 0, logs[next].name, &contextReader{ctx: ctx, r: l.reader()}, tailLines); err != nil {
				return err
			}
			next++
		}
		return nil
	}

	err := walkArtifactLogs(ctx, artifactPath, func(header *tar.Header, r io.Reader) error {
		indexes := turns[header.Name]
		if len(indexes) == 0 {
			return nil
		}
		i := indexes[0]
		turns[header.Name] = indexes[1:]

		if i == next {
			if err := writeArtifactLog(w, next > 0, logs[next].name, r, tailLines); err != nil {
				return err
			}
			next++
			if err := writeSetAside(); err != nil {
				return err
			}
		} else {
			if tailLines <= 0 && spool == nil {
				f, err := createSpool()
				if err != nil {
					return fmt.Errorf("failed to set aside log %q: %w", logs[i].name, err)
				}
				spool = f
			}
			l, err := setAsideArtifactLog(r, tailLines, spool)
			if err != nil {
				return fmt.Errorf("failed to set aside log %q: %w", logs[i].name, err)
			}
			setAside[i] = l
		}
		if next == len(logs) {
			return errStopWalk
		}
		return nil
	})
	if err != nil {
		return err
	}
	if next < len(logs) {
		return fmt.Errorf("log %q vanished from artifact %q", logs[next].path, artifactPath)
	}
	return nil
}

// setAsideLog is a log read before its turn: its last lines, or, for whole
// logs, its section of the request's temporary file.
type setAsideLog struct {
	tail    []byte
	section *io.SectionReader
}

// setAsideArtifactLog reads r: its last tailLines lines, or, for whole logs,
// all of it, appended to spool.
func setAsideArtifactLog(r io.Reader, tailLines int, spool *os.File) (*setAsideLog, error) {
	if tailLines > 0 {
		tail, err := readTail(r, tailLines)
		if err != nil {
			return nil, err
		}
		return &setAsideLog{tail: tail}, nil
	}

	// Reads go through ReadAt, which leaves the offset of writes at the end.
	start, err := spool.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	n, err := io.Copy(spool, r)
	if err != nil {
		return nil, err
	}
	return &setAsideLog{section: io.NewSectionReader(spool, start, n)}, nil
}

func (l *setAsideLog) reader() io.Reader {
	if l.section != nil {
		return l.section
	}
	return bytes.NewReader(l.tail)
}

// createSpool creates the temporary file of whole logs set aside; tests count
// the calls.
var createSpool = func() (*os.File, error) {
	f, err := os.CreateTemp("", "alfred-logs-*")
	if err != nil {
		return nil, err
	}
	// Unlinked at once: the open file stays usable, and nothing is left behind
	// however the server stops.
	_ = os.Remove(f.Name())
	return f, nil
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
			// A copy: the tail may be held a while (set aside), and buf can be
			// much larger than it.
			return bytes.Clone(lastNLines(buf, n)), nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// followFinalMaxBytes bounds follow mode's final read from the artifact, of
// which only the end matters: what was written after the last poll.
const followFinalMaxBytes = 32 << 20

// followTruncatedNotice is sent when the output written after follow mode's
// last poll exceeds followFinalMaxBytes, in place of what is cut.
const followTruncatedNotice = "\n[alfred: more output was written after the last poll than follow mode sends at the end; the start of it is cut. `alfred logs JOB TASK -n all` prints whole logs.]\n"

// finalFollowDelta returns what the finalized logs read from r add to
// lastContent, the last poll's read. It keeps only the last
// followFinalMaxBytes of them: if what was written since that poll does not
// fit, the delta starts with followTruncatedNotice rather than silently lose
// the start.
func finalFollowDelta(lastContent []byte, r io.Reader) ([]byte, error) {
	finalContent, truncated, err := readLastBytes(r, followFinalMaxBytes)
	if err != nil {
		return nil, err
	}
	delta := findNewContent(lastContent, finalContent)
	// No overlap with the last poll in what is kept: the cut held unseen output.
	if truncated && len(lastContent) > 0 && len(delta) == len(finalContent) {
		return append([]byte(followTruncatedNotice), delta...), nil
	}
	return delta, nil
}

// readLastBytes returns the last limit bytes of r, and whether r had more,
// holding at most about twice that rather than all of r.
func readLastBytes(r io.Reader, limit int) ([]byte, bool, error) {
	var buf []byte
	truncated := false
	chunk := make([]byte, 32*1024)
	for {
		k, err := r.Read(chunk)
		buf = append(buf, chunk[:k]...)
		if len(buf) > 2*limit {
			buf = buf[:copy(buf, buf[len(buf)-limit:])]
			truncated = true
		}
		if err == io.EOF {
			if len(buf) > limit {
				buf = buf[len(buf)-limit:]
				truncated = true
			}
			return buf, truncated, nil
		}
		if err != nil {
			return nil, false, err
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

// openArtifact opens an artifact archive; tests watch the reads from it.
var openArtifact = func(name string) (io.ReadCloser, error) {
	return os.Open(name)
}

// contextReader fails its reads once ctx is done.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// walkArtifactLogs calls fn for each regular .log file of a tar.zst archive, in
// archive order, with a reader of its content. fn returning errStopWalk ends the
// walk without error. Once ctx is done, reading the archive fails, which ends
// the walk and whatever fn is reading.
func walkArtifactLogs(ctx context.Context, artifactPath string, fn func(header *tar.Header, r io.Reader) error) error {
	f, err := openArtifact(artifactPath)
	if err != nil {
		return fmt.Errorf("failed to open artifact %q: %w", artifactPath, err)
	}
	defer f.Close()

	zr, err := zstd.NewReader(&contextReader{ctx: ctx, r: f})
	if err != nil {
		return fmt.Errorf("failed to create zstd reader for %q: %w", artifactPath, err)
	}
	defer zr.Close()

	// Checked above and below the decoder: it may serve reads from what it has
	// already decompressed, without reading the archive.
	tr := tar.NewReader(&contextReader{ctx: ctx, r: zr})
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
