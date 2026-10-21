package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/gammadia/alfred/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	cursorHide = "\033[?25l"
	cursorShow = "\033[?25h"
)

func newTestRenderer(now time.Time) *watchRenderer {
	return &watchRenderer{
		jobName:   "test-job",
		verbose:   false,
		termWidth: func() int { return 300 },
		now:       func() time.Time { return now },
	}
}

func completedJobStatus(now time.Time) *proto.JobStatus {
	return &proto.JobStatus{
		ScheduledAt: timestamppb.New(now.Add(-5 * time.Minute)),
		CompletedAt: timestamppb.New(now),
		Tasks: []*proto.TaskStatus{
			{Name: "task-1", Status: proto.TaskStatus_COMPLETED, StartedAt: timestamppb.New(now.Add(-3 * time.Minute))},
		},
	}
}

// --- EOF tests ---

func TestWatchLoop_EOF_AfterStart_RestoresCursor(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	msgCh := make(chan recvResult, 2)
	msgCh <- recvResult{msg: completedJobStatus(now)}
	msgCh <- recvResult{err: io.EOF}

	var buf bytes.Buffer
	err := runWatchLoop(context.Background(), msgCh, newTestRenderer(now), "test-job", &buf, nil, nil, nil, nil)

	assert.NoError(t, err)
	output := buf.String()
	assert.Contains(t, output, cursorHide, "cursor should be hidden when rendering starts")
	assert.Contains(t, output, cursorShow, "cursor should be restored on exit")
	assert.Contains(t, output, "completed")
}

func TestWatchLoop_EOF_AfterStart_CallsOnFirstMessage(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	msgCh := make(chan recvResult, 2)
	msgCh <- recvResult{msg: completedJobStatus(now)}
	msgCh <- recvResult{err: io.EOF}

	var buf bytes.Buffer
	firstMessageCalled := false
	err := runWatchLoop(context.Background(), msgCh, newTestRenderer(now), "test-job", &buf,
		func() { firstMessageCalled = true }, nil, nil, nil)

	assert.NoError(t, err)
	assert.True(t, firstMessageCalled, "onFirstMessage should be called")
}

// --- Context cancellation tests ---

func TestWatchLoop_ContextCancelled_BeforeStart_IsAnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	msgCh := make(chan recvResult, 1)
	msgCh <- recvResult{err: errors.New("context canceled")}

	var buf bytes.Buffer
	cleanStopCalled := false
	err := runWatchLoop(ctx, msgCh, newTestRenderer(time.Now()), "test-job", &buf,
		nil, func() { cleanStopCalled = true }, nil, nil)

	assert.EqualError(t, err, "interrupted before job 'test-job' completed", "no final status to check: not a success")
	assert.True(t, cleanStopCalled, "onCleanStop should be called")
	assert.NotContains(t, buf.String(), "Interrupted", "should not print Interrupted message")
}

func TestWatchLoop_ContextCancelled_BeforeStart_DoesNotCallOnFailStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	msgCh := make(chan recvResult, 1)
	msgCh <- recvResult{err: errors.New("context canceled")}

	var buf bytes.Buffer
	failStopCalled := false
	err := runWatchLoop(ctx, msgCh, newTestRenderer(time.Now()), "test-job", &buf,
		nil, nil, func() { failStopCalled = true }, nil)

	assert.Error(t, err)
	assert.False(t, failStopCalled, "onFailStop should NOT be called on interrupt")
}

func TestWatchLoop_ContextCancelled_AfterStart_RestoresCursor(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())

	msgCh := make(chan recvResult, 2)
	msgCh <- recvResult{msg: completedJobStatus(now)}
	cancel()
	msgCh <- recvResult{err: errors.New("context canceled")}

	var buf bytes.Buffer
	err := runWatchLoop(ctx, msgCh, newTestRenderer(now), "test-job", &buf, nil, nil, nil, nil)

	assert.EqualError(t, err, "interrupted before job 'test-job' completed")
	output := buf.String()
	assert.Contains(t, output, cursorHide, "cursor should have been hidden")
	assert.Contains(t, output, cursorShow, "cursor should be restored")
	assert.NotContains(t, output, "Interrupted", "should not print Interrupted message")
}

func TestWatchLoop_ContextCancelled_AfterStart_CursorShowAfterHide(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())

	msgCh := make(chan recvResult, 2)
	msgCh <- recvResult{msg: completedJobStatus(now)}
	cancel()
	msgCh <- recvResult{err: errors.New("context canceled")}

	var buf bytes.Buffer
	err := runWatchLoop(ctx, msgCh, newTestRenderer(now), "test-job", &buf, nil, nil, nil, nil)

	assert.Error(t, err)
	output := buf.String()
	lastHide := strings.LastIndex(output, cursorHide)
	lastShow := strings.LastIndex(output, cursorShow)
	assert.Greater(t, lastShow, lastHide, "cursor show must appear after cursor hide")
}

// --- Error tests ---

func TestWatchLoop_Error_BeforeStart_ReturnsError(t *testing.T) {
	streamErr := errors.New("connection lost")

	msgCh := make(chan recvResult, 1)
	msgCh <- recvResult{err: streamErr}

	var buf bytes.Buffer
	failStopCalled := false
	err := runWatchLoop(context.Background(), msgCh, newTestRenderer(time.Now()), "test-job", &buf,
		nil, nil, func() { failStopCalled = true }, nil)

	assert.ErrorIs(t, err, streamErr)
	assert.True(t, failStopCalled, "onFailStop should be called on error before start")
}

func TestWatchLoop_Error_AfterStart_RestoresCursor(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	streamErr := errors.New("connection lost")

	msgCh := make(chan recvResult, 2)
	msgCh <- recvResult{msg: completedJobStatus(now)}
	msgCh <- recvResult{err: streamErr}

	var buf bytes.Buffer
	err := runWatchLoop(context.Background(), msgCh, newTestRenderer(now), "test-job", &buf, nil, nil, nil, nil)

	assert.ErrorIs(t, err, streamErr)
	output := buf.String()
	assert.Contains(t, output, cursorShow, "cursor should be restored on error")
	assert.Contains(t, output, "Waiting for job data")
}

func TestWatchLoop_Error_AfterStart_DoesNotCallOnFailStop(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	streamErr := errors.New("connection lost")

	msgCh := make(chan recvResult, 2)
	msgCh <- recvResult{msg: completedJobStatus(now)}
	msgCh <- recvResult{err: streamErr}

	var buf bytes.Buffer
	failStopCalled := false
	err := runWatchLoop(context.Background(), msgCh, newTestRenderer(now), "test-job", &buf,
		nil, nil, func() { failStopCalled = true }, nil)

	assert.ErrorIs(t, err, streamErr)
	assert.False(t, failStopCalled, "onFailStop should NOT be called after start")
}

// --- Cursor safety: the defer always writes cursor-show ---

func TestWatchLoop_DeferAlwaysRestoresCursor(t *testing.T) {
	// Even if the function exits through the EOF path (which also writes cursor-show),
	// the defer still writes it. This means cursor-show always appears in the output,
	// providing defense-in-depth for cursor restoration.
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	msgCh := make(chan recvResult, 2)
	msgCh <- recvResult{msg: completedJobStatus(now)}
	msgCh <- recvResult{err: io.EOF}

	var buf bytes.Buffer
	_ = runWatchLoop(context.Background(), msgCh, newTestRenderer(now), "test-job", &buf, nil, nil, nil, nil)

	output := buf.String()
	// cursor-show should appear at least twice: once explicit, once from defer
	assert.GreaterOrEqual(t, strings.Count(output, cursorShow), 2,
		"cursor-show should appear from both explicit write and defer")
}

// --- Flicker: each frame is one write that overwrites the previous one in place ---

// writeRecorder keeps every Write call separately, to check how output is split.
type writeRecorder struct{ writes []string }

func (r *writeRecorder) Write(p []byte) (int, error) {
	r.writes = append(r.writes, string(p))
	return len(p), nil
}

func TestWatchLoop_FramesOverwriteInPlaceInOneWrite(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	tall := &proto.JobStatus{
		ScheduledAt: timestamppb.New(now.Add(-5 * time.Minute)),
		Tasks: []*proto.TaskStatus{
			{Name: "task-1", Status: proto.TaskStatus_QUEUED},
			{Name: "task-2", Status: proto.TaskStatus_RUNNING, StartedAt: timestamppb.New(now)},
		},
	}
	short := &proto.JobStatus{
		ScheduledAt: timestamppb.New(now.Add(-5 * time.Minute)),
		Tasks: []*proto.TaskStatus{
			{Name: "task-1", Status: proto.TaskStatus_RUNNING, StartedAt: timestamppb.New(now)},
			{Name: "task-2", Status: proto.TaskStatus_RUNNING, StartedAt: timestamppb.New(now)},
		},
	}

	msgCh := make(chan recvResult, 3)
	msgCh <- recvResult{msg: tall}
	msgCh <- recvResult{msg: short}
	msgCh <- recvResult{err: io.EOF}

	var rec writeRecorder
	err := runWatchLoop(context.Background(), msgCh, newTestRenderer(now), "test-job", &rec, nil, nil, nil, nil)
	assert.EqualError(t, err, "stream ended before job 'test-job' completed")

	var frames []string
	for _, w := range rec.writes {
		if strings.HasPrefix(w, "\033[?2026h") {
			frames = append(frames, w)
		}
	}
	require.Len(t, frames, 2, "one write per rendered frame")

	for _, frame := range frames {
		assert.True(t, strings.HasSuffix(frame, "\033[?2026l"), "a frame ends its synchronized update")
		assert.NotContains(t, frame, "\r\033[J", "the display is overwritten, not cleared before the new text")
	}
	assert.Contains(t, frames[1], "\033[2A", "the second frame starts over the first one's three lines")
	assert.Contains(t, frames[1], "\033[K\033[J", "lines left by the taller first frame get cleared")
}
