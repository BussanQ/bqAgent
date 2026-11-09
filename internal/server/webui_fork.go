package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
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

type forkAction struct {
	Tip    bool   `json:"tip,omitempty"`
	Target string `json:"target"`
	Epoch  int    `json:"expected_context"`
}

type replyCheckpoint struct {
	Native  extagent.ForkCheckpoint `json:"native"`
	Root    string                  `json:"root"`
	Meta    session.Meta            `json:"meta"`
	Task    *taskstate.State        `json:"task,omitempty"`
	Working bool                    `json:"working,omitempty"`
	At      time.Time               `json:"at"`
}

func saveReplyCheckpoint(s *session.Session, id, rootID string, result extagent.TurnResponse, working []map[string]any) (*forkAction, error) {
	native := extagent.ForkCheckpoint{State: result.State, Turn: id, Tip: result.ForkTip}
	if !validTurnID(id) || !validTurnID(rootID) || result.State.ExternalSessionID == "" {
		return nil, errors.New("回复缺少已确认的会话位置")
	}
	if !native.Tip {
		point, err := loadRewindPoint(s, rootID)
		if err != nil {
			return nil, err
		}
		if point.Thread != result.State.ExternalSessionID || point.Agent != result.State.Agent {
			return nil, errors.New("回复与原生会话不一致")
		}
		native.Turn = point.Turn
	}
	task, err := s.LoadTaskState()
	if err != nil {
		return nil, err
	}
	p := replyCheckpoint{Root: rootID, At: time.Now().UTC(), Meta: s.Meta(), Task: task, Working: s.TranscriptMode() == session.TranscriptModeFull,
		Native: native}
	if s.TranscriptMode() == session.TranscriptModeCompact {
		if _, err := forkPrefix(working, id, p); err != nil {
			return nil, nil
		}
	}
	root, err := os.OpenRoot(s.Dir())
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if p.Working {
		if err := saveRewindWorking(root, "reply-"+id, working); err != nil {
			return nil, err
		}
	}
	if err := writeReplyCheckpoint(root, id, p); err != nil {
		return nil, err
	}
	return &forkAction{Target: id, Epoch: s.ContextNumber(), Tip: native.Tip}, nil
}

func writeReplyCheckpoint(root *os.Root, id string, p replyCheckpoint) error {
	if err := root.MkdirAll("fork-points", privatefile.DirMode); err != nil {
		return err
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return atomicfile.WriteRoot(root, filepath.Join("fork-points", id+".json"), data, privatefile.FileMode)
}

func readReplyCheckpoint(root *os.Root, id string) (replyCheckpoint, error) {
	var p replyCheckpoint
	if !validTurnID(id) || strings.TrimSpace(id) != id {
		return p, os.ErrNotExist
	}
	data, err := root.ReadFile(filepath.Join("fork-points", id+".json"))
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(data, &p)
	return p, err
}

func annotateForks(s *session.Session, view []ConversationHistoryMessage, rows []map[string]any, allowTip bool) {
	root, err := os.OpenRoot(s.Dir())
	if err != nil {
		return
	}
	defer root.Close()
	seen := map[string]bool{}
	var tip string
	if allowTip && len(rows) > 0 {
		if h, ok := transcript.ReadExternalHistory(rows[len(rows)-1][transcript.ExternalHistoryKey]); ok {
			tip = h.ID
		}
	}
	for i := range view {
		m := &view[i]
		if m.submission != "" {
			seen[m.submission] = true
		}
		if m.Anchor == "" {
			continue
		}
		p, err := readReplyCheckpoint(root, m.Anchor)
		if err == nil && seen[p.Root] && (!p.Native.Tip || m.Anchor == tip) && p.Native.State.Agent != extagent.AgentClaude {
			m.Fork = &forkAction{Target: m.Anchor, Epoch: s.ContextNumber(), Tip: p.Native.Tip}
		}
	}
}

func forkPrefix(rows []map[string]any, id string, p replyCheckpoint) ([]map[string]any, error) {
	var root string
	var end int
	for i, row := range rows {
		if row["role"] == "user" && row[transcript.ExternalInputKey] == nil {
			root, _ = row[transcript.SubmissionKey].(string)
		}
		h, ok := transcript.ReadExternalHistory(row[transcript.ExternalHistoryKey])
		if !ok || h.ID != id {
			continue
		}
		if end != 0 || root != p.Root || row["role"] != "assistant" || row["name"] != string(p.Native.State.Agent) {
			return nil, errors.New("无法核对所选回复的回合")
		}
		end = i + 1
	}
	if end == 0 {
		return nil, errors.New("所选回复已不在当前上下文")
	}
	return rows[:end], nil
}

type forkRequest struct {
	Workspace string `json:"workspace_id"`
	Session   string `json:"session_id"`
	Epoch     *int   `json:"expected_context"`
	Target    string `json:"target"`
	Operation string `json:"operation"`
}

type forkNative func(context.Context, *session.Session, string, string, extagent.ForkCheckpoint) (extagent.ForkBinding, error)

func (ch *WebUIChannel) handleFork(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		writeError(w, 405, chatResponse{Error: "method not allowed"})
		return
	}
	var req forkRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil || req.Epoch == nil || *req.Epoch < 0 || !validTurnID(req.Target) || !validTurnID(req.Operation) || req.Session == "" {
		writeError(w, 400, chatResponse{Error: "无效的聊天分支请求"})
		return
	}
	svc, _, err := ch.resolveService(req.Workspace)
	if err != nil {
		writeError(w, 404, chatResponse{Error: "工作区不存在"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	id, status, err := forkConversation(ctx, svc, req, svc.externalBroker.ForkConversation)
	if err != nil {
		writeError(w, status, chatResponse{Error: err.Error()})
		return
	}
	writeJSON(w, 200, map[string]string{"session_id": id})
}

func forkConversation(ctx context.Context, svc *Service, req forkRequest, native forkNative) (string, int, error) {
	sourceID, err := session.CanonicalID(req.Session)
	if err != nil {
		return "", 400, err
	}
	origin := session.ForkOrigin{Session: sourceID, Epoch: *req.Epoch, Target: req.Target, Operation: req.Operation}
	if child, err := svc.store.ExistingFork(origin); err != nil {
		return "", 409, err
	} else if child != nil {
		return child.ID(), 200, nil
	}
	s, err := svc.store.Open(sourceID)
	if err != nil {
		return "", 404, err
	}
	if !s.Meta().Chat || validateQueueSession(s) != nil {
		return "", 400, errors.New("分支仅支持 Run 单聊")
	}
	root, err := os.OpenRoot(s.Dir())
	if err != nil {
		return "", 500, err
	}
	defer root.Close()
	point, err := readReplyCheckpoint(root, req.Target)
	if err != nil {
		return "", 409, errors.New("这条回复没有已确认的原生分支位置")
	}
	if point.Native.State.Agent == extagent.AgentClaude {
		return "", 409, errors.New("Claude 不支持从历史回复创建分支")
	}
	if point.Native.Tip {
		unlock, ok := svc.locker.TryLock(s.ID())
		if !ok {
			return "", 409, session.ErrContextBusy
		}
		defer unlock()
	}
	gate, err := s.OpenContextLock(point.Native.Tip)
	if err != nil {
		return "", 409, err
	}
	defer gate.Close()
	if point.Native.Tip {
		if err := s.RefreshContexts(); err != nil {
			return "", 500, err
		}
		intent, err := s.PendingRewind()
		if err != nil {
			return "", 500, err
		}
		if intent != nil {
			return "", 409, session.ErrRewindPending
		}
		q, err := readCommandQueue(s, "")
		if err != nil {
			return "", 500, err
		}
		if q.hasWork() || svc.webTurns.otherActive(s.ID(), "") {
			return "", 409, errors.New("OpenCode 只能从空闲聊天的最新回复创建分支")
		}
	}
	if s.ContextNumber() != origin.Epoch {
		return "", 409, errors.New("来源上下文已变更，请重新选择回复")
	}
	rows, err := s.LoadMessages()
	if err != nil {
		return "", 500, err
	}
	prefix, err := forkPrefix(rows, req.Target, point)
	if err != nil {
		return "", 409, err
	}
	if point.Native.Tip && len(prefix) != len(rows) {
		return "", 409, errors.New("OpenCode 只能从最新完成的回复创建分支")
	}
	meta := point.Meta
	title := []rune(strings.TrimSpace(s.Meta().Task))
	if len(title) > 45 {
		title = title[:45]
	}
	meta.Task = string(title) + " · 分支"
	child, err := svc.store.CreateFork(origin, meta, func(child *session.Session, opDir string) error {
		if err := child.RewriteMessages(prefix); err != nil {
			return err
		}
		if point.Working && child.TranscriptMode() == session.TranscriptModeFull {
			working, err := loadRewindWorking(s, "reply-"+req.Target)
			if err != nil {
				return err
			}
			if err := child.SaveWorkingMessages(working); err != nil {
				return err
			}
		}
		if point.Task != nil {
			if err := child.SaveTaskState(*point.Task); err != nil {
				return err
			}
		}
		if err := copyForkHandoffs(s, child, point.At); err != nil {
			return err
		}
		branch, err := native(ctx, child, opDir, svc.workspaceRoot, point.Native)
		if err != nil {
			return err
		}
		if branch.State.ExternalSessionID == "" || branch.State.ExternalSessionID == point.Native.State.ExternalSessionID || branch.State.BQSessionID != child.ID() || branch.State.Agent != point.Native.State.Agent || !point.Native.Tip && branch.IDs[point.Native.Turn] == "" {
			return errors.New("Agent 未确认独立的分支会话")
		}
		if err := copyForkPoints(s, child, prefix, point.Native, branch); err != nil {
			return err
		}
		// A later turn may append while the native copy runs. Only a change
		// to the inherited prefix invalidates the selected checkpoint.
		now, err := s.LoadMessages()
		if err != nil {
			return err
		}
		current, err := forkPrefix(now, req.Target, point)
		if err != nil {
			return err
		}
		if point.Native.Tip {
			active, err := svc.externalBroker.ActiveSession(s.ID())
			if err != nil {
				return err
			}
			if len(current) != len(now) || active.Agent != point.Native.State.Agent || active.ExternalSessionID != point.Native.State.ExternalSessionID {
				return errors.New("OpenCode 来源会话已变更，请重新选择最新回复")
			}
		}
		before, err := forkPrefixDigest(prefix)
		if err != nil {
			return err
		}
		after, err := forkPrefixDigest(current)
		if err != nil {
			return err
		}
		if before != after {
			return errors.New("来源历史已变更，请重新选择回复")
		}
		return nil
	})
	if errors.Is(err, extagent.ErrForkUnknown) {
		return "", http.StatusFailedDependency, err
	}
	if errors.Is(err, session.ErrForkDeleted) {
		return "", http.StatusGone, err
	}
	if err != nil {
		return "", 409, err
	}
	return child.ID(), 200, nil
}

func forkPrefixDigest(rows []map[string]any) (string, error) {
	h := sha256.New()
	enc := json.NewEncoder(h)
	for _, row := range rows {
		if err := enc.Encode(row); err != nil {
			return "", err
		}
	}
	return string(h.Sum(nil)), nil
}

func copyForkHandoffs(source, child *session.Session, through time.Time) error {
	old, err := readCommandQueue(source, "")
	if err != nil {
		return err
	}
	q := commandQueue{History: make(map[string]queueHandoffHistory)}
	for id, h := range old.History {
		if h.Context != source.ContextNumber() || h.Recorded.After(through) {
			continue
		}
		if h.Blob != "" {
			payload, err := readQueueBundle(source, h.Blob)
			if err != nil {
				return err
			}
			blob, err := writeQueueBundle(child, payload)
			if err != nil {
				return err
			}
			h.Blob = blob
		}
		h.Context = 0
		q.History[id] = h
	}
	if len(q.History) == 0 {
		return nil
	}
	return q.writeJournal(child)
}

// Only native-backed points retained by the copy can become actions in the
// child. Files, working snapshots and tool rows remain usable after the source
// BQ session is deleted. Pending queue entries and native bindings stay local.
func copyForkPoints(s, child *session.Session, rows []map[string]any, native extagent.ForkCheckpoint, b extagent.ForkBinding) error {
	src, err := os.OpenRoot(s.Dir())
	if err != nil {
		return err
	}
	defer src.Close()
	dest, err := os.OpenRoot(child.Dir())
	if err != nil {
		return err
	}
	defer dest.Close()
	if err := dest.MkdirAll("rewind", privatefile.DirMode); err != nil {
		return err
	}
	for _, row := range rows {
		if id, ok := row[transcript.SubmissionKey].(string); ok && row["role"] == "user" {
			p, err := loadRewindPoint(s, id)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			mapped := b.IDs[p.Turn]
			if p.Agent != b.State.Agent || p.Thread != native.State.ExternalSessionID || mapped == "" {
				continue
			}
			p.Thread, p.Turn = b.State.ExternalSessionID, mapped
			if p.HasWorking {
				if err := copyWorkSnapshot(s, dest, id); err != nil {
					return err
				}
			}
			if p.HasFiles {
				path := filepath.Join("rewind", id+".files.json")
				data, err := src.ReadFile(path)
				if err != nil {
					return err
				}
				if err := atomicfile.WriteRoot(dest, path, data, privatefile.FileMode); err != nil {
					return err
				}
			}
			data, err := json.Marshal(p)
			if err != nil {
				return err
			}
			if err := atomicfile.WriteRoot(dest, filepath.Join("rewind", id+".json"), data, privatefile.FileMode); err != nil {
				return err
			}
		}
		h, ok := transcript.ReadExternalHistory(row[transcript.ExternalHistoryKey])
		if !ok || row["role"] != "assistant" {
			continue
		}
		p, err := readReplyCheckpoint(src, h.ID)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		old := &p.Native
		if old.State.Agent != b.State.Agent || old.State.ExternalSessionID != native.State.ExternalSessionID {
			continue
		}
		if old.Tip {
			if !native.Tip || h.ID != native.Turn {
				continue
			}
		} else {
			if b.IDs[old.Turn] == "" {
				continue
			}
			old.Turn = b.IDs[old.Turn]
		}
		old.State.BQSessionID, old.State.ExternalSessionID = child.ID(), b.State.ExternalSessionID
		old.State.ForkBinding = true
		if p.Working {
			if err := copyWorkSnapshot(s, dest, "reply-"+h.ID); err != nil {
				return err
			}
		}
		if err := writeReplyCheckpoint(dest, h.ID, p); err != nil {
			return err
		}
	}
	return nil
}

func copyWorkSnapshot(source *session.Session, dest *os.Root, id string) error {
	rows, err := loadRewindWorking(source, id)
	if err != nil {
		return fmt.Errorf("读取分支工作上下文：%w", err)
	}
	return saveRewindWorking(dest, id, rows)
}
