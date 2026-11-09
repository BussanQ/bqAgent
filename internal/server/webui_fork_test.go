package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"bqagent/internal/extagent"
	"bqagent/internal/session"
	"bqagent/internal/taskstate"
	"bqagent/internal/transcript"
)

func TestWebUIConversationForkCopiesFinishedReplyAndReturnsToSource(t *testing.T) {
	for _, mode := range []session.TranscriptMode{session.TranscriptModeCompact, session.TranscriptModeFull} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			opts := session.Options{TranscriptMode: mode}
			states := extagent.NewStateStore(root, opts)
			broker := extagent.NewBroker(states, nil, nil)
			defer broker.Close()
			svc := NewService(ServiceOptions{WorkspaceRoot: root, SessionOptions: &opts, ExternalBroker: broker, SystemPrompt: "test"})
			s, err := svc.store.Create(session.CreateOptions{Chat: true, Task: "Review"})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.RecordMessage(map[string]any{"role": "user", "content": "previous context"}); err != nil {
				t.Fatal(err)
			}
			if err := s.StartContext("clear", "codex", func(*session.Session) error { return nil }); err != nil {
				t.Fatal(err)
			}
			state := extagent.SessionState{BQSessionID: s.ID(), Agent: extagent.AgentCodex, Transport: extagent.TransportAppServer, ExternalSessionID: "native", Model: "source-model", Effort: "high"}
			if err := states.Save(state); err != nil {
				t.Fatal(err)
			}
			text := func(role, content string) map[string]any { return map[string]any{"role": role, "content": content} }
			user := text("user", "same")
			user["content"] = []any{map[string]any{"type": "text", "text": "same"}, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,YWJj"}}}
			user[transcript.SubmissionKey] = "root"
			answer := text("assistant", "same reply")
			answer["name"] = "codex"
			answer[transcript.ExternalHistoryKey] = transcript.ExternalHistory{ID: "segment", Parts: []transcript.Part{{Kind: "text", ID: "reply", Text: "same reply"}}}
			if err := s.RecordMessages(user, answer); err != nil {
				t.Fatal(err)
			}
			p, err := beginRewindPoint(s, "original")
			if err != nil {
				t.Fatal(err)
			}
			if err := p.save(s, "root", state, "native-turn", []resolvedFileAttachment{{Name: "saved.txt", Data: []byte("file")}}, nil); err != nil {
				t.Fatal(err)
			}
			task := taskstate.State{Goal: "at cut", Status: "active", Criteria: []taskstate.Criterion{{Description: "done"}}}
			if err := s.SaveTaskState(task); err != nil {
				t.Fatal(err)
			}
			working := []map[string]any{user, answer}
			if mode == session.TranscriptModeFull {
				working = []map[string]any{text("developer", "summary at cut"), answer}
			}
			action, err := saveReplyCheckpoint(s, "segment", "root", extagent.TurnResponse{State: state}, working)
			if err != nil || action == nil {
				t.Fatal(action, err)
			}
			if err := s.RecordMessages(text("user", "later"), text("assistant", "later answer")); err != nil {
				t.Fatal(err)
			}
			task.Goal = "newer task"
			if err := s.SaveTaskState(task); err != nil {
				t.Fatal(err)
			}
			source, err := svc.conversationHistory(s.ID(), 0, true)
			if err != nil || len(source.Messages) != 6 || source.Messages[3].Fork == nil || source.Messages[3].Anchor != "segment" {
				t.Fatal(source.Messages, err)
			}
			req := forkRequest{Session: s.ID(), Epoch: &action.Epoch, Target: action.Target, Operation: "copy-id"}
			copies := 0
			copyNative := func(_ context.Context, child *session.Session, _, _ string, p extagent.ForkCheckpoint) (extagent.ForkBinding, error) {
				copies++
				if p.Turn != "native-turn" || p.State != state {
					t.Fatal(p)
				}
				b := extagent.ForkBinding{State: p.State, IDs: map[string]string{p.Turn: "child-turn"}}
				b.State.ExternalSessionID, b.State.BQSessionID = "child-native", child.ID()
				data, _ := json.Marshal(b.State)
				err := os.WriteFile(filepath.Join(child.Dir(), "external-agent.json"), data, 0600)
				return b, err
			}
			// A newer source turn can be live while copying an earlier checkpoint.
			gate, err := s.OpenContextLock(false)
			if err != nil {
				t.Fatal(err)
			}
			id, status, err := forkConversation(t.Context(), svc, req, copyNative)
			gate.Close()
			if err != nil || status != 200 || id == s.ID() {
				t.Fatal(id, status, err)
			}
			if got, _, err := forkConversation(t.Context(), svc, req, copyNative); err != nil || got != id || copies != 1 {
				t.Fatal(got, err, copies)
			}
			child, err := svc.store.Open(id)
			if err != nil {
				t.Fatal(err)
			}
			if task, err := child.LoadTaskState(); err != nil || task.Goal != "at cut" {
				t.Fatal(task, err)
			}
			bound, err := states.Load(id)
			if err != nil || bound.ExternalSessionID != "child-native" || bound.Model != state.Model || bound.Effort != state.Effort {
				t.Fatal(bound, err)
			}
			if orig, _ := states.Load(s.ID()); orig != state {
				t.Fatal("changed parent", orig)
			}
			rows, err := child.LoadMessages()
			if err != nil || len(rows) != 2 {
				t.Fatal(rows, err)
			}
			loaded, _, err := child.LoadResumableMessages()
			if err != nil || len(loaded) != 2 || mode == session.TranscriptModeFull && loaded[0]["content"] != "summary at cut" {
				t.Fatal(loaded, err)
			}
			inherited, err := loadRewindPoint(child, "root")
			if err != nil || inherited.Turn != "child-turn" || inherited.Thread != "child-native" {
				t.Fatal(inherited, err)
			}
			draft, err := readRewindDraft(child, "root", inherited, rows[0])
			if err != nil || len(draft.Images) != 1 || len(draft.Files) != 1 {
				t.Fatal(draft, err)
			}
			view, err := svc.conversationHistory(id, 0, true)
			if err != nil || view.ForkedFrom.Session != s.ID() || view.ForkedFrom.Target != "segment" || len(view.Messages) != 2 || view.Messages[1].Fork == nil {
				t.Fatal(view, err)
			}
			parentRows, _ := s.LoadMessages()
			if len(parentRows) != 4 {
				t.Fatal("truncated parent")
			}
			other := req
			other.Target = "changed"
			if _, status, _ := forkConversation(t.Context(), svc, other, copyNative); status != 409 {
				t.Fatal(status)
			}
			if err := svc.store.Delete(s.ID()); err != nil {
				t.Fatal(err)
			}
			ch := NewWebUIChannel(svc, true)
			body, _ := json.Marshal(req)
			out := httptest.NewRecorder()
			ch.handleFork(out, httptest.NewRequest(http.MethodPost, "/api/v1/webui/fork", bytes.NewReader(body)))
			if out.Code != 200 || !bytes.Contains(out.Body.Bytes(), []byte(id)) {
				t.Fatal(out.Code, out.Body.String())
			}
			h, err := svc.conversationHistory(id, 0, true)
			if err != nil || !slices.Equal(h.Messages[0].Images, view.Messages[0].Images) {
				t.Fatal(err, h.Messages)
			}
		})
	}
}

func TestWebUIForkRefusesChangedPrefixBeforePublishing(t *testing.T) {
	svc := NewService(ServiceOptions{WorkspaceRoot: t.TempDir(), SystemPrompt: "test"})
	s, err := svc.store.Create(session.CreateOptions{Chat: true, Task: "changed"})
	if err != nil {
		t.Fatal(err)
	}
	user := map[string]any{"role": "user", "content": "a", transcript.SubmissionKey: "u"}
	reply := map[string]any{"role": "assistant", "content": "b", "name": "codex", transcript.ExternalHistoryKey: transcript.ExternalHistory{ID: "a", Parts: []transcript.Part{{Kind: "text", ID: "t", Text: "b"}}}}
	if err := s.RecordMessages(user, reply); err != nil {
		t.Fatal(err)
	}
	state := extagent.SessionState{Agent: extagent.AgentCodex, Transport: extagent.TransportAppServer, ExternalSessionID: "n"}
	p, err := beginRewindPoint(s, "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.save(s, "u", state, "t", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := saveReplyCheckpoint(s, "a", "u", extagent.TurnResponse{State: state}, []map[string]any{user, reply}); err != nil {
		t.Fatal(err)
	}
	epoch := 0
	req := forkRequest{Epoch: &epoch, Session: s.ID(), Target: "a", Operation: "op"}
	fake := func(_ context.Context, child *session.Session, _, _ string, _ extagent.ForkCheckpoint) (extagent.ForkBinding, error) {
		user["content"] = "moved"
		if err := s.RewriteMessages([]map[string]any{user, reply}); err != nil {
			t.Fatal(err)
		}
		next := state
		next.BQSessionID, next.ExternalSessionID = child.ID(), "copy"
		return extagent.ForkBinding{State: next, IDs: map[string]string{"t": "t"}}, nil
	}
	if _, status, err := forkConversation(t.Context(), svc, req, fake); status != 409 || err == nil {
		t.Fatal(status, err)
	}
	meta, err := svc.store.List(0)
	if err != nil || len(meta) != 1 {
		t.Fatal(meta, err)
	}
}

type forkTipACP struct{ groupFakeACP }

func (*forkTipACP) SupportsConversationFork() bool { return true }

func TestWebUIOpenCodeOffersOnlyLatestSettledReplyForFork(t *testing.T) {
	root := t.TempDir()
	spec := &extagent.AgentTransport{Agent: extagent.AgentOpenCode, Kind: extagent.TransportACP, Command: extagent.CommandSpec{Command: "opencode"}}
	log := &groupACPLog{prompts: map[string][]string{}}
	b := extagent.NewBroker(extagent.NewStateStore(root), map[extagent.AgentName]extagent.DetectionResult{extagent.AgentOpenCode: {Agent: extagent.AgentOpenCode, Preferred: spec, ACP: spec}},
		func(context.Context, extagent.CommandSpec, string) (extagent.ACPClient, error) {
			return &forkTipACP{groupFakeACP{name: "opencode", log: log}}, nil
		})
	defer b.Close()
	svc := NewService(ServiceOptions{WorkspaceRoot: root, ExternalBroker: b, SystemPrompt: "test"})
	first, err := svc.HandleTurn(t.Context(), TurnRequest{Agent: extagent.AgentOpenCode, Message: "same"})
	if err != nil || first.Fork == nil || !first.Fork.Tip || first.Rewind != nil {
		t.Fatal(first, err)
	}
	second, err := svc.HandleTurn(t.Context(), TurnRequest{SessionID: first.SessionID, Agent: extagent.AgentOpenCode, Message: "same"})
	if err != nil || second.Fork == nil {
		t.Fatal(second, err)
	}
	h, err := svc.conversationHistory(first.SessionID, 0, true)
	if err != nil || len(h.Messages) != 4 || h.Messages[1].Fork != nil || h.Messages[3].Fork == nil || !h.Messages[3].Fork.Tip {
		t.Fatal(h.Messages, err)
	}
	epoch := 0
	req := forkRequest{Session: first.SessionID, Epoch: &epoch, Target: first.Fork.Target, Operation: "tip"}
	native := func(_ context.Context, s *session.Session, _, _ string, p extagent.ForkCheckpoint) (extagent.ForkBinding, error) {
		if !p.Tip || p.Turn != second.Fork.Target {
			t.Fatal("called native for an earlier response", p)
		}
		p.State.BQSessionID, p.State.ExternalSessionID = s.ID(), "new-acp"
		data, _ := json.Marshal(p.State)
		err := os.WriteFile(filepath.Join(s.Dir(), "external-agent.json"), data, 0600)
		return extagent.ForkBinding{State: p.State}, err
	}
	if _, status, _ := forkConversation(t.Context(), svc, req, native); status != 409 {
		t.Fatal(status)
	}
	req.Target = second.Fork.Target
	source, _ := svc.store.Open(req.Session)
	f, err := source.OpenContextLock(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, status, _ := forkConversation(t.Context(), svc, req, native); status != 409 {
		t.Fatal(status)
	}
	f.Close()
	id, status, err := forkConversation(t.Context(), svc, req, native)
	if status != 200 || err != nil {
		t.Fatal(status, err)
	}
	view, err := svc.conversationHistory(id, 0, true)
	if err != nil || len(view.Messages) != 4 || view.Messages[3].Fork == nil || !view.Messages[3].Fork.Tip {
		t.Fatal(view.Messages, err)
	}
}

func TestWebUIRejectsLegacyClaudeForkCheckpoint(t *testing.T) {
	root := t.TempDir()
	broker := extagent.NewBroker(extagent.NewStateStore(root), nil, nil)
	defer broker.Close()
	svc := NewService(ServiceOptions{WorkspaceRoot: root, ExternalBroker: broker})
	chat, err := svc.store.Create(session.CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.OpenRoot(chat.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	point := replyCheckpoint{Root: "prompt", Native: extagent.ForkCheckpoint{State: extagent.SessionState{Agent: extagent.AgentClaude, ExternalSessionID: "native"}}}
	if err := writeReplyCheckpoint(dir, "reply", point); err != nil {
		t.Fatal(err)
	}
	rows := []ConversationHistoryMessage{{submission: "prompt"}, {Anchor: "reply"}}
	annotateForks(chat, rows, nil, false)
	if rows[1].Fork != nil {
		t.Fatal("offered Claude fork")
	}
	epoch := chat.ContextNumber()
	_, code, err := forkConversation(t.Context(), svc, forkRequest{Session: chat.ID(), Epoch: &epoch, Target: "reply", Operation: "op"}, nil)
	if code != 409 || err == nil || err.Error() != "Claude 不支持从历史回复创建分支" {
		t.Fatal(code, err)
	}
}
