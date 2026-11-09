package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"bqagent/internal/agent"
	"bqagent/internal/extagent"
	"bqagent/internal/session"
	"bqagent/internal/turn"
)

type commandQueueRequest struct {
	InteractionMode  string              `json:"agent_interaction_mode,omitempty"`
	TierInherited    bool                `json:"tier_inherited,omitempty"`
	ServiceTier      string              `json:"agent_service_tier,omitempty"`
	ExpectedTurn     string              `json:"expected_turn_id,omitempty"`
	Action           string              `json:"action"`
	Workspace        string              `json:"workspace_id"`
	Session          string              `json:"session_id"`
	Parent           string              `json:"parent_turn_id"`
	ID               string              `json:"id"`
	Agent            extagent.AgentName  `json:"agent"`
	Text             string              `json:"text"`
	Kind             string              `json:"kind,omitempty"`
	Model            string              `json:"agent_model,omitempty"`
	Effort           string              `json:"agent_effort,omitempty"`
	EditID           string              `json:"edit_id,omitempty"`
	BodyVersion      *uint64             `json:"body_version,omitempty"`
	Operation        string              `json:"operation_id,omitempty"`
	Anchor           string              `json:"anchor_id,omitempty"`
	Position         string              `json:"position,omitempty"`
	Images           []webUIImagePayload `json:"images,omitempty"`
	Files            []webUIFilePayload  `json:"files,omitempty"`
	KeepAttachments  *[]int              `json:"keep_attachments,omitempty"`
	attachmentDigest string
}

func (req commandQueueRequest) validContent() bool {
	if req.TierInherited && (req.Kind != "message" || req.Agent != extagent.AgentCodex || req.ServiceTier == "") {
		return false
	}
	switch req.Kind {
	case "message":
		return validQueuedText(req.Agent, req.Text, len(req.Images)+len(req.Files) > 0)
	case "":
		return len(req.Images)+len(req.Files) == 0 && req.Model == "" && req.Effort == "" && req.ServiceTier == "" && req.InteractionMode == "" && extagent.ParseCompactCommand(req.Text, false) != nil
	default:
		return false
	}
}

func validQueuedText(agent extagent.AgentName, message string, attachments bool) bool {
	name, prompt, explicit, err := extagent.ParseRouteWithAttachments(message, attachments)
	return agent != "" && agent != extagent.AgentDefault && err == nil && (!explicit || name == agent) &&
		(prompt != "" || attachments) && !strings.HasPrefix(prompt, "/")
}

func (req commandQueueRequest) matches(row *queuedCommand) bool {
	if row == nil || req.Agent != row.Agent || req.Parent != row.Parent || req.Kind != row.Kind || req.Model != row.Model || req.Effort != row.Effort || req.ServiceTier != row.ServiceTier || req.InteractionMode != row.InteractionMode || req.TierInherited != row.TierInherited || req.attachmentDigest != row.InputDigest {
		return false
	}
	if row.TextDigest != "" {
		return row.TextDigest == queueTextDigest(req.Text)
	}
	return row.Text == req.Text
}

func (req commandQueueRequest) matchesBodyVersion(row *queuedCommand) bool {
	if req.BodyVersion == nil {
		return row.BodyVersion == 0
	}
	return row.BodyVersion == *req.BodyVersion
}

func (ch *WebUIChannel) handleCommandQueue(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	query := r.URL.Query()
	req := commandQueueRequest{Workspace: query.Get("workspace_id"), Session: query.Get("session_id"), ID: query.Get("id"), Operation: query.Get("operation_id")}
	switch r.Method {
	case http.MethodGet:
		req.Action = "list"
	case http.MethodDelete:
		req.Action = "withdraw"
		if query.Has("body_version") {
			version, err := strconv.ParseUint(query.Get("body_version"), 10, 64)
			if err != nil {
				writeError(w, 400, chatResponse{Error: "无效的内容版本"})
				return
			}
			req.BodyVersion = &version
		}
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, maxWebUIRequestBodyBytes)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			var limit *http.MaxBytesError
			if errors.As(err, &limit) {
				writeError(w, 413, chatResponse{Error: "队列请求超过大小限制"})
				return
			}
			writeError(w, 400, chatResponse{Error: "无效的队列请求"})
			return
		}
		if decoder.Decode(new(any)) != io.EOF {
			writeError(w, 400, chatResponse{Error: "请求只能包含一个 JSON 对象"})
			return
		}
		switch req.Action {
		case "send_now", "enqueue", "resume", "hold", "renew", "release", "edit", "move", "release_attachments":
		default:
			writeError(w, 400, chatResponse{Error: "未知的队列操作"})
			return
		}
	default:
		writeError(w, 405, chatResponse{Error: "method not allowed"})
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.KeepAttachments != nil && req.Action != "edit" {
		writeError(w, 400, chatResponse{Error: "只有编辑操作可以修改附件列表"})
		return
	}
	if len(req.Images)+len(req.Files) > 0 {
		if !(req.Action == "enqueue" && req.Kind == "message") && !(req.Action == "edit" && req.KeepAttachments != nil) {
			writeError(w, 400, chatResponse{Error: "只有排队消息可以携带附件"})
			return
		}
		req.attachmentDigest = queueBundleDigest(queueAttachmentPayload{Images: req.Images, Files: req.Files})
	}
	if req.Action == "send_now" && (!validTurnID(req.ID) || !validTurnID(req.Operation) || req.BodyVersion == nil || (req.ExpectedTurn != "" && !validTurnID(req.ExpectedTurn))) {
		writeError(w, 400, chatResponse{Error: "立即发送需要条目 ID、操作 ID 和内容版本"})
		return
	}
	if err := req.validateEditAction(); err != nil {
		writeError(w, 400, chatResponse{Error: err.Error()})
		return
	}
	if req.Action == "enqueue" && (!validTurnID(req.Parent) || !validTurnID(req.ID) || !req.validContent()) || (req.Action == "withdraw" || req.Action == "release_attachments") && !validTurnID(req.ID) {
		writeError(w, 400, chatResponse{Error: "无效的命令、操作 ID 或回合 ID"})
		return
	}
	svc, info, err := ch.resolveService(req.Workspace)
	if err != nil {
		writeError(w, 404, chatResponse{Error: "workspace not found"})
		return
	}
	reg := svc.webTurns
	// A brand-new chat may receive input before its first turn announces
	// the session ID. Wait for that particular parent, never another chat.
	if req.Session == "" && req.Action == "enqueue" {
		parent := reg.get(req.Parent)
		if parent == nil {
			writeError(w, 404, chatResponse{Error: "turn not found"})
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-parent.bound:
		}
		parent.mu.Lock()
		req.Session = parent.sessionID
		parent.mu.Unlock()
	}
	id, err := session.CanonicalID(req.Session)
	if err != nil {
		writeError(w, 400, chatResponse{Error: err.Error()})
		return
	}
	req.Session = id
	if r.Method == http.MethodGet && (query.Get("view") == "attachments" || query.Get("view") == "attachment") {
		ch.serveQueueAttachments(w, svc, req, query)
		return
	}
	view, status, err := ch.updateCommandQueue(r.Context(), svc, info, req)
	if err != nil {
		writeError(w, status, chatResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (ch *WebUIChannel) updateCommandQueue(ctx context.Context, svc *Service, info WorkspaceInfo, req commandQueueRequest) (commandQueueView, int, error) {
	reg := svc.webTurns
	reg.queueMu.Lock()
	defer reg.queueMu.Unlock()
	var empty commandQueueView
	s, err := svc.store.Open(req.Session)
	if errors.Is(err, os.ErrNotExist) {
		return empty, 404, err
	}
	if err != nil {
		return empty, 500, err
	}
	if !s.Meta().Chat {
		return empty, 404, fmt.Errorf("此会话不是聊天")
	}
	q, err := reg.readQueue(s, ch.lifetime, req.Action != "list")
	if err != nil {
		if errors.Is(err, errQueueOwned) {
			// A matching receipt is a read, even if a different process
			// now owns the pending queue.
			if old, readErr := readCommandQueue(s, reg.queueOwner); readErr == nil {
				if req.Action == "send_now" {
					if v, status, err := old.sendReplay(req, s.ID()); status != 0 {
						return v, status, err
					}
				}
				if replay, err := old.mutationReplay(req); replay {
					if err != nil {
						return empty, 409, err
					}
					return old.view(s.ID(), req.ID), 200, nil
				}
				row := old.find(req.ID)
				if req.Action == "release_attachments" && row != nil && row.State == "withdrawn" && row.Blob == "" && req.matchesBodyVersion(row) {
					return old.view(s.ID(), req.ID), 200, nil
				}
				if row != nil && ((req.Action == "enqueue" && req.matches(row)) || (req.Action == "withdraw" && row.State == "withdrawn" && req.matchesBodyVersion(row))) {
					return old.view(s.ID(), req.ID), 200, nil
				}
			}
			return empty, 409, err
		}
		return empty, 500, err
	}
	defer reg.releaseIdleQueue(s, q)
	if req.Action == "edit" || req.Action == "move" || req.Action == "hold" || req.Action == "renew" || req.Action == "release" {
		return ch.editCommandQueue(ctx, svc, info, s, q, req)
	}
	if req.Action == "send_now" {
		return ch.sendQueueCard(svc, info, s, q, req)
	}
	row := q.find(req.ID)
	switch req.Action {
	case "list":
		v := q.view(s.ID(), req.ID)
		v.OperationCommitted = req.Operation != "" && req.ID != "" && q.EditedRows[req.Operation] == req.ID
		if send, found := q.Sends[req.Operation]; found && send.Card == req.ID {
			v.OperationCommitted, v.SubmissionID = true, send.Submission
		}
		return v, 200, nil
	case "enqueue":
		if row != nil {
			if !req.matches(row) {
				return empty, 409, fmt.Errorf("命令 ID 已用于其他提交")
			}
			return q.view(s.ID(), req.ID), 200, nil
		}
		if pending, err := s.PendingRewind(); err != nil {
			return empty, 500, err
		} else if pending != nil {
			return empty, 409, session.ErrRewindPending
		}
		parent := reg.get(req.Parent)
		if parent == nil {
			return empty, 404, fmt.Errorf("原回合不存在")
		}
		parent.mu.Lock()
		belongs := parent.hasSession && parent.sessionID == s.ID()
		ended := parent.terminal || parent.expired || parent.stopRequested
		parent.mu.Unlock()
		if !belongs {
			return empty, 409, fmt.Errorf("原回合不属于当前会话，未加入队列")
		}
		if ended {
			return empty, http.StatusGone, fmt.Errorf("原回合已结束，未加入队列")
		}
		if err := validateQueueSession(s); err != nil {
			return empty, 409, err
		}
		entry := queuedCommand{ID: req.ID, Parent: req.Parent, Agent: req.Agent, Text: req.Text, Kind: req.Kind, Model: req.Model, Effort: req.Effort, ServiceTier: req.ServiceTier, InteractionMode: req.InteractionMode, TierInherited: req.TierInherited, State: "waiting"}
		var binding string
		if req.Kind == "message" && req.Agent == builtinChatAgent {
			if req.Model != "" || req.ServiceTier != "" || req.InteractionMode != "" {
				return empty, 400, fmt.Errorf("内置 Agent 使用当前会话配置的模型")
			}
			var effort agent.ReasoningEffort
			if effort, err = agent.ParseReasoningEffort(req.Effort); err == nil {
				entry.Picks = &queuedModelSelection{Effort: string(effort)}
			}
		} else if req.Kind == "message" {
			var state extagent.SessionState
			state, err = svc.externalBroker.QueuedMessageOptions(ctx, req.Agent, s.ID(), extagent.PromptOptions{
				Model: req.Model, Effort: req.Effort, ServiceTier: req.ServiceTier, InteractionMode: req.InteractionMode,
			}, len(req.Images) > 0)
			binding = state.ExternalSessionID
			entry.Picks = &queuedModelSelection{Model: state.Model, Effort: state.Effort, ServiceTier: state.ServiceTier, InteractionMode: state.InteractionMode, TierInherited: req.TierInherited || req.ServiceTier == "" && state.ServiceTier != ""}
		} else {
			binding, err = svc.externalBroker.QueuedCompactBinding(ctx, req.Agent, s.ID(), req.Text)
		}
		if err != nil {
			return empty, 400, err
		}
		if binding != "" {
			if q.Bindings == nil {
				q.Bindings = make(map[string]string)
			}
			q.Bindings[req.ID] = binding
		}
		if req.attachmentDigest != "" {
			payload, metadata, err := svc.snapshotQueueAttachments(req.Images, req.Files)
			if err != nil {
				return empty, queueAttachmentStatus(err), err
			}
			entry.Blob, err = writeQueueBundle(s, payload)
			if err != nil {
				return empty, 500, err
			}
			entry.InputDigest, entry.Attachments = req.attachmentDigest, metadata
		}
		q.Entries = append(q.Entries, entry)
	case "withdraw":
		if row == nil {
			return empty, 404, fmt.Errorf("未找到排队命令")
		}
		if !req.matchesBodyVersion(row) {
			return empty, 409, fmt.Errorf("条目已被修改，请核对后再撤回")
		}
		switch row.State {
		case "withdrawn":
			return q.view(s.ID(), req.ID), 200, nil
		case "waiting":
			// Let a disconnected browser retrieve the frozen files even after
			// the DELETE receipt was lost. Restoration has a separate ack.
			if row.Blob != "" {
				row.State, row.Editing = "withdrawn", false
			} else {
				q.settleReceipt(row, "withdrawn")
			}
		case "unknown", "failed", "stopped", "returned":
			q.settleReceipt(row, "withdrawn")
		default:
			return empty, 409, fmt.Errorf("命令已经交给 Agent，无法撤回")
		}
		if !q.hasWaiting() {
			q.Paused = false
		}
	case "release_attachments":
		if row == nil || row.State != "withdrawn" || !req.matchesBodyVersion(row) {
			return empty, 409, fmt.Errorf("只能释放该版本已撤回的附件")
		}
		if row.Blob == "" {
			return q.view(s.ID(), req.ID), 200, nil
		}
		q.settleReceipt(row, "withdrawn")
	case "resume":
		if err := validateQueueSession(s); err != nil {
			return empty, 409, err
		}
		q.Paused = false
	default:
		return empty, 400, fmt.Errorf("未知的队列操作")
	}
	_, start, err := ch.commitCommandQueue(svc, info, s, q, "", req.Action == "resume" || req.Action == "withdraw")
	if err != nil {
		return empty, 500, err
	}
	if req.Action == "resume" {
		reg.resumeStopped(s.ID())
	}
	if req.Action == "withdraw" {
		reg.queueLeases[s.ID()].retireEdits(req.ID)
	}
	if start != nil {
		start()
	}
	return q.view(s.ID(), req.ID), 200, nil
}

func validateQueueSession(s *session.Session) error {
	meta := s.Meta()
	if storedChatMode(meta.CurrentMode) != ChatModeRun || storedConversationType(meta.ConversationType) == ConversationTypeGroup {
		return fmt.Errorf("只有 Run 单聊可以排队发送")
	}
	return nil
}

func (ch *WebUIChannel) registerWebTurn(svc *Service, request TurnRequest) (*webTurn, context.Context, bool) {
	reg := svc.webTurns
	reg.queueMu.Lock()
	defer reg.queueMu.Unlock()
	j, ctx, newJob := reg.start(ch.lifetime, request.TurnID, request.SessionID)
	if !newJob || request.SessionID == "" {
		return j, ctx, newJob
	}
	s, err := svc.store.Open(request.SessionID)
	if err != nil {
		return j, ctx, newJob
	}
	q, err := reg.readQueue(s, ch.lifetime, false)
	if err != nil {
		log.Printf("read WebUI command queue at turn admission: %v", err)
		return j, ctx, newJob
	}
	defer reg.releaseIdleQueue(s, q)
	if reg.queueLeases[s.ID()] != nil && q.Paused && q.hasWaiting() {
		j.freshAfterPause = true
		j.pauseAt = q.PauseAt
	}
	return j, ctx, newJob
}

// A fresh user turn can release a paused queue once that turn starts in Run
// mode. A newer Stop supersedes sends admitted before it.
func (ch *WebUIChannel) bindWebTurn(svc *Service, j *webTurn, id string) {
	j.bind(svc, id)
	if !j.freshAfterPause {
		return
	}
	reg := svc.webTurns
	reg.queueMu.Lock()
	defer reg.queueMu.Unlock()
	if j.stopRequested {
		return
	}
	s, err := svc.store.Open(id)
	if err != nil || validateQueueSession(s) != nil {
		return
	}
	q, err := reg.readQueue(s, ch.lifetime, false)
	if err != nil {
		_ = j.publish("queue_error", map[string]string{"error": err.Error()})
		return
	}
	defer reg.releaseIdleQueue(s, q)
	if reg.queueLeases[id] != nil && q.Paused && q.PauseAt == j.pauseAt {
		q.Paused = false
		if err = q.save(s); err != nil {
			_ = j.publish("queue_error", map[string]string{"error": err.Error()})
			return
		}
		reg.resumeStopped(id)
		reg.notifyQueue(q.view(id, ""))
	}
}

// Persist the dispatch before starting the child. The caller holds queueMu.
func (ch *WebUIChannel) commitCommandQueue(svc *Service, info WorkspaceInfo, s *session.Session, q *commandQueue, except string, drain bool) (string, func(), error) {
	reg := svc.webTurns
	var child *webTurn
	var start func()
	if drain && q.hasWaiting() && validateQueueSession(s) != nil {
		q.Paused = true
		q.PauseAt = q.Revision + 1
	}
	if next := q.next(); drain && !q.Paused && next != nil && ch.lifetime.Err() == nil && !reg.otherActive(s.ID(), except) {
		var err error
		child, start, err = ch.prepareQueueChild(svc, info, s, q, next)
		if err != nil {
			return "", nil, err
		}
	}
	if err := q.save(s); err != nil {
		if child != nil {
			child.cancel()
			reg.expire(child)
		}
		return "", nil, err
	}
	reg.notifyQueue(q.view(s.ID(), ""))
	if child == nil {
		return "", nil, nil
	}
	return child.id, start, nil
}

func (ch *WebUIChannel) prepareQueueChild(svc *Service, info WorkspaceInfo, s *session.Session, q *commandQueue, next *queuedCommand) (*webTurn, func(), error) {
	reg := svc.webTurns
	if next.Kind != "" && next.Kind != "message" {
		return nil, nil, fmt.Errorf("未知的排队内容类型 %q", next.Kind)
	}
	id := next.TurnID
	if next.State != "submitted" || id == "" {
		id = turn.NewID()
	}
	child, ctx, created := reg.startCommand(ch.lifetime, id, s.ID(), next.ID)
	if !created {
		return nil, nil, fmt.Errorf("排队回合 ID 冲突")
	}
	if q.Paused && next.State == "submitted" {
		child.freshAfterPause = true
		child.pauseAt = q.PauseAt
	}
	next.State, next.TurnID, next.SteerTurn = "running", id, ""
	request := TurnRequest{SessionID: s.ID(), TurnID: id, Agent: next.Agent, Message: "/" + string(next.Agent) + " " + next.Text,
		queuedCompact: next.Kind == "", queuedMessage: next.Kind == "message", queuedBinding: q.Bindings[next.ID]}
	if request.queuedMessage {
		request.Message = next.Text
		request.queuedBlob = next.Blob
	}
	if next.Picks != nil {
		request.AgentModel, request.AgentEffort, request.AgentServiceTier = next.Picks.Model, next.Picks.Effort, next.Picks.ServiceTier
		request.queuedTierInherited = next.Picks.TierInherited
		request.AgentInteractionMode = next.Picks.InteractionMode
	}
	if next.Kind == "message" && next.Agent == builtinChatAgent {
		request.ReasoningEffort = agent.ReasoningEffort(request.AgentEffort)
		request.AgentEffort = ""
	}
	return child, func() { go ch.runWebTurn(ctx, svc, info, request, child) }, nil
}

func (ch *WebUIChannel) finishWebTurn(svc *Service, info WorkspaceInfo, j *webTurn, name string, event any) {
	reg := svc.webTurns
	reg.queueMu.Lock()
	defer reg.queueMu.Unlock()
	var failure string
	if name == "error" {
		switch payload := event.(type) {
		case map[string]any:
			failure, _ = payload["error"].(string)
		case map[string]string:
			failure = payload["error"]
		}
	}
	next, start, err := ch.settleCommandQueue(svc, info, j, name, failure)
	if err != nil {
		log.Printf("WebUI command queue for turn %s: %v", j.id, err)
		_ = j.publish("queue_error", map[string]string{"error": "无法更新命令队列：" + err.Error()})
	}
	if next != "" {
		switch terminal := event.(type) {
		case webUIDoneEvent:
			terminal.NextTurnID = next
			event = terminal
		case map[string]any:
			terminal["next_turn_id"] = next
		case map[string]string:
			terminal["next_turn_id"] = next
		}
	}
	_ = j.publish(name, event)
	if start != nil {
		start()
	}
}

func (ch *WebUIChannel) settleCommandQueue(svc *Service, info WorkspaceInfo, j *webTurn, name, failure string) (string, func(), error) {
	reg := svc.webTurns
	j.mu.Lock()
	id, bound, expired, terminal := j.sessionID, j.hasSession, j.expired, j.terminal
	j.mu.Unlock()
	if expired || id == "" || !bound && j.commandID == "" {
		return "", nil, nil
	}
	s, err := svc.store.Open(id)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	q, err := reg.readQueue(s, ch.lifetime, false)
	if err != nil {
		return "", nil, err
	}
	// Another process owns this queue, or it is empty.
	if reg.queueLeases[s.ID()] == nil {
		return "", nil, nil
	}
	defer reg.releaseIdleQueue(s, q)
	unconfirmed := false
	for i := range q.Entries {
		row := &q.Entries[i]
		if row.State == "steering" && row.SteerTurn == j.id {
			row.State = "unknown"
			row.Error = "当前回合结束前未确认消费"
			q.recordHandoff(s, row)
			unconfirmed = true
		}
	}
	owned := q.find(j.commandID)
	if owned != nil && owned.TurnID == j.id && owned.State == "running" {
		switch name {
		case "done":
			q.settleReceipt(owned, "done")
		case "stopped":
			owned.State = "stopped"
		default:
			owned.State, owned.Error = "failed", failure
		}
	} else if !unconfirmed && !q.hasWaiting() {
		return "", nil, nil
	}
	if unconfirmed || ((name != "done" || terminal || j.stopRequested) && !j.queueResumed) || ch.lifetime.Err() != nil {
		q.holdSubmitted()
		q.Paused = q.hasWaiting()
		if q.Paused {
			q.PauseAt = q.Revision + 1
		}
	}
	next, start, err := ch.commitCommandQueue(svc, info, s, q, j.id, true)
	if err == nil && unconfirmed {
		for _, row := range q.Entries {
			if row.SteerTurn == j.id && row.State == "unknown" {
				_ = j.publish("queue_delivery", q.History[row.TurnID].message())
			}
		}
	}
	return next, start, err
}
