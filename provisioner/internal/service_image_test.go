package internal

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/api/types/image"
)

// pullingDocker is a daemon that has the image once a pull has completed. Its pulls take
// 50ms and answer progress, which ends with an error message while failures > 0.
type pullingDocker struct {
	DockerClient // unused methods
	pulls        atomic.Int32
	failures     atomic.Int32
	present      atomic.Bool
}

func (d *pullingDocker) ImageList(context.Context, image.ListOptions) ([]image.Summary, error) {
	if d.present.Load() {
		return []image.Summary{{}}, nil
	}
	return nil, nil
}

func (d *pullingDocker) ImagePull(context.Context, string, image.PullOptions) (io.ReadCloser, error) {
	d.pulls.Add(1)
	time.Sleep(50 * time.Millisecond)
	progress := `{"status":"Pulling from library/mysql"}` + "\n"
	if d.failures.Add(-1) >= 0 {
		progress += `{"errorDetail":{"message":"toomanyrequests: You have reached your pull rate limit"},"error":"toomanyrequests"}` + "\n"
	} else {
		progress += `{"status":"Status: Downloaded newer image for mysql:8"}` + "\n"
		d.present.Store(true)
	}
	return io.NopCloser(strings.NewReader(progress)), nil
}

func ensureConcurrently(t *testing.T, n int, docker func(i int) DockerClient) []error {
	t.Helper()
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = ensureServiceImage(context.Background(), docker(i), slog.New(slog.NewTextHandler(io.Discard, nil)), "mysql:8")
		}()
	}
	wg.Wait()
	return errs
}

func TestEnsureServiceImagePullsOncePerDaemon(t *testing.T) {
	d := &pullingDocker{}
	for i, err := range ensureConcurrently(t, 10, func(int) DockerClient { return d }) {
		if err != nil {
			t.Errorf("task %d: %v", i, err)
		}
	}
	if d.pulls.Load() != 1 {
		t.Errorf("10 tasks on one daemon pulled %d times, want 1", d.pulls.Load())
	}
}

func TestEnsureServiceImageDoesNotSerializeDaemons(t *testing.T) {
	daemons := []*pullingDocker{{}, {}, {}}
	start := time.Now()
	ensureConcurrently(t, len(daemons), func(i int) DockerClient { return daemons[i] })
	for i, d := range daemons {
		if d.pulls.Load() != 1 {
			t.Errorf("daemon %d pulled %d times, want 1", i, d.pulls.Load())
		}
	}
	if elapsed := time.Since(start); elapsed > 140*time.Millisecond {
		t.Errorf("3 daemons took %s to pull, as if one after another (50ms each)", elapsed)
	}
}

func TestEnsureServiceImageReportsAFailedPull(t *testing.T) {
	d := &pullingDocker{}
	d.failures.Store(100)
	err := ensureConcurrently(t, 1, func(int) DockerClient { return d })[0]
	if err == nil || !strings.Contains(err.Error(), "pull rate limit") {
		t.Fatalf("err = %v, want the pull's error message", err)
	}
	if d.pulls.Load() != 4 {
		t.Errorf("pulled %d times, want 4 attempts", d.pulls.Load())
	}
}

func TestEnsureServiceImageRetriesAFailedPull(t *testing.T) {
	d := &pullingDocker{}
	d.failures.Store(1)
	if err := ensureConcurrently(t, 1, func(int) DockerClient { return d })[0]; err != nil {
		t.Fatal(err)
	}
	if d.pulls.Load() != 2 || !d.present.Load() {
		t.Errorf("pulled %d times (present: %v), want a second attempt that succeeds", d.pulls.Load(), d.present.Load())
	}
}

func TestPullErrorReadsTheWholeProgress(t *testing.T) {
	for name, tc := range map[string]struct {
		progress string
		want     string
	}{
		"success":    {`{"status":"a"}{"status":"b"}`, ""},
		"error only": {`{"status":"a"}{"error":"manifest unknown"}`, "manifest unknown"},
		"detail":     {`{"errorDetail":{"message":"detailed"},"error":"short"}`, "detailed"},
		"cut":        {`{"status":"a"`, "failed to read the pull's progress"},
		"empty":      {``, ""},
	} {
		t.Run(name, func(t *testing.T) {
			err := pullError(strings.NewReader(tc.progress))
			if (err == nil) != (tc.want == "") || (err != nil && !strings.Contains(err.Error(), tc.want)) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestEnsureServiceImageStopsWaitingWhenTheTaskIsAborted(t *testing.T) {
	d := &pullingDocker{}
	key := servicePullKey{d, "mysql:8"}
	unlock := servicePulls.Lock(key) // another task's pull, in progress
	defer unlock()

	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() {
		errs <- ensureServiceImage(ctx, d, slog.New(slog.NewTextHandler(io.Discard, nil)), "mysql:8")
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-errs:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("an aborted task kept waiting for another task's pull")
	}
}
