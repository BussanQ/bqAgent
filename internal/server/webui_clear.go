package server

import (
	"errors"
	"fmt"
	"net/http"
	"os"

	"slices"
	"strings"

	"bqagent/internal/extagent"

	"bqagent/internal/session"
)

func clearRoute(message string, selected extagent.AgentName) (extagent.AgentName, string, bool) {
	agent, text, explicit, _ := extagent.ParseRoute(message)
	if !explicit {
		text = message
		agent = selected
	}
	command, args := splitFirstToken(strings.TrimSpace(text))
	return agent, args, strings.EqualFold(command, "/clear")
}

func (ch *WebUIChannel) handleClear(w http.ResponseWriter, r *http.Request, svc *Service, req TurnRequest, agent extagent.AgentName, args string) {
	if args != "" || len(req.Images)+len(req.Files) != 0 || req.AgentModel != "" || req.AgentEffort != "" || req.AgentServiceTier != "" || req.AgentInteractionMode != "" {
		writeError(w, 400, chatResponse{Error: "/clear 不接受参数、附件或模型设置"})
		return
	}
	if req.ConversationType == ConversationTypeGroup || req.Mode == ChatModeAsk || (agent != "" && agent != builtinChatAgent && !slices.Contains(extagent.SupportedAgents(), agent)) {
		writeError(w, 400, chatResponse{Error: "/clear 仅支持 Run 单聊"})
		return
	}
	status, err := ch.clearContext(svc, req.SessionID, req.TurnID, agent)
	if err != nil {
		writeError(w, status, chatResponse{Error: err.Error()})
		return
	}
	active, err := svc.externalBroker.ActiveAgent(req.SessionID)
	if err != nil {
		writeError(w, 500, chatResponse{Error: err.Error()})
		return
	}
	result := map[string]any{"session_id": req.SessionID, "turn_id": req.TurnID, "mode": "run", "conversation_type": "default", "active_agent": active, "reply_agent_command": "clear", "reply": "上下文已清空"}
	w.Header().Set("Cache-Control", "no-store")
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, 200, result)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	stream := sseStream{writer: w, flusher: w.(http.Flusher)}
	_ = stream.writeEvent("done", result)
}

func (ch *WebUIChannel) clearContext(svc *Service, id, operation string, target extagent.AgentName) (int, error) {
	if id == "" {
		return 200, nil
	} // An unsent draft has no previous context.
	s, err := svc.store.Open(id)
	if errors.Is(err, os.ErrNotExist) {
		return 404, fmt.Errorf("会话不存在")
	}
	if err != nil {
		return 400, err
	}
	if !s.Meta().Chat || validateQueueSession(s) != nil {
		return 400, fmt.Errorf("/clear 仅支持 Run 单聊")
	}
	replay := func() (int, error) {
		if b, found := s.FindClear(operation); found {
			if b.Agent != string(target) {
				return 409, fmt.Errorf("清空操作 ID 已用于不同的 Agent")
			}
			return 200, nil
		}
		return 0, nil
	}
	if status, err := replay(); status != 0 {
		return status, err
	}
	unlock, ok := svc.locker.TryLock(s.ID())
	if !ok {
		return 409, session.ErrContextBusy
	}
	defer unlock()
	gate, err := s.OpenContextLock(true)
	if err != nil {
		if errors.Is(err, session.ErrContextBusy) {
			return 409, err
		}
		return 500, err
	}
	defer gate.Close()
	if err = s.RefreshContexts(); err != nil {
		return 500, err
	}
	if status, err := replay(); status != 0 {
		return status, err
	}
	reg := svc.webTurns
	reg.queueMu.Lock()
	defer reg.queueMu.Unlock()
	if reg.otherActive(id, "") {
		return 409, session.ErrContextBusy
	}
	q, err := reg.readQueue(s, ch.lifetime, true)
	if errors.Is(err, errQueueOwned) {
		return 409, err
	}
	if err != nil {
		return 500, err
	}
	hadWork := q.hasWork()
	committed := false
	defer func() {
		if committed || !hadWork {
			reg.releaseIdleQueue(s, q)
		}
	}()
	err = svc.externalBroker.ClearContext(s, operation, target, func(next *session.Session) error {
		q.holdSubmitted()
		for i := range q.Entries {
			row := &q.Entries[i]
			if row.Kind == "" && (row.State == "failed" || row.State == "returned" || row.State == "stopped" || row.State == "unknown") {
				q.settleReceipt(row, "withdrawn")
				continue
			}
			if row.State != "waiting" {
				continue
			}
			if row.Editing {
				row.BodyVersion++
				row.Editing = false
			}
			if row.Kind == "" {
				q.settleReceipt(row, "withdrawn")
			}
		}
		q.Bindings = nil
		q.Paused = q.hasWaiting()
		q.PauseAt = q.Revision + 1
		return q.writeJournal(next)
	})
	if errors.Is(err, session.ErrContextBusy) {
		return 409, err
	}
	if err != nil {
		return 500, err
	}
	committed = true
	if len(q.released) > 0 {
		q.pruneAttachmentDirectory(s)
	}
	for token := range reg.queueLeases[id].editors {
		reg.queueLeases[id].removeEdit(token)
	}
	reg.notifyQueue(q.view(id, ""))
	return 200, nil
}
