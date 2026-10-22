package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gammadia/alfred/client/sossh"
	"github.com/gammadia/alfred/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type pingServer struct {
	proto.UnimplementedAlfredServer
	watchJobsDelay time.Duration
}

func (s *pingServer) Ping(context.Context, *proto.PingRequest) (*proto.PingResponse, error) {
	return &proto.PingResponse{Version: "test", Commit: "test"}, nil
}

func (s *pingServer) WatchJobs(_ *proto.WatchJobsRequest, srv proto.Alfred_WatchJobsServer) error {
	time.Sleep(s.watchJobsDelay)
	return srv.Send(&proto.JobsList{})
}

func startPingServer(t *testing.T, srv *pingServer) string {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer()
	proto.RegisterAlfredServer(server, srv)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	return listener.Addr().String()
}

// startSilentListener accepts connections and never says anything, like a
// server (or an sshd) that hangs.
func startSilentListener(t *testing.T) string {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { conn.Close() })
		}
	}()
	return listener.Addr().String()
}

func directOptions(t *testing.T, addr string, timeout time.Duration) connectOptions {
	host, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	return connectOptions{host: host, port: port, timeout: timeout}
}

func TestConnectPingsServer(t *testing.T) {
	conn, err := connect(context.Background(), directOptions(t, startPingServer(t, &pingServer{}), 5*time.Second))
	require.NoError(t, err)
	conn.Close()
}

func TestConnectTimeoutDoesNotLimitStreams(t *testing.T) {
	addr := startPingServer(t, &pingServer{watchJobsDelay: 600 * time.Millisecond})
	conn, err := connect(context.Background(), directOptions(t, addr, 200*time.Millisecond))
	require.NoError(t, err)
	defer conn.Close()

	stream, err := proto.NewAlfredClient(conn).WatchJobs(context.Background(), &proto.WatchJobsRequest{})
	require.NoError(t, err)
	_, err = stream.Recv()
	assert.NoError(t, err, "a stream outlasting the connect timeout must not be cut")
}

func TestConnectTimesOutOnSilentServer(t *testing.T) {
	addr := startSilentListener(t)
	start := time.Now()
	_, err := connect(context.Background(), directOptions(t, addr, 300*time.Millisecond))
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second)
	assert.Contains(t, err.Error(), "no answer from "+addr+" within 300ms")
	assert.Contains(t, err.Error(), "--connect-timeout")
	assert.NotContains(t, err.Error(), "ssh")
}

func TestConnectFailsFastWhenRefused(t *testing.T) {
	// A port nothing listens on: refused at once, not waited on until the timeout.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := listener.Addr().String()
	listener.Close()

	start := time.Now()
	_, err = connect(context.Background(), directOptions(t, addr, 30*time.Second))
	require.Error(t, err)
	assert.Less(t, time.Since(start), 10*time.Second)
	assert.Contains(t, err.Error(), addr)
	assert.NotContains(t, err.Error(), "no answer")
}

func TestConnectTimeoutKillsSSH(t *testing.T) {
	pidFile := installHangingSSH(t)

	_, err := connect(context.Background(), connectOptions{
		host:         "alfred.example",
		port:         "25373",
		sshTunneling: true,
		sshUsername:  "someone",
		sshPort:      2222,
		timeout:      300 * time.Millisecond,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no answer from alfred.example:25373 within 300ms through the ssh tunnel to someone@alfred.example:2222")
	assert.Contains(t, err.Error(), "locked agent")
	assert.Contains(t, err.Error(), "`ssh -p 2222 someone@alfred.example`")

	assertSSHGone(t, pidFile)
}

// installHangingSSH puts on PATH an ssh that never gets anywhere, like one
// waiting on a locked SSH agent, and returns the file it writes its pid to.
func installHangingSSH(t *testing.T) string {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "ssh.pid")
	script := "#!/bin/sh\necho $$ > " + pidFile + "\nexec sleep 60\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return pidFile
}

func assertSSHGone(t *testing.T, pidFile string) {
	t.Helper()
	content, err := os.ReadFile(pidFile)
	require.NoError(t, err, "the fake ssh should have been started")
	pid, err := strconv.Atoi(strings.TrimSpace(string(content)))
	require.NoError(t, err)
	assert.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH, "ssh must be killed and reaped")
}

func startHangingTunnel() (net.Conn, error) {
	return sossh.DialContext(context.Background(), "tcp", "alfred.example:2222", "someone", "127.0.0.1:25373")
}

func TestTunnelSetClosesOpenedTunnels(t *testing.T) {
	pidFile := installHangingSSH(t)
	var tunnels tunnelSet

	_, err := tunnels.open(startHangingTunnel)
	require.NoError(t, err)
	require.Eventually(t, func() bool { _, err := os.Stat(pidFile); return err == nil }, 5*time.Second, 10*time.Millisecond)

	tunnels.close()
	assertSSHGone(t, pidFile)
}

func TestTunnelSetStartsNoSSHOnceClosed(t *testing.T) {
	// The race: gRPC dials in the background, so a dial can come after a
	// failed connect has closed the tunnels. It must not start an ssh that
	// nothing would close.
	installHangingSSH(t) // should the guard break, never start a real ssh
	var tunnels tunnelSet
	tunnels.close()

	started := false
	tunnel, err := tunnels.open(func() (net.Conn, error) {
		started = true
		return startHangingTunnel()
	})
	if tunnel != nil {
		t.Cleanup(func() { tunnel.Close() })
	}
	assert.Error(t, err)
	assert.False(t, started, "no ssh may start once the set is closed")
}

func TestConnectTimeoutFlagDefault(t *testing.T) {
	t.Setenv(connectTimeoutEnv, "")
	timeout, err := connectTimeoutFlagDefault()
	assert.NoError(t, err)
	assert.Equal(t, defaultConnectTimeout, timeout)

	t.Setenv(connectTimeoutEnv, "90s")
	timeout, err = connectTimeoutFlagDefault()
	assert.NoError(t, err)
	assert.Equal(t, 90*time.Second, timeout)

	for _, invalid := range []string{"90", "-1s", "0s"} {
		t.Setenv(connectTimeoutEnv, invalid)
		timeout, err = connectTimeoutFlagDefault()
		assert.Error(t, err, invalid)
		assert.Equal(t, defaultConnectTimeout, timeout)
	}
}
