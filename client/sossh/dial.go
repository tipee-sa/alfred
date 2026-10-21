package sossh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type SSHSocatConn struct {
	io.ReadCloser
	io.WriteCloser
	cmd       *exec.Cmd
	cancel    context.CancelFunc
	closeOnce sync.Once
	closeErr  error
}

var _ net.Conn = (*SSHSocatConn)(nil)

// Close kills ssh and waits for it to exit, so that no ssh is left behind
// once Close returns, even one stuck before the tunnel is up (e.g. waiting on
// an SSH agent). Safe to call more than once and from several goroutines.
func (c *SSHSocatConn) Close() error {
	c.closeOnce.Do(func() {
		c.closeErr = errors.Join(c.ReadCloser.Close(), c.WriteCloser.Close())
		c.cancel()
		// ssh exits non-zero (or killed) here by design; only reaping it matters.
		_ = c.cmd.Wait()
	})
	return c.closeErr
}

func (c *SSHSocatConn) LocalAddr() net.Addr {
	return nil
}

func (c *SSHSocatConn) RemoteAddr() net.Addr {
	return nil
}

func (c *SSHSocatConn) SetDeadline(t time.Time) error {
	return fmt.Errorf("not implemented")
}

func (c *SSHSocatConn) SetReadDeadline(t time.Time) error {
	return fmt.Errorf("not implemented")
}

func (c *SSHSocatConn) SetWriteDeadline(t time.Time) error {
	return fmt.Errorf("not implemented")
}

func DialContext(ctx context.Context, network, addr, username, target string) (net.Conn, error) {
	if network != "tcp" {
		return nil, fmt.Errorf("unsupported network: %s", network)
	}

	ctx, cancel := context.WithCancel(ctx)

	host, port, _ := strings.Cut(addr, ":")
	if port == "" {
		port = "22"
	}

	cmd := exec.CommandContext(
		ctx,
		"ssh", fmt.Sprintf("%s@%s", username, host), "-p", fmt.Sprint(port), "-o", "BatchMode=yes", "--",
		"socat", "stdio", fmt.Sprintf("%s:%s", network, target),
	)

	cmd.Stderr = os.Stderr

	in, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	out, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to start socat: %w", err)
	}

	return &SSHSocatConn{
		ReadCloser:  in,
		WriteCloser: out,
		cmd:         cmd,
		cancel:      cancel,
	}, nil
}
