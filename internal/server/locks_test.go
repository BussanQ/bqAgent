package server

import (
	"sync"
	"testing"
)

func TestKeyedLockerReleasesEntriesAfterUnlock(t *testing.T) {
	locker := NewKeyedLocker()

	unlock := locker.Lock("peer-1")
	if size := lockerSize(locker); size != 1 {
		t.Fatalf("locker size while held = %d, want 1", size)
	}
	unlock()
	if size := lockerSize(locker); size != 0 {
		t.Fatalf("locker size after unlock = %d, want 0", size)
	}

	unlock, locked := locker.TryLock("peer-2")
	if !locked {
		t.Fatal("TryLock() = false, want true")
	}
	if _, lockedAgain := locker.TryLock("peer-2"); lockedAgain {
		t.Fatal("second TryLock() = true, want false")
	}
	if size := lockerSize(locker); size != 1 {
		t.Fatalf("locker size after failed TryLock = %d, want 1", size)
	}
	unlock()
	if size := lockerSize(locker); size != 0 {
		t.Fatalf("locker size after final unlock = %d, want 0", size)
	}
}

func TestKeyedLockerKeepsMutualExclusionPerKey(t *testing.T) {
	locker := NewKeyedLocker()
	counter := 0
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := locker.Lock("shared")
			defer unlock()
			counter++
		}()
	}
	wg.Wait()
	if counter != 50 {
		t.Fatalf("counter = %d, want 50", counter)
	}
	if size := lockerSize(locker); size != 0 {
		t.Fatalf("locker size after all unlocks = %d, want 0", size)
	}
}

func lockerSize(locker *KeyedLocker) int {
	locker.mu.Lock()
	defer locker.mu.Unlock()
	return len(locker.locks)
}
