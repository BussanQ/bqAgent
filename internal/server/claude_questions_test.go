package server

import (
	"encoding/json"
	"strings"
	"testing"

	"bqagent/internal/extagent"
	appruntime "bqagent/internal/runtime"
	"bqagent/internal/session"
	"bqagent/internal/transcript"
)

func TestClaudeGroupedQuestionHistoryRetainsChoicesAndTrimsDescriptionsBeforeAnswers(t *testing.T) {
	root := t.TempDir()
	s := NewService(ServiceOptions{WorkspaceRoot: root})
	saved, err := s.store.Create(session.CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	timeline := externalTimeline{conversation: &appruntime.Conversation{Session: saved}, name: extagent.AgentClaude}
	req := extagent.ACPPermissionRequest{RequestID: "group", Agent: extagent.AgentClaude, Dialog: &extagent.AgentDialog{
		Method: "question", Title: "Claude 提问", Header: "子 Agent：worker", Questions: []transcript.PromptQuestion{
			{ID: "q1", Title: "What changes?", MultiSelect: true, Options: []transcript.PromptOption{
				{ID: "a", Label: "Tests", Description: strings.Repeat("细节", 512)}, {ID: "b", Label: "Docs"}}}, {ID: "q2", Title: "Notes?"},
		}}}
	timeline.recordPrompt(req)
	req.Selected = "submit"
	req.Dialog.Questions[0].Options[0].Selected = true
	timeline.recordPrompt(req)
	tools, _ := timeline.finish()
	if err := timeline.recordReply("done", tools, ""); err != nil {
		t.Fatal(err)
	}
	h, err := NewService(ServiceOptions{WorkspaceRoot: root}).ConversationHistory(saved.ID(), 0)
	if err != nil || len(h.Messages) != 1 || len(h.Messages[0].Parts) == 0 {
		t.Fatal(h, err)
	}
	msg := h.Messages[0]
	p := msg.Parts[0].Prompt
	if p == nil || p.State != "selected" || p.Choice != "" || len(p.Questions) != 2 || p.Questions[0].Options[0].ID != "a" || !p.Questions[0].Options[0].Selected || p.Questions[0].Options[1].Selected || p.Questions[1].ID != "q2" {
		t.Fatal(p)
	}
	body, _ := json.Marshal(webHistoryPayload(h))
	if !strings.Contains(string(body), `"selected":true`) || !strings.Contains(string(body), `"multi_select":true`) {
		t.Fatal(string(body))
	}
	budget := historyMessageBytes(msg) - len(p.Questions[0].Options[0].Description)/2
	preview := truncateHistoryMessage(msg, budget)
	if len(preview.Parts) == 0 || preview.Parts[0].Prompt == nil || historyMessageBytes(preview) > budget {
		t.Fatal(preview)
	}
	trimmed := preview.Parts[0].Prompt.Questions
	if len(trimmed) != 2 || len(trimmed[0].Options) != 1 || trimmed[0].Options[0].Label != "Tests" || !trimmed[0].Options[0].Selected ||
		len(p.Questions[0].Options) != 2 || p.Questions[0].Options[0].Description == "" {
		t.Fatal(trimmed, p)
	}
}
