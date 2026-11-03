package openstack

import (
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testArchives returns archives whose "docker save" runs script, counting its runs.
func testArchives(t *testing.T, ttl time.Duration, script string) (*imageArchives, *atomic.Int32) {
	t.Helper()
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd not installed")
	}
	a, err := newImageArchives(filepath.Join(t.TempDir(), "archives"), ttl, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	var saves atomic.Int32
	a.save = func(tag string) *exec.Cmd {
		saves.Add(1)
		return exec.Command("sh", "-c", script, "sh", tag)
	}
	return a, &saves
}

// decompress returns what the archive holds.
func decompress(t *testing.T, f *os.File) string {
	t.Helper()
	defer f.Close()
	cmd := exec.Command("zstd", "--decompress", "-q", "-c")
	cmd.Stdin = f
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestImageArchivesStartEmpty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "archives")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "left-behind.partial"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newImageArchives(dir, time.Hour, slog.Default()); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("dir holds %d files after startup, want none", len(entries))
	}
}

func TestImageArchivesConcurrentOpensShareOneSave(t *testing.T) {
	a, saves := testArchives(t, time.Hour, `sleep 0.2; printf "image %s" "$1"`)

	var wg sync.WaitGroup
	var built atomic.Int32
	contents := make([]string, 10)
	for i := range contents {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			f, b, err := a.open("alfred-image:abc")
			if err != nil {
				t.Error(err)
				return
			}
			if b {
				built.Add(1)
			}
			contents[i] = decompress(t, f)
		}(i)
	}
	wg.Wait()

	if saves.Load() != 1 || built.Load() != 1 {
		t.Errorf("%d saves, %d opens built; want 1 and 1", saves.Load(), built.Load())
	}
	for i, c := range contents {
		if c != "image alfred-image:abc" {
			t.Errorf("open %d read %q", i, c)
		}
	}

	// Later opens reuse it too
	f, b, err := a.open("alfred-image:abc")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if b || saves.Load() != 1 {
		t.Errorf("a later open rebuilt the archive (built %v, %d saves)", b, saves.Load())
	}
}

func TestImageArchivesRetryAFailedBuild(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "failed-once")
	// Fails the first time, works after
	a, saves := testArchives(t, time.Hour, `if [ ! -e "`+marker+`" ]; then touch "`+marker+`"; echo "no such image" >&2; exit 1; fi; printf ok`)

	if _, _, err := a.open("alfred-image:abc"); err == nil {
		t.Fatal("open succeeded although docker save failed")
	}
	f, built, err := a.open("alfred-image:abc")
	if err != nil {
		t.Fatal(err)
	}
	if got := decompress(t, f); !built || got != "ok" || saves.Load() != 2 {
		t.Errorf("after a failed build: built %v, read %q, %d saves; want a rebuild reading \"ok\"", built, got, saves.Load())
	}
	if entries, _ := os.ReadDir(a.dir); len(entries) != 1 {
		t.Errorf("dir holds %d files, want only the archive (no partial left by the failure)", len(entries))
	}
}

func TestImageArchivesEvictUnusedOnes(t *testing.T) {
	a, saves := testArchives(t, 50*time.Millisecond, `printf "%s" "$1"`)

	f, _, err := a.open("alfred-image:old")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)

	// Opening another image evicts the old one, which an open file still reads
	g, _, err := a.open("alfred-image:new")
	if err != nil {
		t.Fatal(err)
	}
	g.Close()
	if got := decompress(t, f); got != "alfred-image:old" {
		t.Errorf("the open file of an evicted archive read %q", got)
	}
	if entries, _ := os.ReadDir(a.dir); len(entries) != 1 {
		t.Errorf("dir holds %d files, want only the new archive", len(entries))
	}

	f, built, err := a.open("alfred-image:old")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if !built || saves.Load() != 3 {
		t.Errorf("an evicted archive was not rebuilt (built %v, %d saves)", built, saves.Load())
	}
}
