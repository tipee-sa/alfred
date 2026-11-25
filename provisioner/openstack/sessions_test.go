package openstack

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// sshTestServer is an SSH server refusing a connection's sessions past maxSessions, with the
// reason and message sshd gives past MaxSessions. A session's command never ends: the session
// lasts until its client closes it, like a step's log stream.
type sshTestServer struct {
	listener    net.Listener
	config      *ssh.ServerConfig
	maxSessions int

	// Connections accepted since the start, and those still open
	accepted atomic.Int32
	open     atomic.Int32

	mu    sync.Mutex
	conns []net.Conn
}

func newSSHTestServer(t *testing.T, maxSessions int) *sshTestServer {
	t.Helper()

	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &sshTestServer{listener: listener, config: config, maxSessions: maxSessions}
	t.Cleanup(func() {
		_ = listener.Close()
		s.closeConnections(0)
	})

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns = append(s.conns, conn)
			s.mu.Unlock()
			go s.serve(conn)
		}
	}()
	return s
}

func (s *sshTestServer) serve(conn net.Conn) {
	sshConn, channels, requests, err := ssh.NewServerConn(conn, s.config)
	if err != nil {
		_ = conn.Close()
		return
	}
	s.accepted.Add(1)
	s.open.Add(1)
	defer s.open.Add(-1)
	defer sshConn.Close()
	go ssh.DiscardRequests(requests)

	var sessions atomic.Int32
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
			continue
		}
		if int(sessions.Add(1)) > s.maxSessions {
			sessions.Add(-1)
			_ = newChannel.Reject(ssh.ConnectionFailed, "open failed")
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			sessions.Add(-1)
			continue
		}
		go func() {
			defer sessions.Add(-1)
			defer channel.Close()
			// Ends when the client closes the session
			for request := range channelRequests {
				if request.WantReply {
					_ = request.Reply(request.Type == "exec", nil)
				}
			}
		}()
	}
}

// closeConnections closes the connections accepted so far, but the first keep ones.
func (s *sshTestServer) closeConnections(keep int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, conn := range s.conns[min(keep, len(s.conns)):] {
		_ = conn.Close()
	}
	s.conns = s.conns[:min(keep, len(s.conns))]
}

func (s *sshTestServer) dial() (*ssh.Client, error) {
	return dialSSH(s.listener.Addr().String(), &ssh.ClientConfig{
		User:            "alfred",
		Timeout:         5 * time.Second,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
}

// pool returns a session pool on a new main connection to the server.
func (s *sshTestServer) pool(t *testing.T) *sessionPool {
	t.Helper()
	main, err := s.dial()
	if err != nil {
		t.Fatal(err)
	}
	pool := newSessionPool(main, s.dial, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() {
		pool.Close()
		_ = main.Close()
	})
	return pool
}

// eventually waits for the server to see what the client did: closing is not acknowledged.
func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The incident: more sessions at once than one connection takes (a dozen log streams, plus the
// tasks starting and ending around them). None is refused, and each connection is filled
// before another is opened.
func TestSessionPoolOpensAnotherConnectionWhenAllAreFull(t *testing.T) {
	server := newSSHTestServer(t, 4)
	pool := server.pool(t)

	const sessions = 18
	errs := make(chan error, sessions)
	for range sessions {
		go func() {
			session, err := pool.NewSession()
			if err == nil {
				// Held to the end of the test, like a running step's log stream
				err = session.Start("docker logs --follow")
			}
			errs <- err
		}()
	}
	for range sessions {
		if err := <-errs; err != nil {
			t.Fatalf("expected every session to open, got %v", err)
		}
	}

	// 18 sessions, 4 per connection
	if got := server.accepted.Load(); got != 5 {
		t.Errorf("expected 5 connections, got %d", got)
	}
}

func TestSessionPoolReusesTheRoomAClosedSessionLeaves(t *testing.T) {
	server := newSSHTestServer(t, 2)
	pool := server.pool(t)

	var held []*ssh.Session
	for range 4 {
		session, err := pool.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, session)
	}
	if got := server.accepted.Load(); got != 2 {
		t.Fatalf("expected 2 connections for 4 sessions, got %d", got)
	}

	for range 10 {
		_ = held[0].Close()
		session, err := pool.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		held[0] = session
	}
	// The closed session's place may not be free yet when the next one opens, which then takes
	// a third connection: never more, as two places are free from then on
	if got := server.accepted.Load(); got > 3 {
		t.Errorf("expected sessions to reuse the connections, got %d connections", got)
	}
}

// With no connection left to open, a session waits for one to end instead of failing.
func TestSessionPoolWaitsForAFreeSessionAtItsConnectionLimit(t *testing.T) {
	server := newSSHTestServer(t, 1)
	pool := server.pool(t)
	pool.maxConnections = 2

	first, err := pool.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.NewSession(); err != nil {
		t.Fatal(err)
	}

	time.AfterFunc(300*time.Millisecond, func() { _ = first.Close() })
	start := time.Now()
	if _, err := pool.NewSession(); err != nil {
		t.Fatalf("expected the session to open once another ended, got %v", err)
	}
	if waited := time.Since(start); waited < 300*time.Millisecond {
		t.Errorf("expected the session to wait for the other's end, it opened after %s", waited)
	}
	if got := server.accepted.Load(); got != 2 {
		t.Errorf("expected 2 connections, got %d", got)
	}
}

func TestSessionPoolReturnsTheRefusalAfterItsWait(t *testing.T) {
	server := newSSHTestServer(t, 1)
	pool := server.pool(t)
	pool.maxConnections = 1
	pool.wait = 500 * time.Millisecond

	if _, err := pool.NewSession(); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, err := pool.NewSession()
	var refusal *ssh.OpenChannelError
	if !errors.As(err, &refusal) {
		t.Fatalf("expected the server's refusal, got %v", err)
	}
	if got, want := err.Error(), "ssh: rejected: connect failed (open failed)"; got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
	if waited := time.Since(start); waited < 200*time.Millisecond || waited > 2*time.Second {
		t.Errorf("expected a wait of about %s, got %s", pool.wait, waited)
	}
}

// A VM too busy for another connection: the session waits for it, or for a place, all the same.
func TestSessionPoolWaitsWhenAnotherConnectionCannotBeOpened(t *testing.T) {
	server := newSSHTestServer(t, 1)
	pool := server.pool(t)
	var dials atomic.Int32
	pool.dial = func() (*ssh.Client, error) {
		if dials.Add(1) == 1 {
			return nil, errors.New("handshake timed out")
		}
		return server.dial()
	}

	if _, err := pool.NewSession(); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.NewSession(); err != nil {
		t.Fatalf("expected the session to open on a second attempt to connect, got %v", err)
	}
	if got := dials.Load(); got != 2 {
		t.Errorf("expected 2 attempts to connect, got %d", got)
	}

	pool.dial = func() (*ssh.Client, error) { return nil, errors.New("handshake timed out") }
	pool.wait = 300 * time.Millisecond
	_, err := pool.NewSession()
	if err == nil || !isSessionRefused(err) {
		t.Fatalf("expected the refusal, got %v", err)
	}
	if got, want := err.Error(), "ssh: rejected: connect failed (open failed) (and failed to open another SSH connection: handshake timed out)"; got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

// A connection of the pool that died is forgotten, and replaced when its sessions are needed.
func TestSessionPoolDropsAConnectionThatDied(t *testing.T) {
	server := newSSHTestServer(t, 1)
	pool := server.pool(t)

	for range 3 {
		if _, err := pool.NewSession(); err != nil {
			t.Fatal(err)
		}
	}
	if got := server.accepted.Load(); got != 3 {
		t.Fatalf("expected 3 connections, got %d", got)
	}

	// The main connection stays, full
	server.closeConnections(1)
	eventually(t, "the server to close the connections", func() bool { return server.open.Load() == 1 })

	if _, err := pool.NewSession(); err != nil {
		t.Fatalf("expected a session on a new connection, got %v", err)
	}
	if got := server.accepted.Load(); got != 4 {
		t.Errorf("expected a 4th connection, got %d", got)
	}
	pool.mu.Lock()
	extra := len(pool.extra)
	pool.mu.Unlock()
	if extra != 1 {
		t.Errorf("expected the pool to keep only the new connection, it has %d", extra)
	}
}

// The node is gone: no waiting, no other connection.
func TestSessionPoolFailsAtOnceWhenTheMainConnectionIsGone(t *testing.T) {
	server := newSSHTestServer(t, 1)
	pool := server.pool(t)
	_ = pool.main.Close()

	start := time.Now()
	_, err := pool.NewSession()
	if err == nil || isSessionRefused(err) {
		t.Fatalf("expected the connection's error, got %v", err)
	}
	if waited := time.Since(start); waited > 150*time.Millisecond {
		t.Errorf("expected no wait, got %s", waited)
	}
	if got := server.accepted.Load(); got != 1 {
		t.Errorf("expected no other connection, got %d", got)
	}
}

func TestSessionPoolCloseClosesItsConnectionsAndEndsTheWaits(t *testing.T) {
	server := newSSHTestServer(t, 1)
	pool := server.pool(t)
	pool.maxConnections = 3

	for range 3 {
		if _, err := pool.NewSession(); err != nil {
			t.Fatal(err)
		}
	}

	waiting := make(chan error, 1)
	go func() {
		_, err := pool.NewSession()
		waiting <- err
	}()
	time.Sleep(100 * time.Millisecond)

	pool.Close()
	pool.Close() // a node is terminated once, but closing twice must not panic

	select {
	case err := <-waiting:
		if err == nil || !isSessionRefused(err) {
			t.Errorf("expected the waiting session to get the refusal, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected Close to end the wait for a session")
	}
	// The main connection is not the pool's to close
	eventually(t, "the pool's connections to close", func() bool { return server.open.Load() == 1 })

	if _, err := pool.NewSession(); err == nil {
		t.Error("expected no session from a closed pool whose main connection is full")
	}
	if got := server.accepted.Load(); got != 3 {
		t.Errorf("expected a closed pool to open no connection, got %d", got)
	}
}

func TestDialSSHBoundsTheHandshake(t *testing.T) {
	// Accepts the connection, then says nothing
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	if sshHandshakeTimeout > 15*time.Second {
		t.Skipf("the handshake timeout (%s) is too long to wait for in a test", sshHandshakeTimeout)
	}
	if testing.Short() {
		t.Skip("waits for the handshake timeout")
	}

	start := time.Now()
	_, err = dialSSH(listener.Addr().String(), &ssh.ClientConfig{
		Timeout:         time.Second,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if err == nil {
		t.Fatal("expected the handshake to fail")
	}
	if waited := time.Since(start); waited < sshHandshakeTimeout || waited > sshHandshakeTimeout+5*time.Second {
		t.Errorf("expected the handshake to fail after %s, it took %s", sshHandshakeTimeout, waited)
	}
}
