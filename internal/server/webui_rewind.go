package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bqagent/internal/atomicfile"
	"bqagent/internal/extagent"
	"bqagent/internal/privatefile"
	"bqagent/internal/session"
	"bqagent/internal/taskstate"
	"bqagent/internal/transcript"
)

type rewindPoint struct {
	Agent      extagent.AgentName `json:"agent,omitempty"`
	Thread     string             `json:"thread"`
	Turn       string             `json:"turn"`
	Text       string             `json:"text"`
	HasFiles   bool               `json:"has_files,omitempty"`
	HasWorking bool               `json:"has_working,omitempty"`
	Time       time.Time          `json:"time"`
	Task       *taskstate.State   `json:"task,omitempty"`
}

type rewindAction struct {
	Agent     extagent.AgentName `json:"agent"`
	Target    string             `json:"target"`
	Epoch     int                `json:"expected_context"`
	Operation string             `json:"operation,omitempty"`
}

func annotateRewindTargets(s *session.Session, rows []map[string]any, view []ConversationHistoryMessage, active extagent.SessionState) {
	pending, err := s.PendingRewind()
	if err != nil {
		return
	}
	targets := map[string]*rewindAction{}
	for i := len(rows) - 1; i >= 0; i-- {
		row := rows[i]
		if row["role"] != "user" || row[transcript.ExternalInputKey] != nil {
			continue
		}
		id, _ := row[transcript.SubmissionKey].(string)
		p, err := loadRewindPoint(s, id)
		if err != nil || p.Turn == "" || p.Agent != active.Agent || !s.HasRewindAncestor(string(p.Agent), active.ExternalSessionID, p.Thread) {
			break
		}
		action := &rewindAction{Agent: p.Agent, Target: id, Epoch: s.ContextNumber()}
		if pending != nil {
			if pending.Target != id {
				continue
			}
			action.Operation = pending.Operation
		}
		targets[id] = action
	}
	for i := range view {
		view[i].Rewind = targets[view[i].submission]
	}
}

func beginRewindPoint(s *session.Session, text string) (rewindPoint, error) {
	task, err := s.LoadTaskState()
	return rewindPoint{Text: text, Time: time.Now().UTC(), Task: task}, err
}

func (point rewindPoint) save(s *session.Session, id string, state extagent.SessionState, nativeTurn string, files []resolvedFileAttachment, working []map[string]any) error {
	if !validTurnID(id) || state.ExternalSessionID == "" || nativeTurn == "" {
		return errors.New("无法记录没有原生标识的回合")
	}
	point.Thread, point.Turn, point.HasFiles = state.ExternalSessionID, nativeTurn, len(files) > 0
	point.Agent = state.Agent
	root, err := os.OpenRoot(s.Dir())
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.MkdirAll("rewind", privatefile.DirMode); err != nil {
		return err
	}
	prefix := filepath.Join("rewind", id)
	if s.TranscriptMode() == session.TranscriptModeFull {
		if err := saveRewindWorking(root, id, working); err != nil {
			return err
		}
		point.HasWorking = true
	}
	if point.HasFiles {
		payload := make([]webUIFilePayload, 0, len(files))
		for _, f := range files {
			data := base64.StdEncoding.EncodeToString(f.Data)
			payload = append(payload, webUIFilePayload{Name: f.Name, DataBase64: &data})
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if err := atomicfile.WriteRoot(root, prefix+".files.json", data, privatefile.FileMode); err != nil {
			return err
		}
	}
	data, err := json.Marshal(point)
	if err != nil {
		return err
	}
	return atomicfile.WriteRoot(root, prefix+".json", data, privatefile.FileMode)
}

func loadRewindPoint(s *session.Session, id string) (rewindPoint, error) {
	var point rewindPoint
	if !validTurnID(id) || strings.TrimSpace(id) != id {
		return point, os.ErrNotExist
	}
	root, err := os.OpenRoot(s.Dir())
	if err != nil {
		return point, err
	}
	defer root.Close()
	data, err := root.ReadFile(filepath.Join("rewind", id+".json"))
	if err != nil {
		return point, err
	}
	err = json.Unmarshal(data, &point)
	if point.Agent == "" {
		point.Agent = extagent.AgentCodex
	} // Older Codex-only rewind points.
	return point, err
}

type rewindRequest struct {
	Replay    bool   `json:"replay,omitempty"`
	Workspace string `json:"workspace_id"`
	Session   string `json:"session_id"`
	Operation string `json:"operation"`
	Target    string `json:"target"`
	Epoch     *int   `json:"expected_context"`
}

type rewindDraft struct {
	Agent   extagent.AgentName  `json:"agent"`
	Text    string              `json:"text"`
	Images  []webUIImagePayload `json:"images,omitempty"`
	Files   []webUIFilePayload  `json:"files,omitempty"`
	Context int                 `json:"context_number"`
}

func readRewindDraft(s *session.Session, id string, p rewindPoint, row map[string]any) (rewindDraft, error) {
	draft := rewindDraft{Text: p.Text, Agent: p.Agent, Context: s.ContextNumber() + 1}
	if p.HasFiles {
		root, err := os.OpenRoot(s.Dir())
		if err != nil {
			return draft, err
		}
		defer root.Close()
		data, err := root.ReadFile(filepath.Join("rewind", id+".files.json"))
		if err != nil {
			return draft, err
		}
		if err := json.Unmarshal(data, &draft.Files); err != nil {
			return draft, err
		}
	}
	parts, _ := row["content"].([]any)
	for _, part := range parts {
		if mime, data := storedUserImage(part); data != "" {
			draft.Images = append(draft.Images, webUIImagePayload{MIMEType: mime, DataBase64: data})
		}
	}
	return draft, nil
}

func rewindCut(s *session.Session, rows []map[string]any, target string) (int, rewindPoint, []string, error) {
	index := -1
	var first rewindPoint
	var turns []string
	var previousThread string
	for i, row := range rows {
		if row["role"] != "user" || row[transcript.ExternalInputKey] != nil {
			continue
		}
		id, _ := row[transcript.SubmissionKey].(string)
		if index < 0 && id != target {
			continue
		}
		p, err := loadRewindPoint(s, id)
		if err != nil || p.Thread == "" || p.Turn == "" {
			return 0, first, nil, errors.New("此位置之后包含其他 Agent 或无法核对的回合")
		}
		if index < 0 {
			index, first = i, p
		} else if id == target || p.Agent != first.Agent || !s.HasRewindAncestor(string(p.Agent), p.Thread, previousThread) {
			return 0, first, nil, errors.New("回退范围跨越不同的原生线程")
		}
		previousThread = p.Thread
		turns = append(turns, p.Turn)
	}
	if index < 0 {
		return 0, first, nil, errors.New("所选消息已不在当前上下文")
	}
	return index, first, turns, nil
}

func (ch *WebUIChannel) handleRewind(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		writeError(w, 405, chatResponse{Error: "method not allowed"})
		return
	}
	var req rewindRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil || req.Epoch == nil || *req.Epoch < 0 || !validTurnID(req.Operation) || !validTurnID(req.Target) || req.Session == "" {
		writeError(w, 400, chatResponse{Error: "无效的历史回退请求"})
		return
	}
	svc, _, err := ch.resolveService(req.Workspace)
	if err != nil {
		writeError(w, 404, chatResponse{Error: "工作区不存在"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	draft, status, err := ch.rewind(ctx, svc, req, svc.externalBroker.RewindCodex)
	if err != nil {
		writeError(w, status, chatResponse{Error: err.Error()})
		return
	}
	writeJSON(w, 200, draft)
}

type nativeRewind func(context.Context, *session.Session, session.PendingRewind, []string, string, func(*session.Session) error) error

func (ch *WebUIChannel) rewind(ctx context.Context, svc *Service, req rewindRequest, native nativeRewind) (rewindDraft, int, error) {
	var empty rewindDraft
	s, err := svc.store.Open(req.Session)
	if err != nil {
		return empty, 404, err
	}
	if !s.Meta().Chat || validateQueueSession(s) != nil {
		return empty, 400, errors.New("回退仅支持 Run 单聊")
	}
	if req.Epoch == nil || *req.Epoch < 0 {
		return empty, 400, errors.New("缺少回退上下文")
	}
	if *req.Epoch > s.ContextNumber() {
		return empty, 409, errors.New("上下文已变更")
	}
	if b, found := s.FindBoundary(req.Operation); found {
		return committedRewindDraft(s, req, b)
	}
	if req.Replay {
		return empty, 409, errors.New("此回退尚未完成")
	}
	unlock, ok := svc.locker.TryLock(s.ID())
	if !ok {
		return empty, 409, session.ErrContextBusy
	}
	defer unlock()
	gate, err := s.OpenContextLock(true)
	if err != nil {
		return empty, 409, err
	}
	defer gate.Close()
	if err = s.RefreshContexts(); err != nil {
		return empty, 500, err
	}
	if b, found := s.FindBoundary(req.Operation); found {
		return committedRewindDraft(s, req, b)
	}
	if s.ContextNumber() != *req.Epoch {
		return empty, 409, errors.New("上下文已变更，请重新选择消息")
	}
	reg := svc.webTurns
	reg.queueMu.Lock()
	if reg.otherActive(s.ID(), "") {
		reg.queueMu.Unlock()
		return empty, 409, session.ErrContextBusy
	}
	q, err := reg.readQueue(s, ch.lifetime, true)
	if err != nil {
		reg.queueMu.Unlock()
		return empty, 409, err
	}
	if q.hasWork() {
		reg.queueMu.Unlock()
		return empty, 409, errors.New("队列中仍有待处理的消息，请先完成或撤回")
	}
	if reg.rewinding == nil {
		reg.rewinding = make(map[string]bool)
	}
	reg.rewinding[s.ID()] = true
	reg.queueMu.Unlock()
	defer func() {
		reg.queueMu.Lock()
		delete(reg.rewinding, s.ID())
		reg.releaseIdleQueue(s, q)
		reg.queueMu.Unlock()
	}()
	rows, err := s.LoadMessages()
	if err != nil {
		return empty, 500, err
	}
	i, p, turns, err := rewindCut(s, rows, req.Target)
	if err != nil {
		return empty, 409, err
	}
	if p.Agent != extagent.AgentCodex {
		return empty, 409, errors.New("仅 Codex 支持历史回退")
	}
	draft, err := readRewindDraft(s, req.Target, p, rows[i])
	if err != nil {
		return empty, 500, err
	}
	intent := session.PendingRewind{Agent: string(p.Agent), Operation: req.Operation, Target: req.Target, Thread: p.Thread, Before: p.Turn}
	var working []map[string]any
	if p.HasWorking {
		working, err = loadRewindWorking(s, req.Target)
		if err != nil {
			return empty, 500, err
		}
	}
	err = native(ctx, s, intent, turns, svc.workspaceRoot, func(next *session.Session) error {
		if err := next.RewriteMessages(rows[:i]); err != nil {
			return err
		}
		if p.HasWorking && next.TranscriptMode() == session.TranscriptModeFull {
			if err := next.SaveWorkingMessages(working); err != nil {
				return err
			}
		}
		if p.Task != nil {
			if err := next.SaveTaskState(*p.Task); err != nil {
				return err
			}
		}
		for id, h := range q.History {
			if h.Context == *req.Epoch && !h.Recorded.After(p.Time) {
				h.Context = next.ContextNumber()
				q.History[id] = h
			}
		}
		q.Bindings = nil
		return q.writeJournal(next)
	})
	if err != nil {
		return empty, 409, err
	}
	reg.notifyQueue(q.view(s.ID(), ""))
	return draft, 200, nil
}

// A committed receipt pins the removed prompt to its original immutable epoch.
// Replay remains read-only even after another clear or rewind.
func committedRewindDraft(s *session.Session, req rewindRequest, b session.ContextBoundary) (rewindDraft, int, error) {
	var empty rewindDraft
	if *req.Epoch >= s.ContextNumber() || b != s.BoundaryAfter(*req.Epoch) || b.RewindTo != req.Target {
		return empty, 409, errors.New("回退操作 ID 已用于其他位置")
	}
	source, _ := s.ContextAt(*req.Epoch)
	rows, err := source.LoadMessages()
	if err != nil {
		return empty, 500, err
	}
	i, p, _, err := rewindCut(source, rows, req.Target)
	if err != nil {
		return empty, 409, err
	}
	draft, err := readRewindDraft(source, req.Target, p, rows[i])
	if err != nil {
		return empty, 500, err
	}
	return draft, 200, nil
}
