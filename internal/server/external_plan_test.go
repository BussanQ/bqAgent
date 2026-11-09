package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bqagent/internal/extagent"
	appruntime "bqagent/internal/runtime"
	"bqagent/internal/session"
	"bqagent/internal/transcript"
	"bqagent/internal/turn"
)

func TestExternalPlanKeepsPositionAcrossSteeringAndReload(t *testing.T) {
	for _, mode := range []session.TranscriptMode{session.TranscriptModeCompact, session.TranscriptModeFull} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			options := session.Options{TranscriptMode: mode}
			s := NewService(ServiceOptions{WorkspaceRoot: root, SessionOptions: &options})
			stored, err := s.store.Create(session.CreateOptions{Chat: true})
			if err != nil {
				t.Fatal(err)
			}
			conv := &appruntime.Conversation{Session: stored}
			if err := conv.AddUserMessage("plan"); err != nil {
				t.Fatal(err)
			}
			var events []ExternalPlanEvent
			timeline := externalTimeline{conversation: conv, name: extagent.AgentCodex, planSink: func(e ExternalPlanEvent) { events = append(events, e) }}
			update := func(id, body string, appendText bool, plan transcript.Plan) {
				timeline.observe(extagent.Activity{Plan: &extagent.PlanUpdate{ID: id, Text: body, Append: appendText, Plan: plan}})
			}
			pending := transcript.Plan{Mode: "steps", Steps: []transcript.PlanStep{{Text: "one", Status: "pending", Priority: "high"}}}
			complete := transcript.Plan{Mode: "steps", Steps: []transcript.PlanStep{{Text: "two", Status: "completed", Priority: "low"}}}
			proposal := transcript.Plan{Mode: "proposal"}
			update("steps", "", false, pending)
			timeline.observe(extagent.Activity{CallID: "read", Name: "read", Status: "succeeded"})
			update("p", "first", true, proposal)
			timeline.recordInput(turn.Input{ID: "correction", Kind: turn.Steering, Text: "revise"})
			update("steps", "", false, transcript.Plan{Mode: "steps"})
			update("steps", "", false, complete)
			update("p", " draft", true, proposal)
			update("p", "new **plan**", false, proposal)
			update("p", "new **plan**", false, proposal)
			verify := func() {
				t.Helper()
				reader := NewService(ServiceOptions{WorkspaceRoot: root, SessionOptions: &options})
				h, err := reader.ConversationHistory(stored.ID(), 0)
				if err != nil || len(h.Messages) != 3 {
					t.Fatalf("rows=%+v err=%v", h.Messages, err)
				}
				parts := h.Messages[1].Parts
				if len(parts) != 3 || parts[0].Plan == nil || len(parts[0].Plan.Steps) != 1 || parts[0].Plan.Steps[0] != complete.Steps[0] ||
					parts[1].Kind != "tool" || parts[2].Plan == nil || parts[2].Text != "new **plan**" || h.Messages[2].Content != "revise" {
					t.Fatalf("history=%+v", h.Messages)
				}
			}
			verify()
			tools, _ := timeline.finish()
			if err := timeline.recordReply("new **plan**", tools, ""); err != nil {
				t.Fatal(err)
			}
			if err := conv.SaveWorkingContext(); err != nil {
				t.Fatal(err)
			}
			verify()
			if len(events) != 6 || events[0].ID != events[2].ID || events[1].ID != events[5].ID || !events[4].Append || events[5].Append || events[5].ID != timeline.replyItemID {
				t.Fatalf("events=%+v replyID=%q", events, timeline.replyItemID)
			}
		})
	}
}

type planOnlyClient struct{ groupFakeACP }

func (c *planOnlyClient) Prompt(ctx context.Context, _, _ string) (string, error) {
	sink := extagent.ActivitySinkFromContext(ctx)
	for _, update := range []extagent.PlanUpdate{
		{ID: "steps", Plan: transcript.Plan{Mode: "steps", Steps: []transcript.PlanStep{{Text: "review", Status: "in_progress"}}}},
		{ID: "proposal", Text: "Plan", Append: true, Plan: transcript.Plan{Mode: "proposal"}},
		{ID: "proposal", Text: " ready", Append: true, Plan: transcript.Plan{Mode: "proposal"}},
		{ID: "proposal", Text: "Plan ready", Plan: transcript.Plan{Mode: "proposal"}},
	} {
		sink(extagent.Activity{Plan: &update})
	}
	return "Plan ready", nil
}

func TestWebUIPlanOnlyTurnReplaysWithoutDuplicateAnswer(t *testing.T) {
	root := t.TempDir()
	client := &planOnlyClient{groupFakeACP{name: "codex"}}
	b := extagent.NewBroker(extagent.NewStateStore(root), map[extagent.AgentName]extagent.DetectionResult{extagent.AgentCodex: {Preferred: &extagent.AgentTransport{Agent: extagent.AgentCodex, Kind: extagent.TransportACP}}}, func(context.Context, extagent.CommandSpec, string) (extagent.ACPClient, error) { return client, nil })
	defer b.Close()
	s := NewService(ServiceOptions{WorkspaceRoot: root, ExternalBroker: b})
	ch := NewWebUIChannel(s, true)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webui/chat?compact=1", strings.NewReader(`{"agent":"codex","message":"plan","turn_id":"native-plan"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	ch.handleStreamChat(w, req)
	stream := w.Body.String()
	_, tail, _ := strings.Cut(stream, "event: done\ndata: ")
	var done webUIDoneEvent
	if json.Unmarshal([]byte(tail), &done) != nil || done.Reply != "Plan ready" || !strings.HasSuffix(done.ReplyItemID, "/proposal") || strings.Count(stream, "event: external_plan") != 3 || strings.Contains(stream, "event: external_text") {
		t.Fatal(stream)
	}
	replay := httptest.NewRecorder()
	ch.handleWebTurn(replay, httptest.NewRequest(http.MethodGet, "/api/v1/webui/turns?compact=1&turn_id=native-plan&after=0", nil))
	if replay.Code != http.StatusOK || strings.Count(replay.Body.String(), "event: external_plan") != 3 || !strings.Contains(replay.Body.String(), done.ReplyItemID) {
		t.Fatal(replay.Body)
	}
	s.webTurns.expire(s.webTurns.get("native-plan"))
	fresh := NewService(ServiceOptions{WorkspaceRoot: root})
	h, err := fresh.ConversationHistory(done.SessionID, 0)
	if err != nil || len(h.Messages) != 2 || len(h.Messages[1].Parts) != 2 || h.Messages[1].Parts[0].Plan.Mode != "steps" || h.Messages[1].Parts[1].Text != "Plan ready" {
		t.Fatal(h.Messages, err)
	}
}
