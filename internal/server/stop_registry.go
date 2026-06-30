package server

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	stopCommandStoppedReply = "已停止当前子进程组。"
	stopCommandIdleReply    = "没有正在运行的子进程组。"
)

func isStopCommand(message string) bool {
	return strings.TrimSpace(message) == "/stop"
}

type processGroupStopRegistry struct {
	mu      sync.Mutex
	nextID  atomic.Uint64
	entries map[uint64]processGroupStopEntry
	byKey   map[string]map[uint64]struct{}
}

type processGroupStopEntry struct {
	keys    []string
	command string
	cancel  context.CancelFunc
}

func newProcessGroupStopRegistry() *processGroupStopRegistry {
	return &processGroupStopRegistry{
		entries: make(map[uint64]processGroupStopEntry),
		byKey:   make(map[string]map[uint64]struct{}),
	}
}

func stopPeerKey(peerKey string) string {
	peerKey = strings.TrimSpace(peerKey)
	if peerKey == "" {
		return ""
	}
	return "peer:" + peerKey
}

func stopSessionKey(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	return "session:" + sessionID
}

func stopKeys(peerKey string, sessionID string) []string {
	keys := make([]string, 0, 2)
	seen := make(map[string]struct{}, 2)
	for _, key := range []string{stopPeerKey(peerKey), stopSessionKey(sessionID)} {
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}

func (registry *processGroupStopRegistry) Register(keys []string, command string, cancel context.CancelFunc) func() {
	if registry == nil || cancel == nil {
		return func() {}
	}
	cleanKeys := compactStopKeys(keys)
	if len(cleanKeys) == 0 {
		return func() {}
	}
	id := registry.nextID.Add(1)
	registry.mu.Lock()
	registry.entries[id] = processGroupStopEntry{keys: cleanKeys, command: strings.TrimSpace(command), cancel: cancel}
	for _, key := range cleanKeys {
		ids := registry.byKey[key]
		if ids == nil {
			ids = make(map[uint64]struct{})
			registry.byKey[key] = ids
		}
		ids[id] = struct{}{}
	}
	registry.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() { registry.unregister(id) })
	}
}

func (registry *processGroupStopRegistry) Stop(keys []string) int {
	if registry == nil {
		return 0
	}
	cleanKeys := compactStopKeys(keys)
	if len(cleanKeys) == 0 {
		return 0
	}
	registry.mu.Lock()
	entries := make(map[uint64]processGroupStopEntry)
	for _, key := range cleanKeys {
		for id := range registry.byKey[key] {
			if entry, ok := registry.entries[id]; ok {
				entries[id] = entry
			}
		}
	}
	registry.mu.Unlock()
	for _, entry := range entries {
		entry.cancel()
	}
	return len(entries)
}

func (registry *processGroupStopRegistry) unregister(id uint64) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	entry, ok := registry.entries[id]
	if !ok {
		return
	}
	delete(registry.entries, id)
	for _, key := range entry.keys {
		ids := registry.byKey[key]
		delete(ids, id)
		if len(ids) == 0 {
			delete(registry.byKey, key)
		}
	}
}

func compactStopKeys(keys []string) []string {
	clean := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		clean = append(clean, key)
	}
	return clean
}
