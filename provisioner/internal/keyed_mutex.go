package internal

import "sync"

// KeyedMutex is one mutex per key, created on first use and dropped once no caller holds or
// waits for it. The zero value is ready to use.
type KeyedMutex struct {
	mu    sync.Mutex
	locks map[any]*keyedLock
}

type keyedLock struct {
	sync.Mutex
	refs int // callers holding or waiting for it, protected by KeyedMutex.mu
}

// Lock locks the key's mutex and returns the function that unlocks it. The key must be
// comparable.
func (k *KeyedMutex) Lock(key any) (unlock func()) {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = make(map[any]*keyedLock)
	}
	l, ok := k.locks[key]
	if !ok {
		l = &keyedLock{}
		k.locks[key] = l
	}
	l.refs++
	k.mu.Unlock()

	l.Lock()
	return func() {
		l.Unlock()
		k.mu.Lock()
		if l.refs--; l.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}

// len returns how many keys have a mutex, for tests.
func (k *KeyedMutex) len() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.locks)
}
