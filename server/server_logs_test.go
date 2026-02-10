package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/gammadia/alfred/proto"
	"github.com/gammadia/alfred/server/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
)

func TestMain(m *testing.M) {
	// Initialize the logger so server code doesn't panic on log calls
	log.Base = slog.New(slog.NewTextHandler(io.Discard, nil))
	// Use reflection-free init: set the unexported logger via the public Init path
	// is not possible without viper, so we call the log functions' underlying logger directly.
	// Instead, just call Init after setting viper defaults.
	os.Setenv("ALFRED_LOG_LEVEL", "ERROR")
	os.Setenv("ALFRED_LOG_FORMAT", "text")
	_ = log.Init()
	os.Exit(m.Run())
}

// --- lastNLines tests ---

func TestLastNLines_BasicTail(t *testing.T) {
	data := []byte("line1\nline2\nline3\nline4\nline5\n")
	result := string(lastNLines(data, 3))
	assert.Equal(t, "line3\nline4\nline5\n", result)
}

func TestLastNLines_FewerLinesThanRequested(t *testing.T) {
	data := []byte("line1\nline2\n")
	result := string(lastNLines(data, 10))
	assert.Equal(t, "line1\nline2\n", result)
}

func TestLastNLines_ExactLineCount(t *testing.T) {
	data := []byte("line1\nline2\nline3\n")
	result := string(lastNLines(data, 3))
	assert.Equal(t, "line1\nline2\nline3\n", result)
}

func TestLastNLines_SingleLine(t *testing.T) {
	data := []byte("only line\n")
	result := string(lastNLines(data, 1))
	assert.Equal(t, "only line\n", result)
}

func TestLastNLines_NoTrailingNewline(t *testing.T) {
	data := []byte("line1\nline2\nline3")
	result := string(lastNLines(data, 2))
	assert.Equal(t, "line2\nline3", result)
}

func TestLastNLines_EmptyData(t *testing.T) {
	result := lastNLines([]byte{}, 5)
	assert.Empty(t, result)
}

func TestLastNLines_NilData(t *testing.T) {
	result := lastNLines(nil, 5)
	assert.Nil(t, result)
}

func TestLastNLines_ZeroLines(t *testing.T) {
	data := []byte("line1\nline2\n")
	result := string(lastNLines(data, 0))
	assert.Equal(t, "line1\nline2\n", result, "n=0 returns all data")
}

func TestLastNLines_NegativeLines(t *testing.T) {
	data := []byte("line1\nline2\n")
	result := string(lastNLines(data, -1))
	assert.Equal(t, "line1\nline2\n", result, "n<0 returns all data")
}

func TestLastNLines_WithTailHeaders(t *testing.T) {
	// Simulates tail -v output with file headers
	data := []byte("==> step-1.log <==\nline1\nline2\n\n==> step-2.log <==\nlineA\nlineB\n")
	result := string(lastNLines(data, 3))
	assert.Equal(t, "==> step-2.log <==\nlineA\nlineB\n", result)
}

func TestLastNLines_OnlyNewlines(t *testing.T) {
	data := []byte("\n\n\n\n\n")
	result := string(lastNLines(data, 2))
	assert.Equal(t, "\n\n", result)
}

func TestLastNLines_OneLineNoNewline(t *testing.T) {
	data := []byte("hello")
	result := string(lastNLines(data, 1))
	assert.Equal(t, "hello", result)
}

func TestLastNLines_OneLineRequestMoreThanAvailable(t *testing.T) {
	data := []byte("hello\n")
	result := string(lastNLines(data, 100))
	assert.Equal(t, "hello\n", result)
}

// --- streamReader tests ---

// mockLogStream implements proto.Alfred_StreamTaskLogsServer for testing.
type mockLogStream struct {
	chunks []*proto.StreamTaskLogsChunk
	ctx    context.Context
}

func (m *mockLogStream) Send(chunk *proto.StreamTaskLogsChunk) error {
	m.chunks = append(m.chunks, chunk)
	return nil
}
func (m *mockLogStream) Context() context.Context              { return m.ctx }
func (m *mockLogStream) SetHeader(metadata.MD) error           { return nil }
func (m *mockLogStream) SendHeader(metadata.MD) error          { return nil }
func (m *mockLogStream) SetTrailer(metadata.MD)                {}
func (m *mockLogStream) SendMsg(interface{}) error             { return nil }
func (m *mockLogStream) RecvMsg(interface{}) error             { return nil }

func TestStreamReader_SendsAllContent(t *testing.T) {
	content := "line1\nline2\nline3\n"
	reader := io.NopCloser(strings.NewReader(content))

	mock := &mockLogStream{ctx: context.Background()}
	err := streamReader(mock, reader, "test-job", "test-task")

	require.NoError(t, err)
	assert.NotEmpty(t, mock.chunks)

	var received bytes.Buffer
	for _, chunk := range mock.chunks {
		received.Write(chunk.Data)
		assert.Equal(t, uint32(len(chunk.Data)), chunk.Length)
	}
	assert.Equal(t, content, received.String())
}

func TestStreamReader_EmptyReader(t *testing.T) {
	reader := io.NopCloser(strings.NewReader(""))

	mock := &mockLogStream{ctx: context.Background()}
	err := streamReader(mock, reader, "test-job", "test-task")

	require.NoError(t, err)
	assert.Empty(t, mock.chunks)
}

func TestStreamReader_LargeContent_MultipleChunks(t *testing.T) {
	// Create content larger than the 32KB chunk buffer
	content := strings.Repeat("x", 100*1024) // 100KB
	reader := io.NopCloser(strings.NewReader(content))

	mock := &mockLogStream{ctx: context.Background()}
	err := streamReader(mock, reader, "test-job", "test-task")

	require.NoError(t, err)
	assert.Greater(t, len(mock.chunks), 1, "large content should be sent in multiple chunks")

	var received bytes.Buffer
	for _, chunk := range mock.chunks {
		received.Write(chunk.Data)
	}
	assert.Equal(t, content, received.String())
}
