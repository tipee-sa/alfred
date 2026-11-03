package internal

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestKeyedMutexSerializesAKey(t *testing.T) {
	var k KeyedMutex
	var inside, maxInside atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer k.Lock("image")()
			n := inside.Add(1)
			for m := maxInside.Load(); n > m && !maxInside.CompareAndSwap(m, n); m = maxInside.Load() {
			}
			time.Sleep(5 * time.Millisecond)
			inside.Add(-1)
		}()
	}
	wg.Wait()
	if maxInside.Load() != 1 {
		t.Errorf("%d callers held the same key at once, want 1", maxInside.Load())
	}
	if k.len() != 0 {
		t.Errorf("%d mutexes left once unlocked, want 0", k.len())
	}
}

func TestKeyedMutexLeavesOtherKeysFree(t *testing.T) {
	var k KeyedMutex
	unlock := k.Lock("a")
	defer unlock()

	done := make(chan struct{})
	go func() {
		k.Lock("b")()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("key b waited for key a")
	}
}

func TestKeyedMutexStopsWaitingWhenCanceled(t *testing.T) {
	var k KeyedMutex
	unlock := k.Lock("image")

	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() {
		_, err := k.LockContext(ctx, "image")
		errs <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-errs:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("a canceled waiter kept waiting for the lock")
	}

	// The holder is undisturbed, and once it unlocks, nothing is left behind
	unlock()
	if k.len() != 0 {
		t.Errorf("%d mutexes left, want 0", k.len())
	}
	if _, err := k.LockContext(context.Background(), "image"); err != nil {
		t.Errorf("the key could not be locked again: %v", err)
	}
}
