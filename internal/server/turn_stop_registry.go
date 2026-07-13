package server

import (
	"context"
	"strings"
	"sync"
)

type activeTurnRegistry struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func newActiveTurnRegistry() *activeTurnRegistry {
	return &activeTurnRegistry{cancels: make(map[string]context.CancelFunc)}
}

func validTurnID(turnID string) bool {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" || len(turnID) > 128 {
		return false
	}
	for _, char := range turnID {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func (registry *activeTurnRegistry) Register(turnID string, cancel context.CancelFunc) (func(), bool) {
	turnID = strings.TrimSpace(turnID)
	if registry == nil || !validTurnID(turnID) || cancel == nil {
		return func() {}, false
	}
	registry.mu.Lock()
	if registry.cancels == nil {
		registry.cancels = make(map[string]context.CancelFunc)
	}
	if _, exists := registry.cancels[turnID]; exists {
		registry.mu.Unlock()
		return func() {}, false
	}
	registry.cancels[turnID] = cancel
	registry.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			registry.mu.Lock()
			delete(registry.cancels, turnID)
			registry.mu.Unlock()
		})
	}, true
}

func (registry *activeTurnRegistry) Stop(turnID string) bool {
	turnID = strings.TrimSpace(turnID)
	if registry == nil || !validTurnID(turnID) {
		return false
	}
	registry.mu.Lock()
	cancel := registry.cancels[turnID]
	registry.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}
