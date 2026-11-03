package openstack

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/samber/lo"
)

// imageArchives keeps one compressed docker save of each image the nodes load. Nodes asking
// for the same image at once share one docker save instead of running one each: when a job
// lands on a running fleet, every node asks for its images at the same moment, and 15
// concurrent saves of a 1.1 GB image took 73-97 s each, against 11 s for one alone.
type imageArchives struct {
	dir string
	// An archive no transfer has asked for in this long is deleted
	ttl time.Duration
	log *slog.Logger
	// The commands that write the archive (docker save, then zstd), replaced in tests. The
	// context kills them at shutdown.
	save     func(ctx context.Context, tag string) *exec.Cmd
	compress func(ctx context.Context) *exec.Cmd

	// Canceled by removeAll: ends the sweep and the builds, releases the opens waiting
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	entries map[string]*imageArchive
}

var errArchivesClosed = errors.New("image archives closed: the server is shutting down")

type imageArchive struct {
	ready chan struct{} // closed once the archive is built, or has failed to

	// Set before ready is closed, read only after
	path     string
	err      error
	stats    archiveStats
	lastUsed time.Time // protected by imageArchives.mu
}

// archiveStats says how long building an archive took, and how much of that zstd waited on
// docker save.
type archiveStats struct {
	saved      meter // docker save's output, read by zstd
	bytes      int64 // the archive's size
	start, end time.Time
}

func (s *archiveStats) logAttrs() []any {
	seconds := func(d time.Duration) float64 { return d.Round(100 * time.Millisecond).Seconds() }
	return []any{
		"rawBytes", s.saved.bytes,
		"archiveBytes", s.bytes,
		"totalSeconds", seconds(s.end.Sub(s.start)),
		"saveWaitSeconds", seconds(s.saved.wait),
	}
}

// newImageArchives starts with an empty dir: archives a previous run left are not trusted,
// since it may have died writing one. It sweeps expired archives until removeAll.
func newImageArchives(dir string, ttl time.Duration, log *slog.Logger) (*imageArchives, error) {
	if err := os.RemoveAll(dir); err != nil {
		return nil, fmt.Errorf("failed to clear image archives in '%s': %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create image archives dir '%s': %w", dir, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &imageArchives{
		dir: dir,
		ttl: ttl,
		log: log,
		save: func(ctx context.Context, tag string) *exec.Cmd {
			return exec.CommandContext(ctx, "docker", "save", tag)
		},
		// All cores: one archive serves every node, so its build is worth them
		compress: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "zstd", "--compress", "-5", "-T0", "-q")
		},
		ctx:     ctx,
		cancel:  cancel,
		entries: make(map[string]*imageArchive),
	}
	go a.sweep()
	return a, nil
}

// sweep deletes expired archives every quarter of the TTL, so they go even when no transfer
// comes to trigger it.
func (a *imageArchives) sweep() {
	ticker := time.NewTicker(a.ttl / 4)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			a.mu.Lock()
			a.evict()
			a.mu.Unlock()
		}
	}
}

// open returns the image's archive opened for reading, building it first unless another call
// already has or is doing so, in which case it waits for that one. built says whether this
// call built it. A failed build is forgotten, so the next call tries again. After removeAll,
// it fails at once.
func (a *imageArchives) open(tag string) (f *os.File, built bool, err error) {
	a.mu.Lock()
	if a.ctx.Err() != nil {
		a.mu.Unlock()
		return nil, false, errArchivesClosed
	}
	archive, ok := a.entries[tag]
	if !ok {
		archive = &imageArchive{ready: make(chan struct{})}
		a.entries[tag] = archive
	}
	archive.lastUsed = time.Now()
	a.mu.Unlock()

	if !ok {
		a.build(tag, archive)
	}
	select {
	case <-archive.ready:
	case <-a.ctx.Done():
		return nil, !ok, errArchivesClosed
	}
	if archive.err != nil {
		return nil, !ok, archive.err
	}
	// Eviction may unlink the file later: an open file stays readable
	f, err = os.Open(archive.path)
	return f, !ok, err
}

func (a *imageArchives) build(tag string, archive *imageArchive) {
	defer close(archive.ready)

	archive.err = a.write(tag, archive)
	a.mu.Lock()
	if archive.err != nil {
		delete(a.entries, tag)
	} else {
		// Its TTL starts now, not when the build began: the transfers waiting for it are
		// about to open it
		archive.lastUsed = time.Now()
	}
	a.mu.Unlock()
	if archive.err != nil {
		return
	}
	a.log.Info("Image archive built", append([]any{"tag", tag}, archive.stats.logAttrs()...)...)
}

func (a *imageArchives) write(tag string, archive *imageArchive) error {
	stats := &archive.stats
	stats.start = time.Now()

	tmp, err := os.CreateTemp(a.dir, "*.partial")
	if err != nil {
		return fmt.Errorf("failed to create archive of '%s': %w", tag, err)
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name()) // fails harmlessly once renamed
	}()

	var saveStderr, zstdStderr bytes.Buffer
	saveCmd := a.save(a.ctx, tag)
	saveCmd.Stderr = &saveStderr
	stats.saved = meter{r: lo.Must(saveCmd.StdoutPipe())}

	zstdCmd := a.compress(a.ctx)
	zstdIn := lo.Must(zstdCmd.StdinPipe())
	zstdCmd.Stdout = tmp
	zstdCmd.Stderr = &zstdStderr

	if err := saveCmd.Start(); err != nil {
		return fmt.Errorf("failed to start docker save of '%s': %w", tag, err)
	}
	if err := zstdCmd.Start(); err != nil {
		_ = saveCmd.Process.Kill()
		_ = saveCmd.Wait()
		return fmt.Errorf("failed to start zstd for '%s': %w", tag, err)
	}
	// Copied here rather than by exec (zstdCmd.Stdin), whose copy zstd's Wait waits for: stuck
	// reading a docker save that stalls silently, it would keep zstd's exit from being seen
	copied := make(chan struct{})
	go func() {
		defer close(copied)
		_, _ = io.Copy(zstdIn, &stats.saved)
		_ = zstdIn.Close()
	}()
	zstdErr := zstdCmd.Wait()
	if zstdErr != nil {
		// Nothing reads docker save's output any more: it would block writing, or stall
		// silently with the copy reading it. Its exit ends the copy.
		_ = saveCmd.Process.Kill()
	}
	<-copied // docker save's Wait closes the pipe the copy reads
	saveErr := saveCmd.Wait()
	switch {
	case a.ctx.Err() != nil:
		return errArchivesClosed
	// Checked before docker save: it then only failed from the kill above
	case zstdErr != nil:
		return fmt.Errorf("failed to compress '%s' (output: %s): %w", tag, strings.TrimSpace(zstdStderr.String()), zstdErr)
	case saveErr != nil:
		return fmt.Errorf("failed to docker save '%s' (output: %s): %w", tag, strings.TrimSpace(saveStderr.String()), saveErr)
	}

	info, err := tmp.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat archive of '%s': %w", tag, err)
	}
	// Named after the temporary file, which is unique: a name derived from the tag could
	// be shared by two tags (team/app and team_app)
	path := strings.TrimSuffix(tmp.Name(), ".partial") + ".tar.zst"
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("failed to store archive of '%s': %w", tag, err)
	}
	archive.path = path
	stats.bytes = info.Size()
	stats.end = time.Now()
	return nil
}

// evict deletes the built archives no transfer has asked for within the TTL. Call with mu held.
func (a *imageArchives) evict() {
	for tag, archive := range a.entries {
		select {
		case <-archive.ready:
		default:
			continue // still being built
		}
		if time.Since(archive.lastUsed) < a.ttl {
			continue
		}
		delete(a.entries, tag)
		if err := os.Remove(archive.path); err != nil {
			a.log.Warn("Failed to delete image archive", "tag", tag, "error", err)
		} else {
			a.log.Debug("Deleted unused image archive", "tag", tag)
		}
	}
}

// removeAll deletes every archive, for the provisioner's shutdown: it kills the builds, fails
// the opens waiting for them and any later one, and ends the sweep.
func (a *imageArchives) removeAll() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cancel()
	a.entries = make(map[string]*imageArchive)
	if err := os.RemoveAll(a.dir); err != nil {
		a.log.Warn("Failed to delete image archives", "dir", a.dir, "error", err)
	}
}
