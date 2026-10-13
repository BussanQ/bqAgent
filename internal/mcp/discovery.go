package mcp

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	"bqagent/internal/tools"
)

// Discovery publishes immutable tool catalogs as servers finish connecting.
// A turn retains its own snapshot, so background updates cannot change the
// definitions/function bindings of an in-flight model request.
type Discovery struct {
	mu          sync.RWMutex
	definitions []tools.Definition
	functions   map[string]tools.Function
	statuses    map[string]ServerStatus
	done        chan struct{}
}

func StartDiscovery(ctx context.Context, cfg Config, getenv func(string) string, client *http.Client, logf Logf, report func(ServerStatus), timeout time.Duration) *Discovery {
	discovery := &Discovery{statuses: make(map[string]ServerStatus), done: make(chan struct{})}
	record := func(status ServerStatus) {
		discovery.mu.Lock()
		discovery.statuses[status.Name] = status
		discovery.mu.Unlock()
		if report != nil {
			report(status)
		}
	}
	// Seed before returning so the first UI frame already shows connecting.
	for _, status := range cfg.Status(getenv) {
		if status.State == "unverified" {
			status.State, status.Reason = "connecting", "discovery_in_progress"
		}
		record(status)
	}
	go func() {
		defer close(discovery.done)
		if timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		discover(ctx, cfg, getenv, client, logf, record, 0, func(definitions []tools.Definition, functions map[string]tools.Function) {
			discovery.mu.Lock()
			discovery.definitions, discovery.functions = definitions, functions
			discovery.mu.Unlock()
		})
	}()
	return discovery
}

func (discovery *Discovery) Snapshot() ([]tools.Definition, map[string]tools.Function) {
	if discovery == nil {
		return nil, nil
	}
	discovery.mu.RLock()
	defer discovery.mu.RUnlock()
	return slices.Clone(discovery.definitions), maps.Clone(discovery.functions)
}

func (discovery *Discovery) Status() []ServerStatus {
	if discovery == nil {
		return nil
	}
	discovery.mu.RLock()
	defer discovery.mu.RUnlock()
	result := make([]ServerStatus, 0, len(discovery.statuses))
	for _, name := range sortedMapKeys(discovery.statuses) {
		result = append(result, discovery.statuses[name])
	}
	return result
}

func (discovery *Discovery) Done() <-chan struct{} { return discovery.done }
