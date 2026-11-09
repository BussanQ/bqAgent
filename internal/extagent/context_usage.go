package extagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"bqagent/internal/atomicfile"
	"bqagent/internal/privatefile"
	"bqagent/internal/session"
	"bqagent/internal/turn"
)

// Window == 0 invalidates a previous reading. Zero used tokens is a valid
// provider report only when a positive capacity is known.
type ContextUsage struct {
	Context    int               `json:"context"`
	Used       int64             `json:"used"`
	Window     int64             `json:"window"`
	Estimated  bool              `json:"estimated,omitempty"`
	Summary    bool              `json:"summary,omitempty"`
	Model      string            `json:"model,omitempty"`
	Captured   int64             `json:"captured"`
	Categories []ContextCategory `json:"categories,omitempty"`
	CompactAt  *int64            `json:"compact_at,omitempty"`
	Percentage *float64          `json:"percentage,omitempty"`
}

type ContextCategory struct {
	Name     string `json:"name"`
	Tokens   int64  `json:"tokens"`
	Deferred bool   `json:"deferred,omitempty"`
}

type contextUsageRecord struct {
	Owner     string        `json:"owner,omitempty"`
	Native    string        `json:"native"`
	Transport TransportKind `json:"transport"`
	Selected  string        `json:"selected,omitempty"`
	Usage     ContextUsage  `json:"usage"`
}

type usageSink func(string, ContextUsage)
type usageKey struct{}
type idleUsageKey struct{}

func (p appServerNotification) contextUsage() *ContextUsage {
	if p.TokenUsage == nil || p.TokenUsage.Last.TotalTokens <= 0 {
		return nil
	}
	if p.TokenUsage.ModelContextWindow <= 0 {
		return &ContextUsage{}
	}
	return &ContextUsage{Used: p.TokenUsage.Last.TotalTokens, Window: p.TokenUsage.ModelContextWindow}
}

func contextUsageSink(ctx context.Context) usageSink {
	sink, _ := ctx.Value(usageKey{}).(usageSink)
	return sink
}

func contextUsagePath(name AgentName) string {
	return filepath.Join("agent-context-usage", string(name)+".json")
}

func readUsageRecord(root *os.Root, path string) (contextUsageRecord, error) {
	var record contextUsageRecord
	file, err := root.Open(path)
	if err != nil {
		return record, err
	}
	defer file.Close()
	err = json.NewDecoder(io.LimitReader(file, 256<<10)).Decode(&record)
	return record, err
}

func (broker *Broker) readContextUsage(state SessionState) *ContextUsage {
	if state.ExternalSessionID == "" || broker.store.sessions == nil || !slices.Contains(SupportedAgents(), state.Agent) {
		return nil
	}
	s, err := broker.store.sessions.Open(state.BQSessionID)
	if err != nil {
		return nil
	}
	root, err := os.OpenRoot(s.ContextDir())
	if err != nil {
		return nil
	}
	defer root.Close()
	r, err := readUsageRecord(root, contextUsagePath(state.Agent))
	if err != nil || r.Native != state.ExternalSessionID || r.Transport != state.Transport || r.Selected != state.Model || r.Usage.Captured <= 0 || r.Usage.Context != s.ContextNumber() {
		return nil
	}
	return &r.Usage
}

// SavedContextUsage only reads the current binding. Polling an idle composer
// must not start an adapter or probe the model catalog.
func (broker *Broker) SavedContextUsage(id string, name AgentName) *ContextUsage {
	if broker == nil || broker.store == nil || !slices.Contains(SupportedAgents(), name) {
		return nil
	}
	state, err := broker.store.stateForAgent(id, name)
	if err != nil {
		return nil
	}
	return broker.readContextUsage(state)
}

// Compare the owner and replace its snapshot under a per-agent file lock.
// Opening the existing context as a root prevents late reports from recreating
// a deleted conversation. Claims from newer turns retire older native callbacks.
func updateUsageRecord(dir string, name AgentName, update func(contextUsageRecord) (contextUsageRecord, bool)) error {
	if dir == "" || !slices.Contains(SupportedAgents(), name) {
		return nil
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	path := contextUsagePath(name)
	if err := root.MkdirAll(filepath.Dir(path), privatefile.DirMode); err != nil {
		return err
	}
	lock, err := root.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, privatefile.FileMode)
	if err != nil {
		return err
	}
	defer lock.Close()
	deadline := time.Now().Add(time.Second)
	for {
		err = session.TryLockFile(lock, true)
		if err == nil {
			break
		}
		if !errors.Is(err, session.ErrContextBusy) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(5 * time.Millisecond)
	}
	prev, readErr := readUsageRecord(root, path)
	var pathErr *os.PathError
	if errors.As(readErr, &pathErr) && !os.IsNotExist(readErr) {
		return readErr
	}
	next, ok := update(prev)
	if !ok {
		return nil
	}
	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	return atomicfile.WriteRoot(root, path, data, privatefile.FileMode)
}

// Keep telemetry out of prompts and transcripts. The context directory also
// fences forks, rewinds and /clear, including reverts retaining a native ID.
func trackContextUsage(ctx context.Context, dir string, epoch int, state SessionState, selected string, emit func(ContextUsage)) (context.Context, func()) {
	var mu sync.Mutex
	var closed bool
	owner := turn.NewID()
	base := contextUsageRecord{Owner: owner, Native: state.ExternalSessionID, Transport: state.Transport, Selected: selected}
	var last int64
	save := func(update func(contextUsageRecord) (contextUsageRecord, bool)) {
		if err := updateUsageRecord(dir, state.Agent, update); err != nil && !os.IsNotExist(err) {
			log.Printf("save %s context usage: %v", state.Agent, err)
		}
	}
	save(func(prev contextUsageRecord) (contextUsageRecord, bool) {
		if prev.Native == base.Native && prev.Selected == base.Selected && prev.Transport == base.Transport {
			base.Usage = prev.Usage
			last = prev.Usage.Captured
		}
		return base, true
	})
	publish := func(id string, u ContextUsage, idle bool) {
		if id == "" || u.Used < 0 || u.Window < 0 {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if closed && !idle {
			return
		}
		if u.Captured > 0 {
			if u.Captured <= last {
				return
			}
		} else {
			u.Captured = max(time.Now().UnixMicro(), last+1)
		}
		last = u.Captured
		u.Context = epoch
		save(func(prev contextUsageRecord) (contextUsageRecord, bool) {
			if prev.Owner != owner || idle && prev.Native != id {
				return prev, false
			}
			prev.Native, prev.Usage = id, u
			// Pi reports its effective provider/model on the bound session.
			// A native command can change it after the turn started.
			if state.Agent == AgentPi && state.Transport == TransportCLI && u.Model != "" {
				prev.Selected = u.Model
			}
			return prev, true
		})
		if !closed && emit != nil {
			emit(u)
		}
	}
	record := usageSink(func(id string, u ContextUsage) { publish(id, u, false) })
	ctx = context.WithValue(ctx, usageKey{}, record)
	if state.Agent == AgentCodex && state.Transport == TransportAppServer {
		ctx = context.WithValue(ctx, idleUsageKey{}, usageSink(func(id string, u ContextUsage) { publish(id, u, true) }))
	}
	activity := ActivitySinkFromContext(ctx)
	ctx = context.WithValue(ctx, activityContextKey{}, ActivitySink(func(event Activity) {
		if event.ContextUsage != nil {
			record(event.NativeSession, *event.ContextUsage)
		} else if activity != nil {
			activity(event)
		}
	}))
	if state.ExternalSessionID != "" && state.Model != selected {
		record(state.ExternalSessionID, ContextUsage{})
	}
	return ctx, func() { mu.Lock(); closed = true; emit = nil; mu.Unlock() }
}
