package server

import "sync"

type KeyedLocker struct {
	mu    sync.Mutex
	locks map[string]*lockEntry
}

type lockEntry struct {
	mu   sync.Mutex
	refs int
}

func NewKeyedLocker() *KeyedLocker {
	return &KeyedLocker{locks: make(map[string]*lockEntry)}
}

func (locker *KeyedLocker) Lock(key string) func() {
	if key == "" {
		return func() {}
	}

	entry := locker.acquireEntry(key)
	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		locker.releaseEntry(key, entry)
	}
}

func (locker *KeyedLocker) TryLock(key string) (func(), bool) {
	if key == "" {
		return func() {}, true
	}

	entry := locker.acquireEntry(key)
	if !entry.mu.TryLock() {
		locker.releaseEntry(key, entry)
		return nil, false
	}
	return func() {
		entry.mu.Unlock()
		locker.releaseEntry(key, entry)
	}, true
}

func (locker *KeyedLocker) acquireEntry(key string) *lockEntry {
	locker.mu.Lock()
	defer locker.mu.Unlock()
	entry, ok := locker.locks[key]
	if !ok {
		entry = &lockEntry{}
		locker.locks[key] = entry
	}
	entry.refs++
	return entry
}

func (locker *KeyedLocker) releaseEntry(key string, entry *lockEntry) {
	locker.mu.Lock()
	defer locker.mu.Unlock()
	entry.refs--
	if entry.refs <= 0 {
		delete(locker.locks, key)
	}
}
