package openstack

import (
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"slices"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	// Connections a node's sessions spread over at most, the main one included. At sshd's
	// default of 10 sessions each, that is 80 sessions.
	maxSessionConnections = 8
	// How long opening a session waits for a free one once every connection is full
	sessionWait = time.Minute
	// Bounds an SSH handshake, which ssh.ClientConfig.Timeout does not cover
	sshHandshakeTimeout  = 10 * time.Second
	sshKeepaliveInterval = 30 * time.Second
)

// sessionPool opens a node's SSH sessions, over as many connections as they need.
//
// sshd caps the sessions of one connection (MaxSessions, 10 by default) and refuses the next
// with "ssh: rejected: connect failed (open failed)". Each running step holds a session for as
// long as it runs (docker logs --follow), so a node running a dozen tasks sits at that cap, and
// the mkdir, archive and delete of the tasks starting and ending around them were refused:
// tasks failed creating their workspace, artifacts and logs were lost (24 tasks of one job on
// 2026-11-25). The cap is per connection, so the pool opens another one when all are full.
//
// A refused session ran nothing, so opening it again, there or elsewhere, is always safe.
type sessionPool struct {
	// The node's connection, which also carries its Docker client. The node owns it.
	main *ssh.Client
	// Opens another connection to the node
	dial func() (*ssh.Client, error)
	log  *slog.Logger

	maxConnections int
	wait           time.Duration

	// One caller at a time opens a connection: the others wait, then find room on it
	dialMu sync.Mutex

	mu     sync.Mutex
	extra  []*ssh.Client // the connections opened by the pool, which owns them
	closed bool
	done   chan struct{}
}

func newSessionPool(main *ssh.Client, dial func() (*ssh.Client, error), log *slog.Logger) *sessionPool {
	return &sessionPool{
		main:           main,
		dial:           dial,
		log:            log,
		maxConnections: maxSessionConnections,
		wait:           sessionWait,
		done:           make(chan struct{}),
	}
}

// NewSession opens a session on the first connection with room for it, opening another
// connection if none has. With every connection full and no way to open another, it waits for
// a session to end, for p.wait at most, then returns the refusal.
func (p *sessionPool) NewSession() (*ssh.Session, error) {
	deadline := time.Now().Add(p.wait)
	for attempt := 0; ; attempt++ {
		session, err := p.open()
		if err == nil || !isSessionRefused(err) {
			return session, err
		}

		// 200 ms, 400 ms, ... 3.2 s at most, each halved to doubled at random: the sessions
		// refused together do not come back together
		delay := 200 * time.Millisecond << min(attempt, 4)
		delay = delay/2 + rand.N(delay)
		// The last attempt is made at the deadline, not given up before it
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, err
		}
		delay = min(delay, remaining)
		select {
		case <-time.After(delay):
		case <-p.done:
			return nil, err
		}
	}
}

// open tries each connection once, then a new one. An error that is not a refusal comes from
// the main connection: the node is gone, and its Docker client with it.
func (p *sessionPool) open() (*ssh.Session, error) {
	session, refusal := p.main.NewSession()
	if refusal == nil || !isSessionRefused(refusal) {
		return session, refusal
	}
	if session := p.openOnExtra(); session != nil {
		return session, nil
	}

	p.dialMu.Lock()
	defer p.dialMu.Unlock()

	// Another caller may have opened a connection while this one waited for the lock
	if session := p.openOnExtra(); session != nil {
		return session, nil
	}

	p.mu.Lock()
	connections, closed := 1+len(p.extra), p.closed
	p.mu.Unlock()
	if closed || connections >= p.maxConnections {
		return nil, refusal
	}

	client, err := p.dial()
	if err != nil {
		// Still a refusal for NewSession, which tries again: a VM too busy to answer one
		// handshake may answer the next
		return nil, fmt.Errorf("%w (and failed to open another SSH connection: %v)", refusal, err)
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = client.Close()
		return nil, refusal
	}
	p.extra = append(p.extra, client)
	connections = 1 + len(p.extra)
	p.mu.Unlock()

	p.log.Info("Opened another SSH connection to the node", "connections", connections)
	go p.keepAlive(client)

	session, err = client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("failed to open a session on a new SSH connection: %w", err)
	}
	return session, nil
}

// openOnExtra returns a session opened on one of the pool's own connections, or nil if none
// has room. A connection failing otherwise than by refusing is dropped.
func (p *sessionPool) openOnExtra() *ssh.Session {
	p.mu.Lock()
	clients := slices.Clone(p.extra)
	p.mu.Unlock()

	for _, client := range clients {
		session, err := client.NewSession()
		if err == nil {
			return session
		}
		if !isSessionRefused(err) {
			p.drop(client, err)
		}
	}
	return nil
}

// keepAlive keeps an idle connection from being dropped on the way, as the node does for its
// main one: a step that prints nothing for hours leaves its log stream silent.
func (p *sessionPool) keepAlive(client *ssh.Client) {
	ticker := time.NewTicker(sshKeepaliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
			if _, _, err := client.SendRequest("keepalive@alfred", true, nil); err != nil {
				p.drop(client, err)
				return
			}
		}
	}
}

// drop closes a connection of the pool that stopped working and forgets it.
func (p *sessionPool) drop(client *ssh.Client, cause error) {
	p.mu.Lock()
	index := slices.Index(p.extra, client)
	if index >= 0 {
		p.extra = slices.Delete(p.extra, index, index+1)
	}
	closed := p.closed
	p.mu.Unlock()

	if index >= 0 {
		_ = client.Close()
		if !closed {
			p.log.Warn("Dropped an SSH connection to the node", "error", cause)
		}
	}
}

// Close closes the connections the pool opened, and ends the waits for a session. The main
// connection is the node's to close.
func (p *sessionPool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	clients := p.extra
	p.extra = nil
	close(p.done)
	p.mu.Unlock()

	for _, client := range clients {
		_ = client.Close()
	}
}

// isSessionRefused tells whether the server refused to open the session, as sshd does past
// MaxSessions, rather than the connection failing.
func isSessionRefused(err error) bool {
	var refusal *ssh.OpenChannelError
	return errors.As(err, &refusal)
}

// dialSSH is ssh.Dial with a bound on the handshake: config.Timeout covers only the TCP
// connection, and a VM that accepts it but no longer answers would hold the caller forever.
func dialSSH(address string, config *ssh.ClientConfig) (*ssh.Client, error) {
	conn, err := net.DialTimeout("tcp", address, config.Timeout)
	if err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Now().Add(sshHandshakeTimeout)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	sshConn, channels, requests, err := ssh.NewClientConn(conn, address, config)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = sshConn.Close()
		return nil, err
	}
	return ssh.NewClient(sshConn, channels, requests), nil
}
