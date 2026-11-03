package openstack

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/samber/lo"
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

// transferStats says where an image transfer spent its time: producing the stream on the
// server (docker save, zstd), sending it, or loading it on the node.
type transferStats struct {
	saved      meter // docker save's output, read by zstd
	sent       meter // zstd's output, read by the SSH session
	start, end time.Time
}

func (s *transferStats) logAttrs() []any {
	seconds := func(d time.Duration) float64 { return d.Round(100 * time.Millisecond).Seconds() }
	return []any{
		"rawBytes", s.saved.bytes,
		"sentBytes", s.sent.bytes,
		"totalSeconds", seconds(s.end.Sub(s.start)),
		// starting the processes and the command on the node, until the session reads
		"setupSeconds", seconds(s.sent.first.Sub(s.start)),
		// zstd waiting on docker save
		"saveWaitSeconds", seconds(s.saved.wait),
		// the session waiting on zstd, docker save included
		"compressWaitSeconds", seconds(s.sent.wait),
		// the session blocked writing: the network, or docker load on the node pushing back
		"sendSeconds", seconds(s.sent.eof.Sub(s.sent.first) - s.sent.wait),
		// docker load finishing on the node after the last byte arrived
		"loadTailSeconds", seconds(s.end.Sub(s.sent.eof)),
	}
}

// transferImage streams an image to the node: docker save | zstd on the server, then
// zstd --decompress | docker load on the node through SSH.
func (n *Node) transferImage(tag string, stats *transferStats) error {
	session, err := newSession(n.ssh)
	if err != nil {
		return err
	}
	defer session.Close()

	var saveStderr, zstdStderr, loadOutput bytes.Buffer
	saveCmd := exec.Command("docker", "save", tag)
	saveCmd.Stderr = &saveStderr
	stats.saved = meter{r: lo.Must(saveCmd.StdoutPipe())}

	zstdCmd := exec.Command("zstd", "--compress", "--adapt=min=5,max=8")
	zstdCmd.Stdin = &stats.saved
	zstdCmd.Stderr = &zstdStderr
	stats.sent = meter{r: lo.Must(zstdCmd.StdoutPipe())}

	session.Stdin = &stats.sent
	session.Stdout = &loadOutput
	session.Stderr = os.Stderr

	stats.start = time.Now()
	if err := saveCmd.Start(); err != nil {
		return fmt.Errorf("failed to start docker save of '%s': %w", tag, err)
	}
	if err := zstdCmd.Start(); err != nil {
		_ = saveCmd.Process.Kill()
		_ = saveCmd.Wait()
		return fmt.Errorf("failed to start zstd for '%s': %w", tag, err)
	}

	loadErr := session.Run("zstd --decompress | docker load")
	stats.end = time.Now()
	if loadErr != nil {
		// Nothing reads the pipeline any more: stop it, or the Waits below block for good
		_ = saveCmd.Process.Kill()
		_ = zstdCmd.Process.Kill()
	}
	// zstd first: its Wait ends the goroutine that reads docker save's pipe, which docker
	// save's Wait then closes
	zstdErr := zstdCmd.Wait()
	saveErr := saveCmd.Wait()

	switch {
	// A failed docker save also fails docker load, on an empty or cut stream: report the
	// cause, unless docker save only died from the kill above (which leaves no output)
	case saveErr != nil && (loadErr == nil || saveStderr.Len() > 0):
		return fmt.Errorf("failed to docker save '%s' (output: %s): %w", tag, strings.TrimSpace(saveStderr.String()), saveErr)
	case loadErr != nil:
		return fmt.Errorf("failed to docker load '%s' (output: %s): %w", tag, strings.TrimSpace(loadOutput.String()), loadErr)
	case zstdErr != nil:
		return fmt.Errorf("failed to compress '%s' (output: %s): %w", tag, strings.TrimSpace(zstdStderr.String()), zstdErr)
	}
	return nil
}
