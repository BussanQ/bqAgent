package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"bqagent/internal/extagent"
	appruntime "bqagent/internal/runtime"
	"bqagent/internal/session"
	"bqagent/internal/turn"
)

func TestNativeSteeringUsesRegisteredTurnAndPersistsAcknowledgedUserBoundary(t *testing.T) {
	s := NewService(ServiceOptions{WorkspaceRoot: t.TempDir()})
	saved, err := s.store.Create(session.CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	conv := &appruntime.Conversation{Session: saved}
	if err := conv.AddUserMessage("initial"); err != nil {
		t.Fatal(err)
	}
	queue := &turn.InputQueue{}
	queue.Close()
	unregister, ok := s.activeTurns.RegisterWithInputs("web-turn", func() {}, queue)
	if !ok {
		t.Fatal("registration failed")
	}
	defer unregister()
	timeline := &externalTimeline{}
	var events []turn.Event
	hooks := timeline.steering(s.activeTurns, "web-turn", queue, func(e turn.Event) { events = append(events, e) }, conv, extagent.AgentCodex)
	if _, err := s.AddTurnInput("web-turn", turn.Steering, "too early"); !errors.Is(err, turn.ErrInputClosed) {
		t.Fatal(err)
	}
	timeline.observe(extagent.Activity{Text: "first part"})
	timeline.observe(extagent.Activity{CallID: "finished", Status: "succeeded", Output: "ok"})
	timeline.observe(extagent.Activity{CallID: "open", Status: "running"})
	hooks.Ready(func(input turn.Input) error {
		if input.Text != "use Go" {
			return errors.New("provider refused")
		}
		hooks.Applied(input)
		return nil
	}, turn.Steering)
	if _, err := s.AddTurnInput("other", turn.Steering, "wrong"); !errors.Is(err, ErrTurnNotFound) {
		t.Fatal(err)
	}
	if _, err := s.AddTurnInput("web-turn", turn.Steering, "refused"); err == nil {
		t.Fatal("refusal was lost")
	}
	input, err := s.AddTurnInput("web-turn", turn.Steering, "use Go")
	if err != nil || len(events) != 2 || events[0].Kind != "input.available" || events[0].Text != "codex" || events[1].Kind != "input.applied" || events[1].ItemID != input.ID || events[1].TurnID != "web-turn" || timeline.historyErr != nil {
		t.Fatalf("events=%+v err=%v historyErr=%v", events, err, timeline.historyErr)
	}
	records, err := saved.LoadMessages()
	if err != nil || len(records) != 3 || records[1]["content"] != "first part" || records[2]["content"] != "use Go" {
		t.Fatalf("live transcript=%+v err=%v", records, err)
	}
	timeline.observe(extagent.Activity{CallID: "finished", Status: "succeeded", Output: "replay"})
	timeline.observe(extagent.Activity{CallID: "open", Status: "succeeded", Output: "late"})
	timeline.observe(extagent.Activity{Text: "answer"})
	tools, partial := timeline.finish()
	if partial != "answer" || len(tools) != 1 || tools[0].ID != "open" || tools[0].Result != "late" {
		t.Fatalf("%v %q", tools, partial)
	}
	if err := timeline.recordReply(partial, tools, ""); err != nil {
		t.Fatal(err)
	}
	history, err := s.ConversationHistory(saved.ID(), 0)
	if err != nil || len(history.Messages) != 4 || history.Messages[1].Sender != "codex" || len(history.Messages[1].Tools) != 2 || history.Messages[1].Tools[0].ID != "finished" || history.Messages[1].Tools[1].ID != "open" || history.Messages[1].Tools[1].Result != "late" || history.Messages[2].Role != "user" || len(history.Messages[3].Tools) != 0 {
		t.Fatalf("restored=%+v err=%v", history.Messages, err)
	}
}

type toolActivityClient struct{ groupFakeACP }

func (c *toolActivityClient) Prompt(ctx context.Context, _, prompt string) (string, error) {
	emit := extagent.ActivitySinkFromContext(ctx)
	emit(extagent.Activity{CallID: "c", Name: "read", Status: "running", Input: map[string]any{"api_key": "secret", "path": "f"}})
	emit(extagent.Activity{Text: "public text"})
	if prompt == "stop" {
		return "", context.Canceled
	}
	emit(extagent.Activity{CallID: "c", Status: "succeeded", Output: "one "})
	emit(extagent.Activity{CallID: "c", Status: "succeeded", Output: "duplicate"})
	return "final answer", nil
}

func TestWebUIExternalTimelineStreamsAndRestoresTerminalTools(t *testing.T) {
	root := t.TempDir()
	b := extagent.NewBroker(extagent.NewStateStore(root), map[extagent.AgentName]extagent.DetectionResult{extagent.AgentPi: {Preferred: &extagent.AgentTransport{Agent: extagent.AgentPi, Kind: extagent.TransportACP}}}, func(context.Context, extagent.CommandSpec, string) (extagent.ACPClient, error) {
		return &toolActivityClient{groupFakeACP{name: "pi"}}, nil
	})
	defer b.Close()
	s := NewService(ServiceOptions{WorkspaceRoot: root, ExternalBroker: b})
	ch := NewWebUIChannel(s, true)
	for _, tc := range []struct{ prompt, terminal, status string }{{"run", "done", "succeeded"}, {"stop", "stopped", "unknown"}} {
		t.Run(tc.prompt, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/webui/chat", strings.NewReader(`{"agent":"pi","message":"`+tc.prompt+`"}`))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			ch.handleStreamChat(w, r)
			body := w.Body.String()
			start := strings.Index(body, "event: tool_start")
			delta := strings.Index(body, `"delta":"public text"`)
			result := strings.Index(body, "event: tool_result")
			_, last, ok := strings.Cut(body, "event: "+tc.terminal+"\ndata: ")
			if !ok || start < 0 || delta <= start || result <= delta || strings.Count(body, "event: tool_result") != 1 || strings.Contains(body, "secret") {
				t.Fatal(body)
			}
			var end struct {
				SessionID string `json:"session_id"`
			}
			if json.Unmarshal([]byte(last), &end) != nil || end.SessionID == "" {
				t.Fatal(body)
			}
			h, err := s.ConversationHistory(end.SessionID, 0)
			if err != nil {
				t.Fatal(err)
			}
			msg := h.Messages[len(h.Messages)-1]
			if msg.Sender != "pi" || len(msg.Tools) != 1 || msg.Tools[0].Status != tc.status || msg.Tools[0].Source != "external" || msg.Tools[0].Arguments["api_key"] != "[REDACTED]" {
				t.Fatalf("history: %+v", msg)
			}
			if tc.prompt == "run" && (msg.Content != "final answer" || msg.Tools[0].Result != "one ") {
				t.Fatalf("history: %+v", msg)
			}
			if tc.prompt == "stop" && !strings.Contains(msg.Content, "public text") {
				t.Fatal(msg.Content)
			}
		})
	}
}

func TestExternalTimelinePersistsOrderedItemsAcrossReloadAndSteering(t *testing.T) {
	for _, mode := range []session.TranscriptMode{session.TranscriptModeCompact, session.TranscriptModeFull} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			options := session.Options{TranscriptMode: mode}
			service := NewService(ServiceOptions{WorkspaceRoot: root, SessionOptions: &options})
			saved, err := service.store.Create(session.CreateOptions{Chat: true})
			if err != nil {
				t.Fatal(err)
			}
			conv := &appruntime.Conversation{Session: saved}
			if err := conv.AddUserMessage("review"); err != nil {
				t.Fatal(err)
			}
			timeline := &externalTimeline{conversation: conv, name: extagent.AgentCodex}
			timeline.observe(extagent.Activity{TextID: "intro", Text: "inspect "})
			timeline.observe(extagent.Activity{CallID: "r", Name: "read", Input: map[string]any{"api_key": "sensitive", "path": "a"}, Status: "running"})
			timeline.observe(extagent.Activity{TextID: "note", Text: "check B"})
			timeline.observe(extagent.Activity{TextID: "intro", Text: "inspect A", TextSnapshot: true})
			reload := func() []ConversationHistoryMessage {
				t.Helper()
				fresh := NewService(ServiceOptions{WorkspaceRoot: root, SessionOptions: &options})
				h, err := fresh.ConversationHistory(saved.ID(), 0)
				if err != nil {
					t.Fatal(err)
				}
				return h.Messages
			}
			// Nothing has called finish yet. A fresh reader still sees the
			// order and the last confirmed public state of every item.
			rows := reload()
			if len(rows) != 2 || len(rows[1].Parts) != 3 || rows[1].Parts[0].Text != "inspect A" || rows[1].Parts[1].ID != "r" || rows[1].Parts[2].Text != "check B" || rows[1].Tools[0].Status != "unknown" || rows[1].Tools[0].Arguments["api_key"] == "sensitive" {
				t.Fatalf("partial history: %+v", rows)
			}
			timeline.recordInput(turn.Input{ID: "one", Kind: turn.Steering, Text: "use C"})
			timeline.observe(extagent.Activity{TextID: "intro", Text: "inspect A", TextSnapshot: true}) // native replay
			timeline.observe(extagent.Activity{TextID: "intro", Text: "inspect A first", TextSnapshot: true})
			timeline.observe(extagent.Activity{TextID: "final", Text: "answer"})
			timeline.observe(extagent.Activity{CallID: "r", Output: "done", Status: "succeeded"})
			tools, _ := timeline.finish()
			if err = timeline.recordReply("answer in C", tools, ""); err != nil {
				t.Fatal(err)
			}
			if err = conv.SaveWorkingContext(); err != nil {
				t.Fatal(err)
			}
			rows = reload()
			if len(rows) != 4 || len(rows[1].Parts) != 3 || rows[1].Parts[0].Text != "inspect A first" || rows[1].Tools[0].Result != "done" || rows[2].Content != "use C" || len(rows[3].Parts) != 1 || rows[3].Parts[0].Text != "answer in C" || len(rows[3].Tools) != 0 {
				t.Fatalf("restored history: %+v", rows)
			}
			messages, _, err := saved.LoadResumableMessages()
			if err != nil || len(messages) != 4 || messages[3]["content"] != "answer in C" {
				t.Fatal(messages, err)
			}
		})
	}
}

func TestExternalFinalAggregateDoesNotRepeatEarlierCommentary(t *testing.T) {
	s := NewService(ServiceOptions{WorkspaceRoot: t.TempDir()})
	saved, err := s.store.Create(session.CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	timeline := &externalTimeline{conversation: &appruntime.Conversation{Session: saved}, name: extagent.AgentPi}
	timeline.observe(extagent.Activity{Text: "lead"})
	timeline.observe(extagent.Activity{CallID: "t", Status: "succeeded"})
	timeline.observe(extagent.Activity{Text: "ans"})
	tools, _ := timeline.finish()
	if err := timeline.recordReply("leadanswer", tools, ""); err != nil {
		t.Fatal(err)
	}
	h, err := s.ConversationHistory(saved.ID(), 0)
	if err != nil || len(h.Messages) != 1 || len(h.Messages[0].Parts) != 3 || h.Messages[0].Parts[2].Text != "answer" || h.Messages[0].Content != "leadanswer" {
		t.Fatal(h.Messages, err)
	}
}

type historicalPromptObserver struct {
	events []extagent.ACPPermissionRequest
}

func (o *historicalPromptObserver) EmitACPPermissionRequest(req extagent.ACPPermissionRequest) {
	o.events = append(o.events, req)
}
func (o *historicalPromptObserver) EmitACPPermissionResolution(req extagent.ACPPermissionRequest) {
	o.events = append(o.events, req)
}

func TestExternalHistoryRestoresPromptResolutionAtItsOriginalPosition(t *testing.T) {
	s := NewService(ServiceOptions{WorkspaceRoot: t.TempDir()})
	saved, err := s.store.Create(session.CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	timeline := &externalTimeline{conversation: &appruntime.Conversation{Session: saved}, name: extagent.AgentPi}
	observer := &historicalPromptObserver{}
	sink := timeline.permissions(observer)
	timeline.observe(extagent.Activity{Text: "comment"})
	req := extagent.ACPPermissionRequest{Agent: extagent.AgentPi, RequestID: "decision", Dialog: &extagent.AgentDialog{Method: "editor", Title: "Update plan", Prefill: "private editor body"}, Options: []extagent.ACPPermissionOption{{OptionID: "submit", Name: "提交"}}}
	sink.EmitACPPermissionRequest(req)
	timeline.recordInput(turn.Input{Kind: turn.Steering, Text: "change scope"})
	timeline.observe(extagent.Activity{Text: "next"})
	req.Selected = "submit"
	sink.(externalPromptSink).EmitACPPermissionResolution(req)
	tools, _ := timeline.finish()
	if err := timeline.recordReply("next", tools, ""); err != nil {
		t.Fatal(err)
	}
	h, err := s.ConversationHistory(saved.ID(), 0)
	if err != nil || len(h.Messages) != 3 || len(h.Messages[0].Parts) != 2 {
		t.Fatalf("history:%+v %v", h.Messages, err)
	}
	p := h.Messages[0].Parts[1].Prompt
	if p == nil || p.Title != "Update plan" || p.Choice != "提交" || p.State != "selected" || h.Messages[1].Content != "change scope" || len(h.Messages[2].Parts) != 1 || len(observer.events) != 2 {
		t.Fatal(h.Messages)
	}
	raw, err := os.ReadFile(saved.MessagesPath())
	if err != nil || strings.Contains(string(raw), "private editor body") {
		t.Fatal("editor contents were journaled", err)
	}
}

func TestExternalHistoryRetriesAcknowledgedBoundaryWithoutDuplicatingInput(t *testing.T) {
	s := NewService(ServiceOptions{WorkspaceRoot: t.TempDir()})
	saved, err := s.store.Create(session.CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	timeline := &externalTimeline{conversation: &appruntime.Conversation{Session: saved}, name: extagent.AgentPi}
	timeline.observe(extagent.Activity{Text: "before"})
	path := saved.MessagesPath()
	if err := os.Rename(path, path+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	timeline.recordInput(turn.Input{ID: "once", Text: "correction", Kind: turn.Steering})
	if timeline.historyErr == nil || len(timeline.waiting) != 2 {
		t.Fatal("failed checkpoint was discarded")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".original", path); err != nil {
		t.Fatal(err)
	}
	// A partially successful write may already have committed the batch.
	if err := saved.RecordMessages(timeline.waiting...); err != nil {
		t.Fatal(err)
	}
	timeline.observe(extagent.Activity{Text: "after"})
	tools, _ := timeline.finish()
	if err := timeline.recordReply("after", tools, ""); err != nil {
		t.Fatal(err)
	}
	h, err := s.ConversationHistory(saved.ID(), 0)
	if err != nil || len(h.Messages) != 3 || h.Messages[0].Content != "before" || h.Messages[1].Content != "correction" || h.Messages[2].Content != "after" {
		t.Fatal(h.Messages, err)
	}
}

func TestReplayedFinalItemAfterSteeringKeepsOnlyItsOriginalBubble(t *testing.T) {
	s := NewService(ServiceOptions{WorkspaceRoot: t.TempDir()})
	saved, err := s.store.Create(session.CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	timeline := &externalTimeline{conversation: &appruntime.Conversation{Session: saved}, name: extagent.AgentCodex}
	timeline.observe(extagent.Activity{TextID: "a", Text: "old answer"})
	timeline.recordInput(turn.Input{ID: "u", Text: "wait", Kind: turn.Steering})
	timeline.observe(extagent.Activity{TextID: "a", Text: "old answer", TextSnapshot: true})
	tools, _ := timeline.finish()
	if err := timeline.recordReply("old answer", tools, ""); err != nil {
		t.Fatal(err)
	}
	h, err := s.ConversationHistory(saved.ID(), 0)
	if err != nil || len(h.Messages) != 2 || h.Messages[0].Parts[0].Text != "old answer" || h.Messages[1].Content != "wait" {
		t.Fatal(h.Messages, err)
	}
}

func TestExternalWebUIStreamAndOrdinaryHistoryAgreeAfterReplayExpires(t *testing.T) {
	root := t.TempDir()
	client := &toolActivityClient{groupFakeACP{name: "pi"}}
	b := extagent.NewBroker(extagent.NewStateStore(root), map[extagent.AgentName]extagent.DetectionResult{extagent.AgentPi: {Preferred: &extagent.AgentTransport{Agent: extagent.AgentPi, Kind: extagent.TransportACP}}}, func(context.Context, extagent.CommandSpec, string) (extagent.ACPClient, error) { return client, nil })
	defer b.Close()
	s := NewService(ServiceOptions{WorkspaceRoot: root, ExternalBroker: b})
	ch := NewWebUIChannel(s, true)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webui/chat?compact=1", strings.NewReader(`{"agent":"pi","message":"run","turn_id":"chronology"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	ch.handleStreamChat(w, req)
	stream := w.Body.String()
	tool := strings.Index(stream, "event: tool_start")
	middle := strings.Index(stream, `"text":"public text"`)
	result := strings.Index(stream, "event: tool_result")
	final := strings.Index(stream, `"text":"final answer"`)
	if w.Code != 200 || tool < 0 || middle <= tool || result <= middle || final <= result || strings.Contains(stream, `"delta":"public text"`) {
		t.Fatal(stream)
	}
	_, tail, _ := strings.Cut(stream, "event: done\ndata: ")
	var done webUIDoneEvent
	if json.Unmarshal([]byte(tail), &done) != nil || done.SessionID == "" {
		t.Fatal(tail)
	}
	s.webTurns.expire(s.webTurns.get("chronology"))
	fresh := NewService(ServiceOptions{WorkspaceRoot: root})
	history := httptest.NewRecorder()
	(&handler{service: fresh}).handleConversationHistory(history, httptest.NewRequest(http.MethodGet, "/api/v1/webui/conversations/"+done.SessionID, nil))
	var reply struct{ Messages []conversationMessage }
	if history.Code != 200 || json.Unmarshal(history.Body.Bytes(), &reply) != nil || len(reply.Messages) != 2 {
		t.Fatal(history.Body)
	}
	m := reply.Messages[1]
	if m.Content != "final answer" || len(m.Parts) != 3 || m.Parts[0].Kind != "tool" || m.Parts[1].Text != "public text" || m.Parts[2].Text != "final answer" || m.Tools[0].Result != "one " {
		t.Fatal(m)
	}
}
