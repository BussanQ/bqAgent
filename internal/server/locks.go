package server

import "sync"

type KeyedLocker struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func NewKeyedLocker() *KeyedLocker {
	return &KeyedLocker{locks: make(map[string]*sync.Mutex)}
}

func (locker *KeyedLocker) Lock(key string) func() {
	if key == "" {
		return func() {}
	}

	mutex := locker.mutexFor(key)
	mutex.Lock()
	return mutex.Unlock
}

func (locker *KeyedLocker) TryLock(key string) (func(), bool) {
	if key == "" {
		return func() {}, true
	}

	mutex := locker.mutexFor(key)
	if !mutex.TryLock() {
		return nil, false
	}
	return mutex.Unlock, true
}

func (locker *KeyedLocker) mutexFor(key string) *sync.Mutex {
	locker.mu.Lock()
	defer locker.mu.Unlock()
	mutex, ok := locker.locks[key]
	if !ok {
		mutex = &sync.Mutex{}
		locker.locks[key] = mutex
	}
	return mutex
}
