package openstack

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// meter counts the bytes read through it and the time spent blocked in Read, which is the
// time its consumer waited on the producer upstream. One goroutine reads it; read its fields
// once that goroutine is done.
type meter struct {
	r     io.Reader
	bytes int64
	wait  time.Duration
	first time.Time // when the first Read began
	eof   time.Time // when the last byte had been read
}

func (m *meter) Read(p []byte) (int, error) {
	start := time.Now()
	if m.first.IsZero() {
		m.first = start
	}
	n, err := m.r.Read(p)
	m.wait += time.Since(start)
	m.bytes += int64(n)
	if err == io.EOF {
		m.eof = time.Now()
	}
	return n, err
}

// transferStats says where an image transfer spent its time: getting the image's archive
// (building it, or waiting for the transfer building it), sending it, or loading it on the node.
type transferStats struct {
	built      bool  // this transfer built the archive
	sent       meter // the archive, read by the SSH session
	start, end time.Time
	opened     time.Time // when the archive was ready to send
}

func (s *transferStats) logAttrs() []any {
	seconds := func(d time.Duration) float64 { return d.Round(100 * time.Millisecond).Seconds() }
	return []any{
		"archiveBuilt", s.built,
		"sentBytes", s.sent.bytes,
		"totalSeconds", seconds(s.end.Sub(s.start)),
		// building the archive, or waiting for the transfer that builds it
		"archiveSeconds", seconds(s.opened.Sub(s.start)),
		// starting the command on the node, until the session reads
		"setupSeconds", seconds(s.sent.first.Sub(s.opened)),
		// the session blocked writing: the network, or docker load on the node pushing back
		"sendSeconds", seconds(s.sent.eof.Sub(s.sent.first) - s.sent.wait),
		// docker load finishing on the node after the last byte arrived
		"loadTailSeconds", seconds(s.end.Sub(s.sent.eof)),
	}
}

// transferImage streams the image's archive to the node, building it first if no other
// transfer has: zstd --decompress | docker load on the node, through SSH.
func (n *Node) transferImage(tag string, stats *transferStats) error {
	*stats = transferStats{start: time.Now()}
	archive, built, err := n.provisioner.archives.open(tag)
	stats.built = built
	if err != nil {
		return err
	}
	defer archive.Close()
	stats.opened = time.Now()

	session, err := newSession(n.ssh)
	if err != nil {
		return err
	}
	defer session.Close()

	var loadOutput bytes.Buffer
	stats.sent = meter{r: archive}
	session.Stdin = &stats.sent
	session.Stdout = &loadOutput
	session.Stderr = os.Stderr

	err = session.Run("zstd --decompress | docker load")
	stats.end = time.Now()
	if err != nil {
		return fmt.Errorf("failed to docker load '%s' (output: %s): %w", tag, strings.TrimSpace(loadOutput.String()), err)
	}
	return nil
}
