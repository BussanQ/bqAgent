package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"bqagent/internal/extagent"
	"bqagent/internal/session"
	"bqagent/internal/transcript"
	"bqagent/internal/turn"
)

const replayMemoryBytes = 512 << 10
const completedReplayLifetime = 10 * time.Minute

// Each workspace Service owns its live browser turns. Only the server context
// and an explicit stop cancel the producer. Browsers are independent readers.
type webTurnRegistry struct {
	queueMu     sync.Mutex
	queueLeases map[string]*queueLease
	rewinding   map[string]bool // protected by queueMu
	queueOwner  string
	mu          sync.Mutex
	turns       map[string]*webTurn
}

func (r *webTurnRegistry) start(ctx context.Context, id, sessionID string) (*webTurn, context.Context, bool) {
	return r.startCommand(ctx, id, sessionID, "")
}

func (r *webTurnRegistry) startCommand(ctx context.Context, id, sessionID, commandID string) (*webTurn, context.Context, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.turns[id] != nil {
		return nil, nil, false
	}
	if r.turns == nil {
		r.turns = make(map[string]*webTurn)
	}
	runCtx, cancel := context.WithCancel(ctx)
	j := &webTurn{id: id, commandID: commandID, sessionID: sessionID, cancel: cancel, changed: make(chan struct{}), bound: make(chan struct{}), ends: []int64{0}}
	r.turns[id] = j
	return j, runCtx, true
}

func (r *webTurnRegistry) get(id string) *webTurn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.turns[id]
}

func (r *webTurnRegistry) active(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	return r.runningSessions()[sessionID]
}

func (r *webTurnRegistry) runningSessions() map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	active := make(map[string]string)
	for _, j := range r.turns {
		j.mu.Lock()
		if !j.terminal && j.sessionID != "" && (j.hasSession || active[j.sessionID] == "") {
			active[j.sessionID] = j.id
		}
		j.mu.Unlock()
	}
	return active
}

func (r *webTurnRegistry) expire(j *webTurn) {
	r.mu.Lock()
	if r.turns[j.id] != j {
		r.mu.Unlock()
		return
	}
	delete(r.turns, j.id)
	r.mu.Unlock()
	j.dispose()
}

func (r *webTurnRegistry) forgetSession(id string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	var removed []*webTurn
	for key, j := range r.turns {
		j.mu.Lock()
		match := j.sessionID == id
		j.mu.Unlock()
		if match {
			delete(r.turns, key)
			removed = append(removed, j)
		}
	}
	r.mu.Unlock()
	for _, j := range removed {
		j.cancel()
		j.dispose()
	}
}

type webTurn struct {
	baseAt                        time.Time
	inputTarget                   extagent.SessionState // protected by queueMu
	steerTail                     chan struct{}         // preserves acceptance order across asynchronous provider writes
	commandID                     string                // immutable from registration; set only for queued commands
	freshAfterPause               bool
	pauseAt                       uint64
	queueResumed                  bool
	stopRequested                 bool // protected by queueMu
	id                            string
	cancel                        context.CancelFunc
	mu                            sync.Mutex
	sessionID                     string
	hasSession, terminal, expired bool
	changed, bound                chan struct{}
	boundOnce                     sync.Once
	ends                          []int64
	memory                        []byte
	file                          *os.File
	spoolPath                     string
	lastError                     []byte
	snapshots                     webSnapshotState

	baseMu       sync.RWMutex
	baseAgent    extagent.AgentName
	baseSession  *session.Session
	baseFile     *os.File
	baseIdentity os.FileInfo
	baseSize     int64
	baseErr      error
}

func (j *webTurn) bind(svc *Service, id string) {
	j.baseMu.Lock()
	j.mu.Lock()
	expired := j.expired
	j.mu.Unlock()
	if expired {
		j.baseMu.Unlock()
		return
	}
	s, err := svc.store.Open(id)
	if err == nil {
		j.baseSession = s
		j.baseAt = time.Now().UTC()
		j.baseAgent, err = svc.externalBroker.ActiveAgent(id)
		file, openErr := os.Open(s.MessagesPath())
		if openErr == nil {
			stat, statErr := file.Stat()
			if statErr == nil {
				j.baseFile, j.baseSize, j.baseIdentity = file, stat.Size(), stat
			} else {
				file.Close()
				err = statErr
			}
		} else if !errors.Is(openErr, os.ErrNotExist) {
			err = openErr
		}
	}
	j.baseErr = err
	j.baseMu.Unlock()
	j.mu.Lock()
	j.sessionID, j.hasSession = id, true
	j.boundOnce.Do(func() { close(j.bound) })
	j.mu.Unlock()
	_ = j.publish("turn_session", map[string]string{"session_id": id})
}

// An open file and a length pin the pre-turn transcript across appends and
// atomic rewrites. Reading history never waits for a running agent's lock.
func (j *webTurn) history(ctx context.Context, svc *Service) (map[string]any, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-j.bound:
	}
	j.baseMu.RLock()
	defer j.baseMu.RUnlock()
	j.mu.Lock()
	expired := j.expired
	j.mu.Unlock()
	if expired {
		return nil, os.ErrNotExist
	}
	if j.baseErr != nil {
		return nil, j.baseErr
	}
	if j.baseSession == nil {
		return nil, nil
	}
	var records transcript.Records
	if j.baseSize > 0 {
		file := j.baseFile
		if file == nil {
			var err error
			file, err = os.Open(j.baseSession.MessagesPath())
			if err != nil {
				return nil, err
			}
			defer file.Close()
			stat, err := file.Stat()
			if err != nil {
				return nil, err
			}
			if !os.SameFile(j.baseIdentity, stat) || stat.Size() < j.baseSize {
				return nil, os.ErrNotExist
			}
		}
		reader := bufio.NewReader(io.NewSectionReader(file, 0, j.baseSize))
		for offset := int64(0); offset < j.baseSize; {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			line, err := reader.ReadBytes('\n')
			if err != nil && err != io.EOF {
				return nil, err
			}
			var record map[string]any
			if err := json.Unmarshal(line, &record); err != nil {
				return nil, err
			}
			records.Add(record, offset)
			offset += int64(len(line))
		}
	}
	messages, offsets := records.Messages()
	h, err := svc.projectConversationHistory(j.baseSession, messages, offsets, 0, true, j)
	if err != nil {
		return nil, err
	}
	return webHistoryPayload(h), nil
}

// Finished turns usually refer to the same append-only transcript. Release
// its descriptor during the replay window and reopen the verified prefix on
// demand. If a rewrite already replaced the file, keep the pinned old inode.
func (j *webTurn) releaseBaseline() {
	j.baseMu.Lock()
	defer j.baseMu.Unlock()
	if j.baseFile == nil {
		return
	}
	stat, err := os.Stat(j.baseSession.MessagesPath())
	if j.baseSize == 0 || (err == nil && os.SameFile(j.baseIdentity, stat) && stat.Size() >= j.baseSize) {
		j.baseFile.Close()
		j.baseFile = nil
	}
}

func (j *webTurn) writeFrame(frame []byte) error {
	if j.file == nil && len(j.memory)+len(frame) > replayMemoryBytes {
		f, err := os.CreateTemp("", "bqagent-web-turn-*")
		if err != nil {
			return err
		}
		// POSIX can unlink the private spool immediately. On Windows cleanup
		// removes the file after closing the last handle.
		if err := os.Remove(f.Name()); err != nil {
			j.spoolPath = f.Name()
		}
		if _, err = f.Write(j.memory); err != nil {
			f.Close()
			if j.spoolPath != "" {
				_ = os.Remove(j.spoolPath)
				j.spoolPath = ""
			}
			return err
		}
		j.file = f
		j.memory = nil
	}
	if j.file != nil {
		_, err := j.file.Write(frame)
		return err
	}
	j.memory = append(j.memory, frame...)
	return nil
}

func (j *webTurn) publish(name string, value any) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.terminal || j.expired {
		return io.ErrClosedPipe
	}
	if e, ok := value.(turn.Event); ok && name == "message_snapshot" {
		if j.snapshots == nil {
			j.snapshots = make(webSnapshotState)
		}
		name, value = j.snapshots.compact(e)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	id := len(j.ends)
	prefix := ""
	if name != "message" {
		prefix = "event: " + name + "\n"
	}
	frame := []byte(fmt.Sprintf("id: %d\n%sdata: %s\n\n", id, prefix, data))
	err = j.writeFrame(frame)
	if err != nil {
		frame = []byte(fmt.Sprintf("id: %d\nevent: error\ndata: {\"error\":\"无法保存回合事件\"}\n\n", id))
		j.lastError = frame
		j.cancel()
	}
	j.ends = append(j.ends, j.ends[id-1]+int64(len(frame)))
	j.terminal = err != nil || name == "done" || name == "stopped" || name == "error"
	if j.terminal {
		j.snapshots = nil
	}
	if j.terminal && !j.hasSession {
		j.boundOnce.Do(func() { close(j.bound) })
	}
	close(j.changed)
	j.changed = make(chan struct{})
	return err
}

func (j *webTurn) batch(after int) (data []byte, next int, changed <-chan struct{}, done bool, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	end := len(j.ends) - 1
	if j.expired {
		return nil, after, nil, false, os.ErrNotExist
	}
	if after < 0 || after > end {
		return nil, after, nil, false, fmt.Errorf("invalid event cursor")
	}
	if after == end {
		return nil, after, j.changed, j.terminal, nil
	}
	if len(j.lastError) > 0 && after == end-1 {
		return j.lastError, end, nil, true, nil
	}
	next = after + 1
	for next < end && j.ends[next+1]-j.ends[after] <= 64<<10 && !(next+1 == end && j.lastError != nil) {
		next++
	}
	a, b := j.ends[after], j.ends[next]
	if j.file != nil {
		data = make([]byte, b-a)
		if _, err = j.file.ReadAt(data, a); err != nil {
			return nil, after, nil, false, err
		}
	} else {
		data = j.memory[a:b] // append-only, immutable after publication
	}
	return data, next, nil, j.terminal && next == end, nil
}

func (j *webTurn) subscribe(w http.ResponseWriter, r *http.Request, after int) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unavailable", 500)
		return
	}
	data, next, changed, done, err := j.batch(after)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	var full webSnapshotState
	var external map[string]*strings.Builder
	if r.URL.Query().Get("compact") != "1" {
		full = make(webSnapshotState)
		external = make(map[string]*strings.Builder)
		if after > 0 {
			data, next, changed, done, err = j.batch(0)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()
	for {
		if len(data) > 0 {
			if full != nil {
				data, err = full.fullBatch(data, after, external)
				if err != nil {
					return
				}
			}
			if _, err = w.Write(data); err != nil {
				return
			}
			flusher.Flush()
		}
		if done {
			return
		}
		if changed != nil {
			select {
			case <-r.Context().Done():
				return
			case <-changed:
			}
		}
		if r.Context().Err() != nil {
			return
		}
		data, next, changed, done, err = j.batch(next)
		if err != nil {
			return
		}
	}
}

func (j *webTurn) dispose() {
	j.mu.Lock()
	j.expired = true
	j.boundOnce.Do(func() { close(j.bound) })
	close(j.changed)
	j.changed = make(chan struct{})
	if j.file != nil {
		j.file.Close()
		if j.spoolPath != "" {
			_ = os.Remove(j.spoolPath)
		}
	}
	j.memory = nil
	j.ends, j.lastError, j.snapshots = nil, nil, nil
	j.mu.Unlock()
	j.baseMu.Lock()
	if j.baseFile != nil {
		j.baseFile.Close()
		j.baseFile = nil
	}
	j.baseMu.Unlock()
}
