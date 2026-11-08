package server

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"bqagent/internal/atomicfile"
	"bqagent/internal/extagent"
	"bqagent/internal/privatefile"
	"bqagent/internal/session"
	"bqagent/internal/transcript"
	"bqagent/internal/turn"
)

const queueJournalVersion = 4

// Entries are also operation receipts. A retry can confirm a settled or
// withdrawn submission without admitting it a second time.
type queuedCommand struct {
	InteractionMode string                `json:"agent_interaction_mode,omitempty"`
	TierInherited   bool                  `json:"tier_inherited,omitempty"`
	ServiceTier     string                `json:"agent_service_tier,omitempty"`
	Priority        uint64                `json:"priority,omitempty"`
	SteerTurn       string                `json:"steer_turn_id,omitempty"`
	ID              string                `json:"id"`
	Parent          string                `json:"parent"`
	Agent           extagent.AgentName    `json:"agent"`
	Text            string                `json:"text"`
	TextDigest      string                `json:"text_sha256,omitempty"`
	BodyVersion     uint64                `json:"body_version,omitempty"`
	Editing         bool                  `json:"editing,omitempty"`
	Kind            string                `json:"kind,omitempty"` // Empty in original /compact receipts.
	Model           string                `json:"agent_model,omitempty"`
	Effort          string                `json:"agent_effort,omitempty"`
	Picks           *queuedModelSelection `json:"selection,omitempty"`
	State           string                `json:"state"`
	Error           string                `json:"error,omitempty"`
	TurnID          string                `json:"turn_id,omitempty"`
	Attachments     []queueAttachmentInfo `json:"attachments,omitempty"`
	Blob            string                `json:"blob,omitempty"`
	InputDigest     string                `json:"input_attachment_digest,omitempty"`
}

type queuedModelSelection struct {
	InteractionMode string `json:"interaction_mode,omitempty"`
	TierInherited   bool   `json:"tier_inherited,omitempty"`
	ServiceTier     string `json:"service_tier,omitempty"`
	Model           string `json:"model,omitempty"`
	Effort          string `json:"effort,omitempty"`
}

type commandQueue struct {
	History    map[string]queueHandoffHistory `json:"handoff_history,omitempty"`
	Sends      map[string]queueSendReceipt    `json:"sends,omitempty"`
	Version    int                            `json:"version"`
	Owner      string                         `json:"owner"`
	Revision   uint64                         `json:"revision"`
	PauseAt    uint64                         `json:"pause_at,omitempty"`
	Paused     bool                           `json:"paused"`
	Entries    []queuedCommand                `json:"entries"`
	Bindings   map[string]string              `json:"bindings,omitempty"`
	Mutations  map[string]string              `json:"mutations,omitempty"`   // Operation ID -> digest of the committed edit or move.
	EditedRows map[string]string              `json:"edited_rows,omitempty"` // Locate an edit receipt without resending its attachment bytes.
	released   []string
}

type commandQueueView struct {
	SubmissionID       string          `json:"submission_id,omitempty"`
	SessionID          string          `json:"session_id"`
	Revision           uint64          `json:"revision"`
	Paused             bool            `json:"paused"`
	Items              []queuedCommand `json:"items"`
	Receipt            *queuedCommand  `json:"receipt,omitempty"`
	TurnID             string          `json:"turn_id,omitempty"`
	OperationCommitted bool            `json:"operation_committed,omitempty"`
}

func (q *commandQueue) view(id, operation string) commandQueueView {
	v := commandQueueView{SessionID: id, Revision: q.Revision, Paused: q.Paused, Items: []queuedCommand{}}
	for i := range q.Entries {
		row := q.Entries[i]
		row.TextDigest = ""
		row.Blob, row.InputDigest = "", ""
		if row.ID == operation {
			v.Receipt = &row
		}
		switch row.State {
		case "running":
			v.TurnID = row.TurnID
			v.Items = append(v.Items, row)
		case "steering":
			v.TurnID = row.SteerTurn
			v.Items = append(v.Items, row)
		case "waiting", "submitted", "returned", "unknown", "failed", "stopped":
			v.Items = append(v.Items, row)
		case "withdrawn":
			if len(row.Attachments) > 0 {
				v.Items = append(v.Items, row)
			}
		}
	}
	return v
}

func queueTextDigest(text string) string {
	hash := sha256.Sum256([]byte(text))
	return hex.EncodeToString(hash[:])
}

// Settled messages already live in the transcript. Keep a compact receipt for
// delayed client retries without rewriting a second copy of every user prompt.
func (q *commandQueue) settleReceipt(row *queuedCommand, state string) {
	row.State = state
	if row.TextDigest == "" {
		row.TextDigest = queueTextDigest(row.Text)
	}
	row.Text, row.Error, row.Picks = "", "", nil
	row.Editing = false
	if row.Blob != "" {
		q.released = append(q.released, row.Blob)
		row.Blob, row.Attachments = "", nil
	}
	delete(q.Bindings, row.ID)
}

func (q *commandQueue) find(id string) *queuedCommand {
	for i := range q.Entries {
		if q.Entries[i].ID == id {
			return &q.Entries[i]
		}
	}
	return nil
}

func (q *commandQueue) next() *queuedCommand {
	var next, priority *queuedCommand
	for i := range q.Entries {
		row := &q.Entries[i]
		if row.State == "running" || row.State == "steering" {
			return nil
		}
		if row.State == "submitted" && (priority == nil || row.Priority < priority.Priority) {
			priority = row
		}
		if next == nil && (row.State == "waiting" || row.State == "returned") {
			next = row
		}
	}
	if priority != nil {
		return priority
	}
	if next != nil && (next.Editing || next.State == "returned") {
		return nil
	}
	return next
}

func (q *commandQueue) save(s *session.Session) error {
	if err := q.writeJournal(s); err != nil {
		return err
	}
	if len(q.released) > 0 {
		root, err := os.OpenRoot(s.Dir())
		if err == nil {
			q.removeReleasedAttachments(root)
			_ = root.Close()
		}
	}
	return nil
}

func (q *commandQueue) writeJournal(s *session.Session) error {
	q.Version = queueJournalVersion
	q.Revision++
	data, err := json.Marshal(q)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(s.ContextDir())
	if err != nil {
		return err
	}
	defer root.Close()
	return atomicfile.WriteRoot(root, "webui-command-queue.json", data, privatefile.FileMode)
}

var errQueueOwned = errors.New("此会话的命令队列正在另一服务中运行")

type queueLease struct {
	file    *os.File
	stop    func() bool
	editors map[string]*queueEditHold
}

func queueLockName(s *session.Session) string { return "." + s.ID() + ".webui-queue.lock" }

func (r *webTurnRegistry) ownQueue(s *session.Session, life context.Context) error {
	if r.queueLeases[s.ID()] != nil {
		return nil
	}
	if err := life.Err(); err != nil {
		return err
	}
	// A sibling lock can be held through removal of the entire session directory,
	// including on Windows. The session store ignores non-directory entries.
	root, err := os.OpenRoot(filepath.Dir(s.Dir()))
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := root.OpenFile(queueLockName(s), os.O_CREATE|os.O_RDWR, privatefile.FileMode)
	if err != nil {
		return err
	}
	if err = session.TryLockFile(file, true); err != nil {
		if errors.Is(err, session.ErrContextBusy) {
			err = errQueueOwned
		}
		_ = file.Close()
		return err
	}
	if r.queueLeases == nil {
		r.queueLeases = make(map[string]*queueLease)
	}
	lease := &queueLease{file: file}
	r.queueLeases[s.ID()] = lease
	lease.stop = context.AfterFunc(life, func() {
		r.queueMu.Lock()
		defer r.queueMu.Unlock()
		if r.queueLeases[s.ID()] == lease {
			r.releaseQueue(s.ID())
		}
	})
	return nil
}

func (r *webTurnRegistry) releaseQueue(id string) {
	if l := r.queueLeases[id]; l != nil {
		for _, hold := range l.editors {
			hold.timer.Stop()
		}
		l.stop()
		_ = l.file.Close()
		delete(r.queueLeases, id)
	}
}

func (q *commandQueue) hasWaiting() bool {
	for _, row := range q.Entries {
		if row.State == "waiting" || row.State == "submitted" {
			return true
		}
	}
	return false
}

func (q *commandQueue) hasWork() bool {
	for _, row := range q.Entries {
		if row.State == "waiting" || row.State == "submitted" || row.State == "steering" || row.State == "running" {
			return true
		}
	}
	return false
}

func (r *webTurnRegistry) releaseIdleQueue(s *session.Session, q *commandQueue) {
	if !q.hasWork() && !r.rewinding[s.ID()] {
		r.releaseQueue(s.ID())
	}
}

func readCommandQueue(s *session.Session, owner string) (*commandQueue, error) {
	root, err := os.OpenRoot(s.ContextDir())
	if err != nil {
		return nil, err
	}
	defer root.Close()
	data, err := root.ReadFile("webui-command-queue.json")
	if errors.Is(err, os.ErrNotExist) {
		return &commandQueue{Version: queueJournalVersion, Owner: owner}, nil
	}
	if err != nil {
		return nil, err
	}
	var q commandQueue
	if err = json.Unmarshal(data, &q); err != nil {
		return nil, err
	}
	if q.Version < 1 || q.Version > queueJournalVersion {
		return nil, fmt.Errorf("不支持的命令队列版本 %d", q.Version)
	}
	return &q, nil
}

// A process owns pending work until it settles or the server exits. Another
// instance may read the snapshot but cannot mistake a live dispatch for a
// crashed one. Re-read after taking ownership to avoid acting on stale rows.
func (r *webTurnRegistry) readQueue(s *session.Session, life context.Context, mutate bool) (*commandQueue, error) {
	if r.rewinding[s.ID()] {
		if mutate {
			return nil, fmt.Errorf("%w：此会话正在回退历史", errQueueOwned)
		}
		return readCommandQueue(s, r.queueOwner)
	}
	if r.queueOwner == "" {
		r.queueOwner = turn.NewID()
	}
	q, err := readCommandQueue(s, r.queueOwner)
	if err != nil {
		return nil, err
	}
	checkHandoffs := q.Owner != r.queueOwner && len(q.History) > 0
	if !mutate && !q.hasWork() && !checkHandoffs {
		return q, nil
	}
	if r.queueLeases[s.ID()] == nil {
		if err = r.ownQueue(s, life); err != nil {
			if errors.Is(err, errQueueOwned) && !mutate {
				return q, nil
			}
			return nil, err
		}
		if err = s.RefreshContexts(); err != nil {
			r.releaseQueue(s.ID())
			return nil, err
		}
		q, err = readCommandQueue(s, r.queueOwner)
		if err != nil {
			r.releaseQueue(s.ID())
			return nil, err
		}
		q.pruneAttachmentDirectory(s)
	}
	restarted := q.Owner != r.queueOwner
	var applied map[string]bool
	changed := false
	if restarted && len(q.History) > 0 {
		var err error
		applied, err = recordedQueueInputs(s)
		if err != nil {
			return nil, err
		}
		for id, h := range q.History {
			if h.Context == s.ContextNumber() && applied[id] {
				if h.Blob != "" {
					q.released = append(q.released, h.Blob)
				}
				delete(q.History, id)
			}
		}
		changed = true // Record that this owner has checked the durable transcript.
	}
	if restarted && q.holdSubmitted() {
		changed = true
	}
	for i := range q.Entries {
		row := &q.Entries[i]
		if row.Editing && (restarted || !r.queueLeases[s.ID()].hasOtherEdit(row.ID, "")) {
			row.Editing = false
			q.Paused = q.hasWaiting()
			changed = true
		}
		if row.State == "steering" {
			parent := r.get(row.SteerTurn)
			if restarted || parent == nil || parent.ended() {
				if applied == nil {
					applied, err = recordedQueueInputs(s)
					if err != nil {
						return nil, err
					}
				}
				if applied[row.TurnID] {
					q.settleReceipt(row, "done")
				} else {
					row.State = "unknown"
					q.recordHandoff(s, row)
					q.Paused = true
				}
				changed = true
			}
		}
		if restarted && row.State == "unknown" && row.SteerTurn != "" && applied[row.TurnID] {
			q.settleReceipt(row, "done")
			changed = true
		}
		if row.State == "running" {
			live := r.get(row.TurnID)
			if restarted || live == nil || live.ended() {
				row.State = "unknown"
				q.Paused = true
				changed = true
			}
		}
		if restarted && row.State == "waiting" {
			q.Paused = true
			changed = true
		}
	}
	q.Owner = r.queueOwner
	if changed {
		q.PauseAt = q.Revision + 1
		if err = q.save(s); err != nil {
			return nil, err
		}
	}
	return q, nil
}

// Stop freezes waiting entries before the native interrupt is sent. If the
// user resumes while cancellation is settling, the terminal handoff honors it.
func (r *webTurnRegistry) pauseQueueForStop(svc *Service, j *webTurn) error {
	j.mu.Lock()
	id := j.sessionID
	j.mu.Unlock()
	if r.queueLeases[id] == nil {
		return nil
	}
	s, err := svc.store.Open(id)
	if err != nil {
		return err
	}
	q, err := readCommandQueue(s, r.queueOwner)
	if err != nil {
		return err
	}
	if !q.hasWaiting() {
		return nil
	}
	q.holdSubmitted()
	q.Paused = true
	q.PauseAt = q.Revision + 1
	if err = q.save(s); err != nil {
		return err
	}
	r.notifyQueue(q.view(id, ""))
	return nil
}

func (r *webTurnRegistry) resumeStopped(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, j := range r.turns {
		j.mu.Lock()
		if j.sessionID == id && !j.terminal && j.stopRequested {
			j.queueResumed = true
		}
		j.mu.Unlock()
	}
}

func (j *webTurn) ended() bool { j.mu.Lock(); defer j.mu.Unlock(); return j.terminal || j.expired }

func (r *webTurnRegistry) otherActive(sessionID, except string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, j := range r.turns {
		j.mu.Lock()
		active := j.id != except && j.sessionID == sessionID && !j.terminal && !j.expired
		j.mu.Unlock()
		if active {
			return true
		}
	}
	return false
}

func (r *webTurnRegistry) notifyQueue(v commandQueueView) {
	r.mu.Lock()
	var jobs []*webTurn
	v.Receipt = nil
	for _, j := range r.turns {
		j.mu.Lock()
		same := j.sessionID == v.SessionID && !j.terminal && !j.expired
		j.mu.Unlock()
		if same {
			jobs = append(jobs, j)
		}
	}
	r.mu.Unlock()
	for _, j := range jobs {
		_ = j.publish("command_queue", v)
	}
}

// Stop or clear returns accepted but unhanded submissions to editable cards.
// The receipt for the earlier press still identifies its original handoff.
func (q *commandQueue) holdSubmitted() bool {
	if !slices.ContainsFunc(q.Entries, func(row queuedCommand) bool { return row.State == "submitted" }) {
		return false
	}
	var slots []int
	var accepted, waiting []queuedCommand
	for i, row := range q.Entries {
		switch row.State {
		case "submitted":
			accepted = append(accepted, row)
		case "waiting", "returned":
			waiting = append(waiting, row)
		default:
			continue
		}
		slots = append(slots, i)
	}
	slices.SortStableFunc(accepted, func(a, b queuedCommand) int { return cmp.Compare(a.Priority, b.Priority) })
	for i := range accepted {
		row := &accepted[i]
		row.State, row.TurnID, row.SteerTurn, row.Priority = "waiting", "", "", 0
		row.BodyVersion++
	}
	for i, row := range append(accepted, waiting...) {
		q.Entries[slots[i]] = row
	}
	return true
}

func recordedQueueInputs(s *session.Session) (map[string]bool, error) {
	records, err := s.LoadMessages()
	if err != nil {
		return nil, err
	}
	applied := make(map[string]bool)
	for _, msg := range records {
		if id, ok := msg[transcript.ExternalInputKey].(string); ok && msg["role"] == "user" {
			applied[id] = true
		}
	}
	return applied, nil
}
