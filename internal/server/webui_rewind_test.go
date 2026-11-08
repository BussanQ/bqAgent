package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"bqagent/internal/extagent"
	"bqagent/internal/session"
	"bqagent/internal/transcript"
)

func TestWebUIRewindKeepsOnlyRetainedHistoryAndRestoresExactDraft(t *testing.T) {
	for _, mode := range []session.TranscriptMode{session.TranscriptModeCompact, session.TranscriptModeFull} {
		t.Run(string(mode), func(t *testing.T) {
			options := session.Options{TranscriptMode: mode}
			root := t.TempDir()
			states := extagent.NewStateStore(root, options)
			broker := extagent.NewBroker(states, nil, nil)
			defer broker.Close()
			svc := NewService(ServiceOptions{WorkspaceRoot: root, SessionOptions: &options, SystemPrompt: "stable", ExternalBroker: broker})
			ch := NewWebUIChannel(svc, true)
			s, err := svc.store.Create(session.CreateOptions{Chat: true, Task: "history"})
			if err != nil {
				t.Fatal(err)
			}
			row := func(id, text string) map[string]any {
				return map[string]any{"role": "user", "content": text, transcript.SubmissionKey: id}
			}
			if err := s.RecordMessage(row("old", "before clear")); err != nil {
				t.Fatal(err)
			}
			if err := s.StartContext("clear", "", func(*session.Session) error { return nil }); err != nil {
				t.Fatal(err)
			}
			first := row("first", "same")
			removed := row("second", "same")
			removed["content"] = []any{map[string]any{"type": "text", "text": "same"}, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,YWJj"}}}
			reply := map[string]any{"role": "assistant", "content": "kept reply"}
			correction := map[string]any{"role": "user", "content": "steered", transcript.ExternalInputKey: "correction"}
			if err := s.RecordMessages(first, reply, removed, correction, row("third", "later")); err != nil {
				t.Fatal(err)
			}
			state := extagent.SessionState{BQSessionID: s.ID(), Agent: extagent.AgentCodex, Transport: extagent.TransportAppServer, ExternalSessionID: "native"}
			if err := states.Save(state); err != nil {
				t.Fatal(err)
			}
			files := []resolvedFileAttachment{{Name: "snapshot.txt", Data: bytes.Repeat([]byte("x"), maxAttachmentInlineBytes+1)}}
			point, err := beginRewindPoint(s, "/codex same")
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"first", "second", "third"} {
				if err := point.save(s, id, state, "native-"+id, files, []map[string]any{{"role": "developer", "content": "earlier summary"}, first}); err != nil {
					t.Fatal(err)
				}
			}
			original, err := svc.conversationHistory(s.ID(), 0, true)
			if err != nil || original.Messages[0].Rewind != nil || original.Messages[4].Rewind == nil || original.Messages[4].Rewind.Target != "second" || original.Messages[5].Rewind != nil {
				t.Fatal(err, original.Messages)
			}
			epoch := 1
			req := rewindRequest{Session: s.ID(), Epoch: &epoch, Operation: "op", Target: "second"}
			count := 0
			apply := func(_ context.Context, s *session.Session, p session.PendingRewind, ids []string, _ string, prepare func(*session.Session) error) error {
				count++
				if p.Before != "native-second" || !slices.Equal(ids, []string{"native-second", "native-third"}) {
					t.Fatal(p, ids)
				}
				return s.RewindContext(p.Operation, "codex", p.Target, prepare)
			}
			draft, status, err := ch.rewind(t.Context(), svc, req, apply)
			if err != nil || status != 200 || draft.Context != 2 || draft.Text != "/codex same" || len(draft.Files) != 1 || len(draft.Images) != 1 || draft.Images[0].DataBase64 != "YWJj" {
				t.Fatal(status, err, draft.Text)
			}
			data, err := base64.StdEncoding.DecodeString(*draft.Files[0].DataBase64)
			if err != nil || !bytes.Equal(data, files[0].Data) {
				t.Fatal("truncated restored file")
			}
			got, err := svc.conversationHistory(s.ID(), 0, true)
			if err != nil || got.ContextNumber != 2 || len(got.Messages) != 4 || got.Messages[0].Content != "before clear" || got.Messages[1].Kind != "context_clear" || got.Messages[2].Content != "same" || got.Messages[3].Content != "kept reply" || got.RewindReceipt == nil || got.RewindReceipt.Target != "second" {
				t.Fatal(err, got)
			}
			reopened, err := svc.store.Open(s.ID())
			if err != nil {
				t.Fatal(err)
			}
			work, _, err := reopened.LoadResumableMessages()
			if err != nil || len(work) != 2 || (mode == session.TranscriptModeCompact && work[0][transcript.SubmissionKey] != "first") || (mode == session.TranscriptModeFull && work[0]["content"] != "earlier summary") {
				t.Fatal(err, work)
			}
			old, err := s.LoadMessages()
			if err != nil || len(old) != 5 {
				t.Fatal("lost immutable baseline", err, old)
			}
			req.Replay = true
			body, _ := json.Marshal(req)
			w := httptest.NewRecorder()
			ch.handleRewind(w, httptest.NewRequest(http.MethodPost, "/api/v1/webui/rewind", bytes.NewReader(body)))
			var replay rewindDraft
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &replay) != nil || replay.Context != 2 || replay.Text != draft.Text || count != 1 {
				t.Fatal(w.Code, replay.Text)
			}
			req.Target = "first"
			if _, status, _ := ch.rewind(t.Context(), svc, req, apply); status != 409 {
				t.Fatal(status)
			}
			req.Operation = "different"
			if _, status, _ := ch.rewind(t.Context(), svc, req, apply); status != 409 || count != 1 {
				t.Fatal("stale epoch", status, count)
			}
		})
	}
}

func TestWebUIRewindRejectsMixedTailAndBusyContexts(t *testing.T) {
	workspace := t.TempDir()
	states := extagent.NewStateStore(workspace)
	broker := extagent.NewBroker(states, nil, nil)
	defer broker.Close()
	svc := NewService(ServiceOptions{WorkspaceRoot: workspace, SystemPrompt: "stable", ExternalBroker: broker})
	ch := NewWebUIChannel(svc, true)
	s, err := svc.store.Create(session.CreateOptions{Task: "mixed", Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	root := map[string]any{"role": "user", "content": "same", transcript.SubmissionKey: "codex"}
	if err := s.RecordMessage(root); err != nil {
		t.Fatal(err)
	}
	point, err := beginRewindPoint(s, "same")
	if err != nil {
		t.Fatal(err)
	}
	if err := point.save(s, "codex", extagent.SessionState{ExternalSessionID: "thread"}, "cut", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := states.Save(extagent.SessionState{BQSessionID: s.ID(), Agent: extagent.AgentCodex, Transport: extagent.TransportAppServer, ExternalSessionID: "thread"}); err != nil {
		t.Fatal(err)
	}
	initial, err := svc.conversationHistory(s.ID(), 0, true)
	if err != nil || initial.Messages[0].Rewind == nil {
		t.Fatal(err, initial.Messages)
	}
	epoch := 0
	req := rewindRequest{Session: s.ID(), Epoch: &epoch, Operation: "op", Target: "codex"}
	native := func(context.Context, *session.Session, session.PendingRewind, []string, string, func(*session.Session) error) error {
		t.Fatal("unexpected native mutation")
		return nil
	}
	gate, err := s.OpenContextLock(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, status, err := ch.rewind(t.Context(), svc, req, native); status != 409 || !errors.Is(err, session.ErrContextBusy) {
		t.Fatal(status, err)
	}
	gate.Close()
	q := commandQueue{Entries: []queuedCommand{{ID: "waiting", State: "waiting"}}}
	if err := q.writeJournal(s); err != nil {
		t.Fatal(err)
	}
	if _, status, err := ch.rewind(t.Context(), svc, req, native); status != 409 || err == nil {
		t.Fatal("queued work", status, err)
	}
	q.Entries = nil
	if err := q.writeJournal(s); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordMessage(map[string]any{"role": "user", "content": "same", transcript.SubmissionKey: "pi"}); err != nil {
		t.Fatal(err)
	}
	if _, status, err := ch.rewind(t.Context(), svc, req, native); status != 409 || err == nil {
		t.Fatal(status, err)
	}
	view, err := svc.conversationHistory(s.ID(), 0, true)
	if err != nil || view.Messages[0].Rewind != nil || view.Messages[1].Rewind != nil {
		t.Fatal(view.Messages, err)
	}
}

func TestRewindRejectsLegacyClaudePoint(t *testing.T) {
	root := t.TempDir()
	store := extagent.NewStateStore(root)
	broker := extagent.NewBroker(store, nil, nil)
	defer broker.Close()
	svc := NewService(ServiceOptions{WorkspaceRoot: root, ExternalBroker: broker})
	chat, err := svc.store.Create(session.CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	state := extagent.SessionState{Agent: extagent.AgentClaude, Transport: extagent.TransportCLI, ExternalSessionID: "native"}
	point, err := beginRewindPoint(chat, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if err := point.save(chat, "target", state, "uuid", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := chat.RecordMessage(map[string]any{"role": "user", transcript.SubmissionKey: "target"}); err != nil {
		t.Fatal(err)
	}
	ch := NewWebUIChannel(svc, true)
	epoch := chat.ContextNumber()
	req := rewindRequest{Session: chat.ID(), Target: "target", Operation: "op", Epoch: &epoch}
	_, code, err := ch.rewind(t.Context(), svc, req, nil)
	if code != 409 || err == nil || err.Error() != "仅 Codex 支持历史回退" {
		t.Fatal(code, err)
	}
	if err := chat.RewindFork("old", "claude", "target", "native", "old-child", func(*session.Session) error { return nil }); err != nil {
		t.Fatal(err)
	}
	req.Replay, req.Operation = true, "old"
	draft, code, err := ch.rewind(t.Context(), svc, req, nil)
	if code != 200 || err != nil || draft.Text != "hello" || draft.Agent != extagent.AgentClaude {
		t.Fatal(code, err, draft)
	}
}
