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
	"time"
)

type SSHSocatConn struct {
	io.ReadCloser
	io.WriteCloser
	cancel context.CancelFunc
}

var _ net.Conn = (*SSHSocatConn)(nil)

func (c *SSHSocatConn) Close() error {
	c.cancel()
	return errors.Join(c.ReadCloser.Close(), c.WriteCloser.Close())
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
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	out, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to start socat: %w", err)
	}

	return &SSHSocatConn{
		ReadCloser:  in,
		WriteCloser: out,
		cancel:      cancel,
	}, nil
}
