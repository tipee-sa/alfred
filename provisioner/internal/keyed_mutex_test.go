package internal

import (
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
