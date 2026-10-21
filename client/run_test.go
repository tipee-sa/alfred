package main

import (
	"io"
	"testing"

	"github.com/gammadia/alfred/proto"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRunSharesWatchFlags(t *testing.T) {
	// run follows its job through watchJob: every watch flag must exist on run too, or
	// watchOptionsFromFlags panics there.
	for _, name := range []string{"json", "once", "abort-on-failure", "abort-on-error"} {
		assert.NotNil(t, runCmd.Flags().Lookup(name), "run lacks --%s", name)
	}
	assert.NotPanics(t, func() { watchOptionsFromFlags(runCmd) })
}

// fakeLoadImageClient ends the stream with recvErr once the client closes its side.
type fakeLoadImageClient struct {
	proto.Alfred_LoadImageClient
	recvErr    error
	closedSend bool
}

func (c *fakeLoadImageClient) CloseSend() error {
	c.closedSend = true
	return nil
}

func (c *fakeLoadImageClient) Recv() (*proto.LoadImageResponse, error) {
	return nil, c.recvErr
}

func TestWaitImageLoaded(t *testing.T) {
	ok := &fakeLoadImageClient{recvErr: io.EOF}
	assert.NoError(t, waitImageLoaded(ok))
	assert.True(t, ok.closedSend)

	// The server ends the stream with its docker load's error: the job must not be scheduled.
	failed := &fakeLoadImageClient{recvErr: status.Error(codes.Unknown, "exit status 1")}
	assert.EqualError(t, waitImageLoaded(failed), "server failed to load image: exit status 1")
}
