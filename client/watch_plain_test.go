package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/gammadia/alfred/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// jobWithTasks returns a job scheduled a minute before now, its tasks named task-1, task-2, …
func jobWithTasks(now time.Time, statuses ...proto.TaskStatus_Status) *proto.JobStatus {
	msg := &proto.JobStatus{ScheduledAt: timestamppb.New(now.Add(-time.Minute))}
	for i, s := range statuses {
		task := &proto.TaskStatus{Name: fmt.Sprintf("task-%d", i+1), Status: s}
		if s == proto.TaskStatus_FAILED {
			task.ExitCode = exitCode(1)
		}
		msg.Tasks = append(msg.Tasks, task)
	}
	return msg
}

func repeatStatus(s proto.TaskStatus_Status, n int) []proto.TaskStatus_Status {
	statuses := make([]proto.TaskStatus_Status, n)
	for i := range statuses {
		statuses[i] = s
	}
	return statuses
}

func TestWatchPlain_OneLinePerStatusChangeThenSummary(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	completed := jobWithTasks(now, proto.TaskStatus_COMPLETED, proto.TaskStatus_FAILED, proto.TaskStatus_COMPLETED)
	completed.CompletedAt = timestamppb.New(now)

	msgCh := make(chan recvResult, 6)
	msgCh <- recvResult{msg: jobWithTasks(now, proto.TaskStatus_QUEUED, proto.TaskStatus_QUEUED, proto.TaskStatus_QUEUED)}
	msgCh <- recvResult{msg: jobWithTasks(now, proto.TaskStatus_RUNNING, proto.TaskStatus_RUNNING, proto.TaskStatus_QUEUED)}
	msgCh <- recvResult{msg: jobWithTasks(now, proto.TaskStatus_RUNNING, proto.TaskStatus_RUNNING, proto.TaskStatus_QUEUED)}
	msgCh <- recvResult{msg: jobWithTasks(now, proto.TaskStatus_COMPLETED, proto.TaskStatus_FAILED, proto.TaskStatus_RUNNING)}
	msgCh <- recvResult{msg: completed}
	msgCh <- recvResult{err: io.EOF}

	var buf bytes.Buffer
	var seen int
	err := runWatchPlain(context.Background(), msgCh, newTestRenderer(now), &buf, func(*proto.JobStatus) { seen++ })

	require.NoError(t, err)
	assert.Equal(t, 5, seen, "onMessage runs for every message (abort-on-* logic)")
	assert.NotContains(t, buf.String(), "\033", "no escape codes off a terminal")
	assert.Equal(t, strings.Join([]string{
		"Job 'test-job' running (0/3 0%, 1m0s) | 3 queued",
		"Job 'test-job' running (0/3 0%, 1m0s) | 1 queued, 2 running | running: task-1 task-2",
		// the identical third message writes nothing
		"Job 'test-job' running (2/3 66%, 1m0s) | 1 running, 1 failed, 1 completed | running: task-3 | failed: task-2 | completed: task-1",
		// the completed state is left to the summary
		"Job 'test-job' completed (3/3 100%, 1m0s) | 1 failed, 2 completed",
		"  failed: task-2",
		"  completed: task-1 task-3",
		"",
	}, "\n"), buf.String())
}

func TestWatchPlain_TruncatesTaskNamesUnlessVerbose(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	run := func(verbose bool) string {
		completed := jobWithTasks(now, repeatStatus(proto.TaskStatus_ABORTED, 25)...)
		completed.CompletedAt = timestamppb.New(now)
		msgCh := make(chan recvResult, 4)
		msgCh <- recvResult{msg: jobWithTasks(now, repeatStatus(proto.TaskStatus_QUEUED, 25)...)}
		msgCh <- recvResult{msg: jobWithTasks(now, repeatStatus(proto.TaskStatus_RUNNING, 25)...)}
		msgCh <- recvResult{msg: completed}
		msgCh <- recvResult{err: io.EOF}

		renderer := newTestRenderer(now)
		renderer.verbose = verbose
		var buf bytes.Buffer
		require.NoError(t, runWatchPlain(context.Background(), msgCh, renderer, &buf, nil))
		return buf.String()
	}

	lines := strings.Split(run(false), "\n")
	assert.True(t, strings.HasSuffix(lines[1], "running: task-1 task-2 task-3 task-4 task-5 task-6 task-7 task-8 task-9 task-10 task-11 task-12 task-13 task-14 task-15 task-16 task-17 task-18 task-19 task-20 … (5 more)"), lines[1])
	assert.Equal(t, "  aborted: task-1 task-2 task-3 task-4 task-5 task-6 task-7 task-8 task-9 task-10 task-11 task-12 task-13 task-14 task-15 task-16 task-17 task-18 task-19 task-20 … (5 more)", lines[3])

	verbose := run(true)
	assert.Equal(t, 4, strings.Count(verbose, "\n"), "with -v, still one line per change: -v is not a one-time dump off a terminal")
	assert.NotContains(t, verbose, "more)")
	assert.Equal(t, 2, strings.Count(verbose, " task-25"), "every name, on the change line and in the summary")
}

func TestWatchPlain_EOFBeforeCompletionIsAnError(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	msgCh := make(chan recvResult, 2)
	msgCh <- recvResult{msg: jobWithTasks(now, proto.TaskStatus_RUNNING)}
	msgCh <- recvResult{err: io.EOF} // the server shutting down also ends the stream cleanly

	var buf bytes.Buffer
	err := runWatchPlain(context.Background(), msgCh, newTestRenderer(now), &buf, nil)

	assert.EqualError(t, err, "stream ended before job 'test-job' completed")
	assert.Contains(t, buf.String(), "Job 'test-job' running (0/1 0%, 1m0s) | 1 running\n  running: task-1\n", "the last known status, as a summary")
}

func TestWatchPlain_InterruptionIsAnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var buf bytes.Buffer
	err := runWatchPlain(ctx, make(chan recvResult), newTestRenderer(time.Now()), &buf, nil)

	assert.EqualError(t, err, "interrupted before job 'test-job' completed")
}

func TestPrintWatchOnce(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, plain := range []bool{true, false} {
		// Only one message and no EOF: --once must not wait for the stream to end.
		msgCh := make(chan recvResult, 1)
		msgCh <- recvResult{msg: jobWithTasks(now, proto.TaskStatus_RUNNING, proto.TaskStatus_QUEUED)}

		var buf bytes.Buffer
		err := printWatchOnce(context.Background(), msgCh, newTestRenderer(now), &buf, plain)

		require.NoError(t, err, "a snapshot of a running job is a success")
		if plain {
			assert.Equal(t, "Job 'test-job' running (0/2 0%, 1m0s) | 1 queued, 1 running\n  queued: task-2\n  running: task-1\n", buf.String())
		} else {
			assert.Contains(t, buf.String(), "Job 'test-job' running (")
			assert.Contains(t, buf.String(), "task-1")
		}
	}
}

func TestPrintWatchOnce_StreamErrorIsReturned(t *testing.T) {
	msgCh := make(chan recvResult, 1)
	msgCh <- recvResult{err: fmt.Errorf("job 'x' not found")}

	var buf bytes.Buffer
	err := printWatchOnce(context.Background(), msgCh, newTestRenderer(time.Now()), &buf, true)

	assert.EqualError(t, err, "failed to receive job status: job 'x' not found")
	assert.Empty(t, buf.String())
}

func TestWatchLoop_EOFBeforeCompletionIsAnError(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	msgCh := make(chan recvResult, 2)
	msgCh <- recvResult{msg: jobWithTasks(now, proto.TaskStatus_RUNNING)}
	msgCh <- recvResult{err: io.EOF}

	var buf bytes.Buffer
	err := runWatchLoop(context.Background(), msgCh, newTestRenderer(now), "test-job", &buf, nil, nil, nil, nil)

	assert.EqualError(t, err, "stream ended before job 'test-job' completed")
	assert.Contains(t, buf.String(), "Job 'test-job' running (", "the summary does not claim the job completed")
	assert.NotContains(t, buf.String(), "completed (")
}

func TestWatchLoop_VerboseTruncatesFramesButNotTheSummary(t *testing.T) {
	// A frame taller than the terminal cannot be redrawn in place, so -v cannot untruncate it;
	// the summary is written once, so it can.
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	completed := jobWithTasks(now, repeatStatus(proto.TaskStatus_COMPLETED, 30)...)
	completed.CompletedAt = timestamppb.New(now)

	msgCh := make(chan recvResult, 3)
	msgCh <- recvResult{msg: jobWithTasks(now, repeatStatus(proto.TaskStatus_RUNNING, 30)...)}
	msgCh <- recvResult{msg: completed}
	msgCh <- recvResult{err: io.EOF}

	renderer := newTestRenderer(now)
	renderer.verbose = true
	var rec writeRecorder
	err := runWatchLoop(context.Background(), msgCh, renderer, "test-job", &rec, nil, nil, nil, nil)
	require.NoError(t, err)

	var frames []string
	for _, w := range rec.writes {
		if strings.HasPrefix(w, "\033[?2026h") {
			frames = append(frames, w)
		}
	}
	require.Len(t, frames, 2, "-v keeps the live display")
	assert.Contains(t, frames[0], "task-20 …", "frames stay truncated")
	assert.NotContains(t, frames[0], "task-30")

	summary := rec.writes[len(rec.writes)-2] // the last write restores the cursor
	assert.Contains(t, summary, "Job 'test-job' completed (")
	assert.Contains(t, summary, "task-1 task-2")
	assert.Contains(t, summary, "task-30")
	assert.NotContains(t, summary, "…")
}

// failingWriter fails every write, like stdout redirected to /dev/full.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errNoSpace }

var errNoSpace = fmt.Errorf("no space left on device")

func TestWatch_WriteErrorsAreReturned(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	completed := jobWithTasks(now, proto.TaskStatus_COMPLETED)
	completed.CompletedAt = timestamppb.New(now)
	stream := func(msgs ...*proto.JobStatus) <-chan recvResult {
		msgCh := make(chan recvResult, len(msgs)+1)
		for _, msg := range msgs {
			msgCh <- recvResult{msg: msg}
		}
		msgCh <- recvResult{err: io.EOF}
		return msgCh
	}

	for _, plain := range []bool{true, false} {
		err := printWatchOnce(context.Background(), stream(completed), newTestRenderer(now), failingWriter{}, plain)
		assert.ErrorIs(t, err, errNoSpace, "--once (plain: %v) must not report a snapshot it could not write", plain)
	}

	err := runWatchPlain(context.Background(), stream(jobWithTasks(now, proto.TaskStatus_RUNNING), completed), newTestRenderer(now), failingWriter{}, nil)
	assert.ErrorIs(t, err, errNoSpace, "a change line")

	err = runWatchPlain(context.Background(), stream(completed), newTestRenderer(now), failingWriter{}, nil)
	assert.ErrorIs(t, err, errNoSpace, "the final summary of a completed job, off a terminal")

	err = runWatchLoop(context.Background(), stream(completed), newTestRenderer(now), "test-job", failingWriter{}, nil, nil, nil, nil)
	assert.ErrorIs(t, err, errNoSpace, "the final summary of a completed job, on a terminal")
}
