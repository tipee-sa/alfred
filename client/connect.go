package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/gammadia/alfred/client/sossh"
	"github.com/gammadia/alfred/proto"
	"github.com/gammadia/alfred/server/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials/insecure"
)

// defaultConnectTimeout bounds connecting to the server. A healthy connection,
// tunnel included, takes a second or two; the rest is for a human at the desk
// to notice and approve an SSH agent's unlock prompt, which ssh waits on
// silently. Unattended (an agent, CI), a locked agent costs this long, not forever.
const defaultConnectTimeout = time.Minute

const connectTimeoutEnv = "ALFRED_CONNECT_TIMEOUT"

type connectOptions struct {
	host         string
	port         string
	sshTunneling bool
	sshUsername  string
	sshPort      int
	timeout      time.Duration
}

func (o connectOptions) remote() string {
	return fmt.Sprintf("%s:%s", o.host, o.port)
}

func (o connectOptions) sshTarget() string {
	return fmt.Sprintf("%s@%s:%d", o.sshUsername, o.host, o.sshPort)
}

func (o connectOptions) describe() string {
	if o.sshTunneling {
		return fmt.Sprintf("%s (ssh tunnel to %s)", o.remote(), o.sshTarget())
	}
	return o.remote()
}

// connect opens the gRPC connection and proves it with a Ping, within
// o.timeout. gRPC dials lazily and the ssh tunnel starts in the background, so
// without the Ping nothing would wait for either until the command's first RPC,
// and nothing would bound that wait but gRPC's own handshake timeout. The
// timeout covers only this Ping: RPCs made afterwards, streams included, are
// not limited by it.
//
// ctx bounds the life of the connection (the ssh tunnel is killed when it is done).
func connect(ctx context.Context, o connectOptions) (*grpc.ClientConn, error) {
	var tunnelsMutex sync.Mutex
	var tunnels []net.Conn
	var tunnelsClosed bool // set on failure: a dial still in flight must not start ssh after the cleanup

	conn, err := grpc.Dial(
		o.remote(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(config.MaxPacketSize)),
		// gRPC abandons a connection attempt after 20s by default (closing the
		// tunnel, so ssh is killed mid-prompt). Let it last longer than the Ping
		// below, so that the timeout which fires is ours, with its explanation.
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff:           backoff.DefaultConfig,
			MinConnectTimeout: o.timeout + 10*time.Second,
		}),
		grpc.WithContextDialer(func(dialCtx context.Context, addr string) (net.Conn, error) {
			if !o.sshTunneling {
				var dialer net.Dialer
				return dialer.DialContext(dialCtx, "tcp", addr)
			}

			// Starting ssh and registering it happen under one lock, so the
			// cleanup below either sees the tunnel or stops it from starting.
			tunnelsMutex.Lock()
			defer tunnelsMutex.Unlock()
			if tunnelsClosed {
				return nil, errors.New("connection abandoned")
			}
			// Not dialCtx: the tunnel must outlive the dial.
			tunnel, err := sossh.DialContext(ctx, "tcp", fmt.Sprintf("%s:%d", o.host, o.sshPort), o.sshUsername, fmt.Sprintf("127.0.0.1:%s", o.port))
			if err == nil {
				tunnels = append(tunnels, tunnel)
			}
			return tunnel, err
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to dial gRPC: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()

	// Not WaitForReady: a connection that fails (ssh refused, server down)
	// fails the Ping at once; only one still being established is waited for.
	if _, err := proto.NewAlfredClient(conn).Ping(pingCtx, &proto.PingRequest{}); err != nil {
		_ = conn.Close()
		// gRPC closes the tunnel too, but in the background: make sure ssh is
		// gone before the client exits, or it would linger, still waiting.
		tunnelsMutex.Lock()
		tunnelsClosed = true
		for _, tunnel := range tunnels {
			_ = tunnel.Close()
		}
		tunnelsMutex.Unlock()

		if ctx.Err() == nil && errors.Is(pingCtx.Err(), context.DeadlineExceeded) {
			return nil, connectTimeoutError(o)
		}
		return nil, fmt.Errorf("%s: %w", o.describe(), err)
	}

	return conn, nil
}

func connectTimeoutError(o connectOptions) error {
	if !o.sshTunneling {
		return fmt.Errorf(
			"no answer from %s within %s.\n"+
				"Check that the server is running and reachable, or raise --connect-timeout (%s).",
			o.remote(), o.timeout, connectTimeoutEnv,
		)
	}
	return fmt.Errorf(
		"no answer from %s within %s through the ssh tunnel to %s.\n"+
			"ssh may be waiting on an SSH agent: a locked agent does not refuse, it waits silently to be unlocked.\n"+
			"Check that `ssh -p %d %s@%s` connects, or raise --connect-timeout (%s).",
		o.remote(), o.timeout, o.sshTarget(),
		o.sshPort, o.sshUsername, o.host, connectTimeoutEnv,
	)
}

// connectTimeoutFlagDefault reads the --connect-timeout default from the
// environment. An invalid value is reported when connecting, not here (init
// cannot fail), and only if the flag does not override it.
func connectTimeoutFlagDefault() (time.Duration, error) {
	value := os.Getenv(connectTimeoutEnv)
	if value == "" {
		return defaultConnectTimeout, nil
	}
	timeout, err := time.ParseDuration(value)
	if err != nil || timeout <= 0 {
		return defaultConnectTimeout, fmt.Errorf("%s=%q is not a positive duration (e.g. 30s, 2m)", connectTimeoutEnv, value)
	}
	return timeout, nil
}
