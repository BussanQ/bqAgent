package server

import (
	"cmp"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"

	"bqagent/internal/extagent"
	appruntime "bqagent/internal/runtime"
	"bqagent/internal/session"
	"bqagent/internal/turn"
)

// A retry names the original handoff even after a returned card is sent again.
type queueSendReceipt struct {
	Card       string `json:"card"`
	Version    uint64 `json:"version"`
	Previous   string `json:"previous,omitempty"`
	Submission string `json:"submission"`
}

func (q *commandQueue) sendReplay(req commandQueueRequest, id string) (commandQueueView, int, error) {
	v := q.view(id, req.ID)
	if _, exists := q.Mutations[req.Operation]; exists {
		return v, 409, fmt.Errorf("操作 ID 已用于队列修改")
	}
	receipt, ok := q.Sends[req.Operation]
	if !ok {
		return v, 0, nil
	}
	if receipt.Card != req.ID || req.BodyVersion == nil || receipt.Version != *req.BodyVersion || receipt.Previous != req.ExpectedTurn {
		return v, 409, fmt.Errorf("发送操作 ID 已用于其他条目或版本")
	}
	v.SubmissionID = receipt.Submission
	v.OperationCommitted = true
	return v, 200, nil
}

func (q *commandQueue) recordSend(req commandQueueRequest, submission string) {
	if q.Sends == nil {
		q.Sends = make(map[string]queueSendReceipt)
	}
	q.Sends[req.Operation] = queueSendReceipt{Card: req.ID, Version: *req.BodyVersion, Previous: req.ExpectedTurn, Submission: submission}
}

// Explicit delivery can jump the FIFO and the automatic edit/pause gates.
// Provider work only starts after the same journal write records the receipt.
func (ch *WebUIChannel) sendQueueCard(svc *Service, info WorkspaceInfo, s *session.Session, q *commandQueue, req commandQueueRequest) (commandQueueView, int, error) {
	reg := svc.webTurns
	v, status, err := q.sendReplay(req, s.ID())
	if status != 0 {
		return v, status, err
	}
	if req.BodyVersion == nil {
		return v, 400, fmt.Errorf("缺少条目内容版本")
	}
	row := q.find(req.ID)
	if row == nil || row.State == "withdrawn" {
		return v, 404, fmt.Errorf("条目不存在或已撤回")
	}
	if !req.matchesBodyVersion(row) {
		return v, 409, fmt.Errorf("条目已被修改，请核对最新内容")
	}
	if err = validateQueueSession(s); err != nil {
		return v, 409, err
	}
	if ch.lifetime.Err() != nil {
		return v, 409, fmt.Errorf("服务正在停止")
	}
	if svc.externalBroker.HasPendingPrompt(s.ID()) {
		return v, 409, fmt.Errorf("请先回答当前 Agent 的请求")
	}
	// A concurrent automatic dispatch or another click has already consumed
	// this version. Persist the alias so it continues to replay that attempt.
	var previousHandoff string
	for _, r := range q.Sends {
		if r.Card == req.ID && r.Version == *req.BodyVersion && r.Previous == req.ExpectedTurn {
			previousHandoff = r.Submission
			break
		}
	}
	if previousHandoff == "" && row.TurnID != "" && req.ExpectedTurn == "" {
		previousHandoff = row.TurnID
	}
	if previousHandoff != "" {
		q.recordSend(req, previousHandoff)
		if err = q.save(s); err != nil {
			return v, 500, err
		}
		return q.sendReplay(req, s.ID())
	}
	if row.State != "waiting" && row.State != "returned" && row.State != "failed" {
		return v, 409, fmt.Errorf("此条目不能再次发送")
	}
	if row.TurnID != req.ExpectedTurn {
		return v, 409, fmt.Errorf("条目的发送状态已变化")
	}
	parent := reg.get(reg.active(s.ID()))
	if parent != nil && (parent.stopRequested || row.Kind == "") {
		return v, 409, fmt.Errorf("请等待当前回合结束后再发送此条目")
	}
	if parent == nil {
		// A non-WebUI turn in this or another process may own the context.
		gate, err := s.OpenContextLock(true)
		if errors.Is(err, session.ErrContextBusy) {
			return v, 409, err
		}
		if err != nil {
			return v, 500, err
		}
		_ = gate.Close()
	}
	row.TurnID = "card-" + turn.NewID()
	row.Error = ""
	row.Editing = false
	row.SteerTurn = ""
	row.State = "submitted"
	row.Priority = q.Revision + 1
	q.recordSend(req, row.TurnID)
	var child *webTurn
	var start func()
	switch {
	case parent == nil:
		child, start, err = ch.prepareQueueChild(svc, info, s, q, row)
	default:
		row.SteerTurn = parent.id
		start, err = ch.prepareQueuedSteer(svc, parent, s, q, row)
	}

	if err != nil {
		return v, 500, err
	}
	if err = q.save(s); err != nil {
		// Directory sync can fail after the rename. A visible matching
		// receipt already consumed the card; finish the recorded handoff once.
		published, readErr := readCommandQueue(s, reg.queueOwner)
		if readErr != nil || published.Sends[req.Operation] != q.Sends[req.Operation] {
			if child != nil {
				child.cancel()
				reg.expire(child)
			}
			return v, 500, err
		}
	}

	reg.queueLeases[s.ID()].retireEdits(row.ID)
	reg.notifyQueue(q.view(s.ID(), ""))
	if start != nil {
		start()
	}
	return q.sendReplay(req, s.ID())
}

func (ch *WebUIChannel) prepareQueuedSteer(svc *Service, parent *webTurn, s *session.Session, q *commandQueue, row *queuedCommand) (func(), error) {
	if row.Kind != "message" || svc.webTurns.otherActive(s.ID(), parent.id) || !queueTargetMatches(parent.inputTarget, row, q.Bindings[row.ID]) {
		return nil, nil
	}
	handler := svc.activeTurns.queueCardHandler(parent.id, row.Agent == builtinChatAgent)
	if handler == nil {
		return nil, nil
	}
	input, err := queueSteeringInput(svc, s, row)
	if err != nil {
		return nil, err
	}
	row.State, row.SteerTurn = "steering", parent.id
	return func() { ch.startQueuedSteer(svc, parent, s, input, handler) }, nil
}

// An explicit press may arrive between job admission and native input readiness.
// Only submissions admitted against this exact parent can join it.
func (ch *WebUIChannel) deliverReadyCards(svc *Service, job *webTurn, target *extagent.SessionState) {
	reg := svc.webTurns
	reg.queueMu.Lock()
	defer reg.queueMu.Unlock()
	if target != nil {
		job.inputTarget = *target
	}
	job.mu.Lock()
	id := job.sessionID
	job.mu.Unlock()
	if reg.queueLeases[id] == nil || job.stopRequested || job.ended() || svc.externalBroker.HasPendingPrompt(id) {
		return
	}
	s, err := svc.store.Open(id)
	if err != nil {
		return
	}
	q, err := readCommandQueue(s, reg.queueOwner)
	if err != nil || q.Paused {
		return
	}
	var candidates []*queuedCommand
	for i := range q.Entries {
		row := &q.Entries[i]
		if row.State == "submitted" && row.SteerTurn == job.id {
			candidates = append(candidates, row)
		}
	}
	slices.SortFunc(candidates, func(a, b *queuedCommand) int { return cmp.Compare(a.Priority, b.Priority) })
	var starts []func()
	changed := false
	for _, row := range candidates {
		start, err := ch.prepareQueuedSteer(svc, job, s, q, row)
		if err != nil {
			row.State = "returned"
			row.Error = err.Error()
			changed = true
		}
		if start != nil {
			starts = append(starts, start)
			changed = true
		}
	}
	if !changed {
		return
	}
	if err := q.save(s); err != nil {
		_ = job.publish("queue_error", map[string]string{"error": err.Error()})
		return
	}
	reg.notifyQueue(q.view(id, ""))
	for _, start := range starts {
		start()
	}
}

func queueTargetMatches(target extagent.SessionState, row *queuedCommand, binding string) bool {
	if target.Agent != row.Agent || (binding != "" && binding != target.ExternalSessionID) {
		return false
	}
	p := row.Picks
	if p == nil {
		return true
	}
	tierMatches := p.ServiceTier == "" || p.ServiceTier == target.ServiceTier && (p.TierInherited || p.ServiceTier == target.PreferredServiceTier)
	modeMatches := p.InteractionMode == "" || p.InteractionMode == target.InteractionMode
	return modeMatches && tierMatches && (p.Model == "" || p.Model == target.Model) && (p.Effort == "" || p.Effort == target.Effort)
}

func queueSteeringInput(svc *Service, s *session.Session, row *queuedCommand) (turn.Input, error) {
	input := turn.Input{ID: row.TurnID, QueueCard: row.ID, Kind: turn.Steering}
	_, prompt, _, err := extagent.ParseRouteWithAttachments(row.Text, row.Blob != "")
	if err != nil {
		return input, err
	}
	var attachments string
	if row.Blob != "" {
		request := TurnRequest{SessionID: s.ID(), queuedBlob: row.Blob}
		if err := svc.loadQueueTurnAttachments(&request); err != nil {
			return input, err
		}
		files, err := svc.materializeFiles(s.ID(), request.Files)
		if err != nil {
			return input, err
		}
		attachments = formatAttachmentContext(files)
		input.Images = request.Images
	}
	input.Text = prompt + attachments
	input.Content = appruntime.UserMessageContent(row.Text+attachments, input.Images)
	return input, nil
}

// Called under queueMu. Wait for earlier native writes outside the lock so
// their callbacks, Stop, and disconnect/replay can all make progress.
func (ch *WebUIChannel) startQueuedSteer(svc *Service, parent *webTurn, saved *session.Session, input turn.Input, send func(turn.Input) error) {
	id := saved.ID()
	previous := parent.steerTail
	done := make(chan struct{})
	parent.steerTail = done
	go func() {
		defer close(done)
		if previous != nil {
			<-previous
		}
		reg := svc.webTurns
		reg.queueMu.Lock()
		closed := parent.stopRequested || parent.ended() || ch.lifetime.Err() != nil
		prompt := svc.externalBroker.HasPendingPrompt(id)
		reg.queueMu.Unlock()
		var err error
		switch {
		case closed:
			err = turn.ErrInputRejected
		case prompt:
			err = fmt.Errorf("%w：请先回答当前 Agent 的请求", turn.ErrInputRejected)
		default:
			err = send(input)
		}
		if err != nil {
			reg.queueMu.Lock()
			defer reg.queueMu.Unlock()
			s, loadErr := svc.store.Open(id)
			if loadErr != nil || s.ContextNumber() != saved.ContextNumber() {
				return
			}
			q, loadErr := reg.readQueue(s, ch.lifetime, true)
			if loadErr != nil || reg.queueLeases[id] == nil {
				return
			}
			defer reg.releaseIdleQueue(s, q)
			row := q.find(input.QueueCard)
			if row == nil || row.TurnID != input.ID || row.State != "steering" && row.State != "unknown" {
				return
			}
			row.State = "unknown"
			if errors.Is(err, turn.ErrInputRejected) {
				row.State = "returned"
			}
			row.Error = err.Error()
			q.recordHandoff(s, row)
			if row.State == "unknown" {
				q.Paused = q.hasWaiting()
				q.PauseAt = q.Revision + 1
			}
			if err := q.save(s); err != nil {
				log.Printf("save queued input refusal: %v", err)
				return
			}
			reg.notifyQueue(q.view(id, ""))
			_ = parent.publish("queue_delivery", q.History[row.TurnID].message())
		}
	}()
}

// A provider acceptance is not proof of consumption. Only input.applied
// settles a steered card, after the user boundary has been recorded.
func (ch *WebUIChannel) appliedQueueInput(svc *Service, job *webTurn, event turn.Event) bool {
	if !strings.HasPrefix(event.ItemID, "card-") {
		return false
	}
	reg := svc.webTurns
	reg.queueMu.Lock()
	defer reg.queueMu.Unlock()
	job.mu.Lock()
	id := job.sessionID
	job.mu.Unlock()
	s, err := svc.store.Open(id)
	if err != nil {
		return false
	}
	q, err := reg.readQueue(s, ch.lifetime, true)
	if err != nil || reg.queueLeases[id] == nil {
		return false
	}
	defer reg.releaseIdleQueue(s, q)
	for i := range q.Entries {
		row := &q.Entries[i]
		archived := q.History[row.TurnID]
		if row.TurnID != event.ItemID || row.SteerTurn != job.id || (row.State != "steering" && row.State != "unknown" && archived.State != "unknown") {
			continue
		}
		blob, text := row.Blob, row.Text
		if archived.ID != "" {
			blob, text = archived.Blob, archived.Text
		}
		var media queueAttachmentPayload
		if blob != "" {
			media, err = readQueueBundle(s, blob)
			if err != nil {
				return false
			}
		}
		files := make([]webUIFilePayload, 0, len(media.Files))
		for _, file := range media.Files {
			files = append(files, webUIFilePayload{Name: file.Name, Path: file.Path})
		}
		_ = job.publish("queued_input_applied", map[string]any{"id": row.TurnID, "message": text, "images": media.Images, "files": files})
		delete(q.History, row.TurnID)
		if row.Blob == "" && blob != "" {
			q.released = append(q.released, blob)
		}
		q.settleReceipt(row, "done")
		if err = q.save(s); err != nil {
			log.Printf("save applied queued input: %v", err)
		} else {
			reg.notifyQueue(q.view(id, ""))
		}
		return true
	}
	return false
}
