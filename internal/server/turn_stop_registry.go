package server

import (
	"context"
	"errors"
	"strings"
	"sync"

	"bqagent/internal/turn"
)

// ErrTurnNotFound means no active turn has the requested id.
var ErrTurnNotFound = errors.New("turn not found")

type activeTurnEntry struct {
	cancel context.CancelFunc
	inputs *turn.InputQueue
}

type activeTurnRegistry struct {
	mu    sync.Mutex
	turns map[string]activeTurnEntry
}

func newActiveTurnRegistry() *activeTurnRegistry {
	return &activeTurnRegistry{turns: make(map[string]activeTurnEntry)}
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
	return registry.RegisterWithInputs(turnID, cancel, nil)
}

// RegisterWithInputs additionally exposes the turn's input queue to AddInput.
func (registry *activeTurnRegistry) RegisterWithInputs(turnID string, cancel context.CancelFunc, inputs *turn.InputQueue) (func(), bool) {
	turnID = strings.TrimSpace(turnID)
	if registry == nil || !validTurnID(turnID) || cancel == nil {
		return func() {}, false
	}
	registry.mu.Lock()
	if registry.turns == nil {
		registry.turns = make(map[string]activeTurnEntry)
	}
	if _, exists := registry.turns[turnID]; exists {
		registry.mu.Unlock()
		return func() {}, false
	}
	registry.turns[turnID] = activeTurnEntry{cancel: cancel, inputs: inputs}
	registry.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			registry.mu.Lock()
			delete(registry.turns, turnID)
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
	entry := registry.turns[turnID]
	registry.mu.Unlock()
	if entry.cancel == nil {
		return false
	}
	// Close first so a canceled turn immediately reports closed instead of
	// accepting input nobody will consume; Close keeps the pending copy.
	if entry.inputs != nil {
		entry.inputs.Close()
	}
	entry.cancel()
	return true
}

// AddInput queues input without taking any session lock. It returns
// ErrTurnNotFound for unknown turns and turn.ErrInputClosed once the turn has
// stopped accepting input.
func (registry *activeTurnRegistry) AddInput(turnID string, input turn.Input) error {
	turnID = strings.TrimSpace(turnID)
	if registry == nil || !validTurnID(turnID) {
		return ErrTurnNotFound
	}
	registry.mu.Lock()
	queue := registry.turns[turnID].inputs
	registry.mu.Unlock()
	if queue == nil {
		return ErrTurnNotFound
	}
	return queue.Add(input)
}
