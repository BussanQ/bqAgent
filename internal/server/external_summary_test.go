package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"bqagent/internal/extagent"
	appruntime "bqagent/internal/runtime"
	"bqagent/internal/session"
	"bqagent/internal/transcript"
	"bqagent/internal/turn"
)

func TestExternalSummaryKeepsOriginalPositionAcrossSteeringAndReload(t *testing.T) {
	for _, mode := range []session.TranscriptMode{session.TranscriptModeCompact, session.TranscriptModeFull} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			opts := session.Options{TranscriptMode: mode}
			s := NewService(ServiceOptions{WorkspaceRoot: root, SessionOptions: &opts})
			stored, err := s.store.Create(session.CreateOptions{Chat: true})
			if err != nil {
				t.Fatal(err)
			}
			conv := &appruntime.Conversation{Session: stored}
			if err := conv.AddUserMessage("root"); err != nil {
				t.Fatal(err)
			}
			var events []ExternalSummaryEvent
			timeline := externalTimeline{conversation: conv, name: extagent.AgentCodex, summarySink: func(e ExternalSummaryEvent) { events = append(events, e) }}
			observe := func(u extagent.SummaryUpdate) { timeline.observe(extagent.Activity{Summary: &u}) }
			observe(extagent.SummaryUpdate{ID: "r", Index: 1, Delta: "second"})
			timeline.observe(extagent.Activity{CallID: "tool", Name: "Read", Status: "succeeded"})
			timeline.recordInput(turn.Input{ID: "followup", Kind: turn.Steering, Text: "user correction"})
			observe(extagent.SummaryUpdate{ID: "r", Index: 0, Delta: "first"})
			observe(extagent.SummaryUpdate{ID: "r", Index: 2, Delta: "temporary"})
			timeline.recordInput(turn.Input{ID: "followup-2", Kind: turn.Steering, Text: "continue"})
			final := extagent.SummaryUpdate{ID: "r", Sections: []string{"first", "revised"}, Ended: true}
			observe(final)
			n := len(events)
			observe(final)
			observe(extagent.SummaryUpdate{ID: "r", Index: 1, Delta: "stale"})
			observe(extagent.SummaryUpdate{ID: "r", Sections: []string{"stale start"}})
			if len(events) != n || !events[n-1].Ended {
				t.Fatal(events)
			}
			for _, event := range events {
				if event.ID != events[0].ID {
					t.Fatal("summary split across segments", events)
				}
			}
			verify := func(want int) {
				t.Helper()
				reader := NewService(ServiceOptions{WorkspaceRoot: root, SessionOptions: &opts})
				h, err := reader.ConversationHistory(stored.ID(), 0)
				if err != nil || len(h.Messages) != want {
					t.Fatal(err, h.Messages)
				}
				row := h.Messages[1]
				if row.Content != "" || len(row.Parts) != 4 || row.Parts[0].Text != "revised" || !row.Parts[0].Summary.Ended ||
					row.Parts[1].Kind != "tool" || row.Parts[2].Text != "first" || row.Parts[3].Text != "" || h.Messages[2].Content != "user correction" || h.Messages[3].Content != "continue" {
					t.Fatal(h.Messages)
				}
				if want == 5 && h.Messages[4].Content != "answer" {
					t.Fatal(h.Messages)
				}
			}
			verify(4)
			tools, partial := timeline.finish()
			if partial != "" {
				t.Fatal("summary included in assistant content", partial)
			}
			if err := timeline.recordReply("answer", tools, ""); err != nil {
				t.Fatal(err)
			}
			verify(5)
		})
	}
}

func TestExternalSummaryLimitsBytesAndFinishesAnInterruptedStream(t *testing.T) {
	var events []ExternalSummaryEvent
	timeline := externalTimeline{name: extagent.AgentCodex, summarySink: func(e ExternalSummaryEvent) { events = append(events, e) }}
	timeline.observe(extagent.Activity{Summary: &extagent.SummaryUpdate{ID: "r", Index: 0, Delta: strings.Repeat("西", maxSummarySectionBytes)}})
	timeline.observe(extagent.Activity{Summary: &extagent.SummaryUpdate{ID: "r", Index: 0, Delta: "more"}})
	_, partial := timeline.finish()
	if partial != "" || len(events) != 2 || !events[0].Truncated || !events[1].Truncated || !events[1].Ended || !utf8.ValidString(events[0].Text) || len(events[0].Text) > maxSummarySectionBytes {
		t.Fatal(partial, len(events))
	}
	message := ConversationHistoryMessage{Content: "answer", Role: "assistant", Parts: append(timeline.parts.Snapshot(), transcript.Part{Kind: "text", ID: "final", Text: "answer"})}
	budget := historyMessageBytes(message) - len(events[0].Text) + 9
	result := truncateHistoryMessage(message, budget)
	if historyMessageBytes(result) > budget || len(result.Parts) != 2 || len(result.Parts[0].Text) > 9 || !result.Parts[0].Summary.Truncated ||
		result.Parts[1].Text != "answer" || message.Parts[0].Text != events[0].Text {
		t.Fatal("history preview damaged the original timeline")
	}
}

func TestExternalSummaryRevisionReusesReleasedBudgetInIndexOrder(t *testing.T) {
	events := 0
	timeline := externalTimeline{name: extagent.AgentCodex, summarySink: func(ExternalSummaryEvent) { events++ }}
	fill := strings.Repeat("x", maxSummarySectionBytes)
	sections := make([]string, 1+maxTurnSummaryBytes/maxSummarySectionBytes)
	for i := 1; i < len(sections); i++ {
		sections[i] = fill
	}
	observe := func(parts []string) {
		timeline.observe(extagent.Activity{Summary: &extagent.SummaryUpdate{ID: "r", Sections: parts}})
	}
	observe(sections)
	if timeline.summaryBytes != maxTurnSummaryBytes {
		t.Fatal(timeline.summaryBytes)
	}
	sections[0], sections[len(sections)-1] = "first", ""
	observe(sections)
	first := timeline.summaries["r"].sections[0]
	if first == nil || first.truncated || first.text.String() != "first" || timeline.summaryBytes != maxTurnSummaryBytes-maxSummarySectionBytes+5 {
		t.Fatal("snapshot did not reclaim space before revising the first paragraph")
	}
	n := events
	observe(sections)
	if n != events {
		t.Fatal("unchanged snapshot replayed")
	}
	// An oversized section index marks the preview without allocating a
	// sparse array. Repeated final snapshots retain that marker only once.
	sections = make([]string, transcript.MaxSummarySections+1)
	sections[0], sections[transcript.MaxSummarySections-1], sections[transcript.MaxSummarySections] = "a", "z", "beyond preview"
	observe(sections)
	n = events
	observe(sections)
	last := timeline.summaries["r"].sections[transcript.MaxSummarySections-1]
	if n != events || last.text.String() != "z" || !last.truncated {
		t.Fatal("duplicate or missing overflow marker")
	}
}

type summaryClient struct{ groupFakeACP }

func (c *summaryClient) Prompt(ctx context.Context, _, _ string) (string, error) {
	sink := extagent.ActivitySinkFromContext(ctx)
	sink(extagent.Activity{Summary: &extagent.SummaryUpdate{ID: "r", Delta: "summary only"}})
	sink(extagent.Activity{Summary: &extagent.SummaryUpdate{ID: "r", Ended: true}})
	return "public answer", nil
}

func TestWebUISummaryReplaysSeparatelyFromAnswerAndRestoresAfterExpiry(t *testing.T) {
	root := t.TempDir()
	client := &summaryClient{groupFakeACP{name: "codex"}}
	b := extagent.NewBroker(extagent.NewStateStore(root), map[extagent.AgentName]extagent.DetectionResult{extagent.AgentCodex: {Preferred: &extagent.AgentTransport{Agent: extagent.AgentCodex, Kind: extagent.TransportACP}}}, func(context.Context, extagent.CommandSpec, string) (extagent.ACPClient, error) { return client, nil })
	defer b.Close()
	s := NewService(ServiceOptions{WorkspaceRoot: root, ExternalBroker: b})
	ch := NewWebUIChannel(s, true)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webui/chat?compact=1", strings.NewReader(`{"agent":"codex","message":"read","turn_id":"summary-turn"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	ch.handleStreamChat(w, req)
	if w.Code != 200 || strings.Count(w.Body.String(), "event: external_summary") != 2 {
		t.Fatal(w.Body)
	}
	_, tail, _ := strings.Cut(w.Body.String(), "event: done\ndata: ")
	var done webUIDoneEvent
	if json.NewDecoder(strings.NewReader(tail)).Decode(&done) != nil || done.Reply != "public answer" {
		t.Fatal(w.Body)
	}
	replay := httptest.NewRecorder()
	ch.handleWebTurn(replay, httptest.NewRequest(http.MethodGet, "/api/v1/webui/turns?turn_id=summary-turn", nil))
	if strings.Count(replay.Body.String(), "event: external_summary") != 2 || strings.Contains(replay.Body.String(), `"delta":"summary only"`) || !strings.Contains(replay.Body.String(), `"delta":"public answer"`) {
		t.Fatal(replay.Body)
	}
	s.webTurns.expire(s.webTurns.get("summary-turn"))
	h, err := NewService(ServiceOptions{WorkspaceRoot: root}).ConversationHistory(done.SessionID, 0)
	if err != nil || len(h.Messages) != 2 || h.Messages[1].Content != done.Reply || len(h.Messages[1].Parts) != 2 || h.Messages[1].Parts[0].Text != "summary only" || !h.Messages[1].Parts[0].Summary.Ended {
		t.Fatal(err, h.Messages)
	}
}
