package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"

	"strings"
	"testing"

	"bqagent/internal/extagent"
	"bqagent/internal/session"
	"bqagent/internal/turn"
)

func postContextClear(ch *WebUIChannel, id, op, text, agent string) *httptest.ResponseRecorder {
	data, _ := json.Marshal(map[string]string{"message": text, "agent": agent, "session_id": id, "turn_id": op})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/webui/chat", strings.NewReader(string(data)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	ch.handleStreamChat(w, r)
	return w
}

func TestWebUIClearKeepsHistoryAndStartsFreshModelContext(t *testing.T) {
	for _, mode := range []session.TranscriptMode{session.TranscriptModeCompact, session.TranscriptModeFull} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			opts := session.Options{TranscriptMode: mode}
			client := &chatModeRecordingClient{}
			svc := NewService(ServiceOptions{WorkspaceRoot: root, SessionOptions: &opts, SystemPrompt: "stable", Client: client})
			ch := NewWebUIChannel(svc, true)
			first, err := svc.HandleTurn(t.Context(), TurnRequest{Message: "secret-old-context"})
			if err != nil {
				t.Fatal(err)
			}
			id := first.SessionID
			old, err := svc.store.Open(id)
			if err != nil {
				t.Fatal(err)
			}
			if err = old.SaveCheckpointSummary("secret-old-checkpoint", nil, "stable"); err != nil {
				t.Fatal(err)
			}
			for _, op := range []string{"clear-one", "clear-one"} {
				w := postContextClear(ch, id, op, "/clear", "")
				if w.Code != 200 || !strings.Contains(w.Body.String(), id) {
					t.Fatal(w.Code, w.Body.String())
				}
			}
			if len(client.messages) != 1 {
				t.Fatal("clear called the model")
			}
			h, err := svc.ConversationHistory(id, 0)
			if err != nil || len(h.Messages) != 3 || h.Messages[0].Content != "secret-old-context" || h.Messages[2].Kind != "context_clear" {
				t.Fatal(err, h)
			}
			next, err := svc.HandleTurn(t.Context(), TurnRequest{SessionID: id, Message: "new-only"})
			if err != nil || next.SessionID != id {
				t.Fatal(next, err)
			}
			prompt, _ := json.Marshal(client.messages[len(client.messages)-1])
			if strings.Contains(string(prompt), "secret-old") || !strings.Contains(string(prompt), "new-only") {
				t.Fatal(string(prompt))
			}
			fresh := NewWebUIChannel(NewService(ServiceOptions{WorkspaceRoot: root, SessionOptions: &opts, Client: client}), true)
			for _, op := range []string{"clear-two", "clear-one"} {
				if w := postContextClear(fresh, id, op, "/clear", ""); w.Code != 200 {
					t.Fatal(w.Code, w.Body.String())
				}
			}
			probe := httptest.NewRecorder()
			fresh.handleWebTurn(probe, httptest.NewRequest(http.MethodGet, "/api/v1/webui/turns?view=context&session_id="+id, nil))
			if probe.Code != 200 || !strings.Contains(probe.Body.String(), `"context_number":2`) {
				t.Fatal(probe.Code, probe.Body.String())
			}
			s, err := svc.store.Open(id)
			if err != nil || s.ContextNumber() != 2 {
				t.Fatal(s, err)
			}
			oldHistory, err := svc.projectConversationHistory(old, nil, nil, 0, false, nil)
			if err != nil || len(oldHistory.Messages) != 0 {
				t.Fatal(oldHistory, err)
			}
		})
	}
}

func TestWebUIClearPreservesPausedQueueAndAttachmentReceipts(t *testing.T) {
	root := t.TempDir()
	svc := NewService(ServiceOptions{WorkspaceRoot: root})
	ch := NewWebUIChannel(svc, true)
	s, err := svc.store.Create(session.CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	data := "ZmlsZQ=="
	blob, err := writeQueueBundle(s, queueAttachmentPayload{Files: []webUIFilePayload{{Name: "file", DataBase64: &data}}})
	if err != nil {
		t.Fatal(err)
	}
	q := commandQueue{Version: queueJournalVersion, Paused: true, Entries: []queuedCommand{
		{ID: "first", State: "waiting", Kind: "message", Agent: "pi", Text: "first", Blob: blob, Picks: &queuedModelSelection{Model: "p/model"}, Attachments: []queueAttachmentInfo{{Kind: "file", Name: "file"}}},
		{ID: "compact", State: "waiting", Agent: "pi", Text: "/compact"},
		{ID: "last", State: "waiting", Kind: "message", Agent: "codex", Text: "last"},
	}, Bindings: map[string]string{"first": "old-pi", "compact": "old-pi"}}
	if err = q.save(s); err != nil {
		t.Fatal(err)
	}
	w := postContextClear(ch, s.ID(), "op", "/clear", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	reopened, err := svc.store.Open(s.ID())
	if err != nil {
		t.Fatal(err)
	}
	q2, err := readCommandQueue(reopened, "test")
	if err != nil {
		t.Fatal(err)
	}
	v := q2.view(s.ID(), "compact")
	if !v.Paused || len(v.Items) != 2 || v.Items[0].ID != "first" || v.Items[1].ID != "last" || v.Receipt.State != "withdrawn" || v.Items[0].Picks.Model != "p/model" || len(q2.Bindings) != 0 {
		t.Fatal(v)
	}
	payload, err := readQueueBundle(reopened, q2.Entries[0].Blob)
	if err != nil || *payload.Files[0].DataBase64 != data {
		t.Fatal(payload, err)
	}
}

func TestWebUIClearRefusesActiveTurnIncludingAnotherService(t *testing.T) {
	root := t.TempDir()
	svc := NewService(ServiceOptions{WorkspaceRoot: root})
	ch := NewWebUIChannel(svc, true)
	s, err := svc.store.Create(session.CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := session.NewStore(root).Open(s.ID())
	gate, err := other.OpenContextLock(false)
	if err != nil {
		t.Fatal(err)
	}
	w := postContextClear(ch, s.ID(), "op", "/clear", "")
	gate.Close()
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	job, _, _ := svc.webTurns.start(context.Background(), "working", s.ID())
	defer job.cancel()
	if _, err := svc.AddTurnInput(job.id, turn.Steering, "/pi /clear"); !errors.Is(err, session.ErrContextBusy) {
		t.Fatal(err)
	}
	if w = postContextClear(ch, s.ID(), "op", "/pi /clear", "pi"); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err = s.RefreshContexts(); err != nil || s.ContextNumber() != 0 {
		t.Fatal(s, err)
	}
	if _, err = os.Stat(s.MessagesPath()); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, msg := range []string{"/clear arg", "/codex /clear arg"} {
		if w = postContextClear(ch, s.ID(), "op", msg, ""); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestWebUIClearDropsNativeThreadsAndPreservesAgentModelChoices(t *testing.T) {
	root := t.TempDir()
	store := extagent.NewStateStore(root)
	detections := map[extagent.AgentName]extagent.DetectionResult{}
	for _, name := range []extagent.AgentName{"pi", "codex"} {
		detections[name] = extagent.DetectionResult{Agent: name, Preferred: &extagent.AgentTransport{Agent: name, Kind: extagent.TransportACP, Command: extagent.CommandSpec{Command: string(name)}}}
	}
	starts := 0
	b := extagent.NewBroker(store, detections, func(_ context.Context, spec extagent.CommandSpec, _ string) (extagent.ACPClient, error) {
		starts++
		return &singleModelClient{groupFakeACP{name: spec.Command}}, nil
	})
	defer b.Close()
	svc := NewService(ServiceOptions{WorkspaceRoot: root, ExternalBroker: b})
	ch := NewWebUIChannel(svc, true)
	s, err := svc.store.Create(session.CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []extagent.AgentName{"pi", "codex"} {
		if err = store.Save(extagent.SessionState{BQSessionID: s.ID(), Agent: name, Model: string(name) + "-model", Effort: "high", ExternalSessionID: "obsolete-" + string(name), Transport: extagent.TransportACP}); err != nil {
			t.Fatal(err)
		}
	}
	w := postContextClear(ch, s.ID(), "op", "/pi /clear", "codex")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if starts != 0 {
		t.Fatal("clear started a provider")
	}
	state, err := store.Load(s.ID())
	if err != nil || state.Agent != "pi" || state.ExternalSessionID != "" || state.Model != "pi-model" || state.Effort != "high" {
		t.Fatal(state, err)
	}
	for _, name := range []extagent.AgentName{"codex", "pi"} {
		state, err := b.QueuedMessageState(t.Context(), name, s.ID(), "", "", "", false)
		if err != nil || state.Model != string(name)+"-model" || state.ExternalSessionID != "" || state.Effort != "high" {
			t.Fatal(state, err)
		}
	}
	w = postContextClear(ch, s.ID(), "op", "/codex /clear", "codex")
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, name := range []extagent.AgentName{"pi", "codex"} {
		r, err := svc.HandleTurn(t.Context(), TurnRequest{SessionID: s.ID(), Message: "fresh", Agent: name})
		if err != nil || r.Reply != string(name)+"/"+string(name)+"-model: fresh (high)" {
			t.Fatal(r, err)
		}
		state, err = store.Load(s.ID())
		if err != nil || state.ExternalSessionID != string(name)+"-session" {
			t.Fatal(state, err)
		}
	}
	if starts != 2 {
		t.Fatal(starts)
	}
}

func TestWebUIClearKeepsImageReferencesInsideTheirOwnContext(t *testing.T) {
	svc := NewService(ServiceOptions{WorkspaceRoot: t.TempDir()})
	ch := NewWebUIChannel(svc, true)
	s, err := svc.store.Create(session.CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	appendImage := func(s *session.Session, red uint8) []byte {
		pixels := image.NewRGBA(image.Rect(0, 0, 1, 1))
		pixels.SetRGBA(0, 0, color.RGBA{R: red, A: 255})
		var pngBytes bytes.Buffer
		if err := png.Encode(&pngBytes, pixels); err != nil {
			t.Fatal(err)
		}
		encoded := base64.StdEncoding.EncodeToString(pngBytes.Bytes())
		if err := s.RecordMessage(map[string]any{"role": "user", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + encoded}}}}); err != nil {
			t.Fatal(err)
		}
		return pngBytes.Bytes()
	}
	oldBytes := appendImage(s, 0)
	if w := postContextClear(ch, s.ID(), "clear", "/clear", ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	s, err = svc.store.Open(s.ID())
	if err != nil {
		t.Fatal(err)
	}
	newBytes := appendImage(s, 255)
	h, err := svc.conversationHistory(s.ID(), 0, true)
	if err != nil || len(h.Messages) != 3 || len(h.Messages[0].Images) != 1 || len(h.Messages[2].Images) != 1 {
		t.Fatal(h, err)
	}
	first, second := h.Messages[0].Images[0], h.Messages[2].Images[0]
	if !strings.HasPrefix(second, "1.") || strings.Count(first, ".") != 2 {
		t.Fatal(first, second)
	}
	for key, want := range map[string][]byte{first: oldBytes, second: newBytes, "1." + first: nil, strings.TrimPrefix(second, "1."): nil, "2." + first: nil, "-1." + first: nil} {
		w := httptest.NewRecorder()
		serveConversationImage(w, httptest.NewRequest(http.MethodGet, "/?image="+key, nil), svc, s.ID())
		if want == nil {
			if w.Code != 404 {
				t.Fatal(key, w.Code)
			}
			continue
		}
		if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), want) {
			t.Fatal(key, w.Code)
		}
	}
}

func TestWebUIClearHoldsDraftsUntilResumeOrNewUserTurn(t *testing.T) {
	for _, action := range []string{"resume", "new-message"} {
		t.Run(action, func(t *testing.T) {
			f := newQueueFixture(t)
			peer := NewWebUIChannel(NewService(ServiceOptions{WorkspaceRoot: f.s.workspaceRoot}), true)
			id := f.saved.ID()
			// A separate service cannot clear or terminate the active provider.
			if w := postContextClear(peer, id, "op", "/clear", ""); w.Code != 409 || f.s.webTurns.active(id) != "parent" {
				t.Fatal(w.Code, w.Body.String())
			}
			message := f.submit("waiting", "opencode", "pending review")
			message.Kind = "message"
			message.Model = "selected"
			compact := f.submit("compact", "pi", "/compact")
			for _, req := range []commandQueueRequest{message, compact} {
				if _, code, body := f.req("POST", req); code != 200 {
					t.Fatal(body)
				}
			}
			if !f.s.StopTurn("parent") {
				t.Fatal("stop failed")
			}
			f.end(t, "parent", "stopped")
			if w := postContextClear(f.ch, id, "op", "/clear", "bqagent"); w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			v, code, body := f.req("GET", message)
			if code != 200 || !v.Paused || len(v.Items) != 1 || v.Items[0].ID != message.ID || v.TurnID != "" {
				t.Fatal(body)
			}
			if v, code, body = f.req("POST", compact); code != 200 || v.Receipt.State != "withdrawn" || len(v.Items) != 1 {
				t.Fatal(body)
			}
			var child string
			if action == "resume" {
				v, code, body = f.req("POST", commandQueueRequest{Action: "resume", Session: id})
				if code != 200 {
					t.Fatal(body)
				}
				child = v.TurnID
			} else {
				w := postContextClear(f.ch, id, "next", "new prompt", "bqagent")
				_, tail, _ := strings.Cut(w.Body.String(), "event: done\ndata: ")
				var done webUIDoneEvent
				if err := json.Unmarshal([]byte(tail), &done); err != nil {
					t.Fatal(w.Body.String())
				}
				child = done.NextTurnID
			}
			if child == "" {
				t.Fatal("waiting message did not dispatch")
			}
			done := f.end(t, child, "done")
			if done.ReplyAgent != "opencode" || done.ReplyAgentModel != "selected" || done.SessionID != id || done.NextTurnID != "" {
				t.Fatal(done)
			}
		})
	}
}
