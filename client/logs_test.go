package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/gammadia/alfred/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Command registration tests ---

func TestLogsCmd_HasTailAlias(t *testing.T) {
	assert.Contains(t, logsCmd.Aliases, "tail")
}

func TestLogsCmd_HasFollowFlag(t *testing.T) {
	f := logsCmd.Flags().Lookup("follow")
	require.NotNil(t, f)
	assert.Equal(t, "f", f.Shorthand)
	assert.Equal(t, "false", f.DefValue)
}

func TestLogsCmd_HasLinesFlag(t *testing.T) {
	f := logsCmd.Flags().Lookup("lines")
	require.NotNil(t, f)
	assert.Equal(t, "n", f.Shorthand)
	assert.Equal(t, "100", f.DefValue)
}

func TestLogsCmd_RequiresExactlyTwoArgs(t *testing.T) {
	assert.Error(t, logsCmd.Args(logsCmd, []string{}))
	assert.Error(t, logsCmd.Args(logsCmd, []string{"job"}))
	assert.NoError(t, logsCmd.Args(logsCmd, []string{"job", "task"}))
	assert.Error(t, logsCmd.Args(logsCmd, []string{"job", "task", "extra"}))
}

// --- recvLogsLoop tests ---

// mockRecv builds a recv function from a sequence of results.
func mockRecv(results ...recvLogsResult) func() (*proto.StreamTaskLogsChunk, error) {
	i := 0
	return func() (*proto.StreamTaskLogsChunk, error) {
		if i >= len(results) {
			return nil, io.EOF
		}
		r := results[i]
		i++
		return r.chunk, r.err
	}
}

type recvLogsResult struct {
	chunk *proto.StreamTaskLogsChunk
	err   error
}

func chunk(data string) recvLogsResult {
	return recvLogsResult{chunk: &proto.StreamTaskLogsChunk{
		Data:   []byte(data),
		Length: uint32(len(data)),
	}}
}

func recvErr(err error) recvLogsResult {
	return recvLogsResult{err: err}
}

func TestRecvLogsLoop_WritesChunksToWriter(t *testing.T) {
	recv := mockRecv(
		chunk("line1\n"),
		chunk("line2\n"),
		chunk("line3\n"),
		recvErr(io.EOF),
	)

	var buf bytes.Buffer
	err := recvLogsLoop(context.Background(), recv, &buf)

	assert.NoError(t, err)
	assert.Equal(t, "line1\nline2\nline3\n", buf.String())
}

func TestRecvLogsLoop_EOF_ReturnsNil(t *testing.T) {
	recv := mockRecv(recvErr(io.EOF))

	var buf bytes.Buffer
	err := recvLogsLoop(context.Background(), recv, &buf)

	assert.NoError(t, err)
	assert.Empty(t, buf.String())
}

func TestRecvLogsLoop_ContextCancelled_ReturnsNil(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	streamErr := errors.New("rpc error: code = Canceled desc = context canceled")
	recv := mockRecv(
		chunk("some output\n"),
		recvErr(streamErr),
	)

	var buf bytes.Buffer
	err := recvLogsLoop(ctx, recv, &buf)

	assert.NoError(t, err, "context cancellation should be a clean exit")
	assert.Equal(t, "some output\n", buf.String(), "chunks received before cancellation should be written")
}

func TestRecvLogsLoop_StreamError_ReturnsError(t *testing.T) {
	streamErr := errors.New("connection lost")
	recv := mockRecv(
		chunk("partial\n"),
		recvErr(streamErr),
	)

	var buf bytes.Buffer
	err := recvLogsLoop(context.Background(), recv, &buf)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "connection lost")
	assert.Equal(t, "partial\n", buf.String(), "chunks received before error should be written")
}

func TestRecvLogsLoop_MultipleChunks_Reassembled(t *testing.T) {
	recv := mockRecv(
		chunk("==> step-1.log <==\n"),
		chunk("output line 1\n"),
		chunk("output line 2\n"),
		chunk("\n==> step-2.log <==\n"),
		chunk("output line A\n"),
		recvErr(io.EOF),
	)

	var buf bytes.Buffer
	err := recvLogsLoop(context.Background(), recv, &buf)

	assert.NoError(t, err)
	expected := "==> step-1.log <==\noutput line 1\noutput line 2\n\n==> step-2.log <==\noutput line A\n"
	assert.Equal(t, expected, buf.String())
}

func TestRecvLogsLoop_EmptyChunks_Ignored(t *testing.T) {
	recv := mockRecv(
		chunk(""),
		chunk("real data\n"),
		chunk(""),
		recvErr(io.EOF),
	)

	var buf bytes.Buffer
	err := recvLogsLoop(context.Background(), recv, &buf)

	assert.NoError(t, err)
	assert.Equal(t, "real data\n", buf.String())
}

func TestRecvLogsLoop_ImmediateEOF(t *testing.T) {
	recv := mockRecv(recvErr(io.EOF))

	var buf bytes.Buffer
	err := recvLogsLoop(context.Background(), recv, &buf)

	assert.NoError(t, err)
	assert.Empty(t, buf.String())
}

func TestRecvLogsLoop_ImmediateContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	recv := mockRecv(recvErr(errors.New("canceled")))

	var buf bytes.Buffer
	err := recvLogsLoop(ctx, recv, &buf)

	assert.NoError(t, err, "immediate cancellation should be clean")
	assert.Empty(t, buf.String())
}
