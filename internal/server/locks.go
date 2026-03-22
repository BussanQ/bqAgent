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

	locker.mu.Lock()
	mutex, ok := locker.locks[key]
	if !ok {
		mutex = &sync.Mutex{}
		locker.locks[key] = mutex
	}
	locker.mu.Unlock()

	mutex.Lock()
	return mutex.Unlock
}
