package internal

import (
	"context"
	"sync"
)

// KeyedMutex is one mutex per key, created on first use and dropped once no caller holds or
// waits for it. The zero value is ready to use.
type KeyedMutex struct {
	mu    sync.Mutex
	locks map[any]*keyedLock
}

type keyedLock struct {
	token chan struct{} // holds one value while locked: a channel, so that waiting can be canceled
	refs  int           // callers holding or waiting for it, protected by KeyedMutex.mu
}

// Lock locks the key's mutex and returns the function that unlocks it. The key must be
// comparable.
func (k *KeyedMutex) Lock(key any) (unlock func()) {
	unlock, _ = k.LockContext(context.Background(), key)
	return unlock
}

// LockContext is Lock, but gives up waiting once ctx is done, and then returns its error.
func (k *KeyedMutex) LockContext(ctx context.Context, key any) (unlock func(), err error) {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = make(map[any]*keyedLock)
	}
	l, ok := k.locks[key]
	if !ok {
		l = &keyedLock{token: make(chan struct{}, 1)}
		k.locks[key] = l
	}
	l.refs++
	k.mu.Unlock()

	release := func() {
		k.mu.Lock()
		if l.refs--; l.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}

	select {
	case l.token <- struct{}{}:
		return func() {
			<-l.token
			release()
		}, nil
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
}

// len returns how many keys have a mutex, for tests.
func (k *KeyedMutex) len() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.locks)
}
