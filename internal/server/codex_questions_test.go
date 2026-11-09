package server

import (
	"encoding/json"
	"testing"

	"bqagent/internal/extagent"
	appruntime "bqagent/internal/runtime"
	"bqagent/internal/session"
	"bqagent/internal/turn"
)

func TestWebUIQuestionHistoryKeepsOptionsAndOnlyPublicSelectionsAcrossSteering(t *testing.T) {
	root := t.TempDir()
	s := NewService(ServiceOptions{WorkspaceRoot: root})
	saved, err := s.store.Create(session.CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	timeline := &externalTimeline{conversation: &appruntime.Conversation{Session: saved}, name: extagent.AgentCodex}
	sink := timeline.permissions(&historicalPromptObserver{}).(externalPromptSink)
	options := []extagent.ACPPermissionOption{{OptionID: "choice-0", Name: "src", Description: "Source files"}, {OptionID: "submit", Name: "填写回答"}, {OptionID: "cancel", Name: "取消本组问题"}}
	request := func(id, title string, secret bool) extagent.ACPPermissionRequest {
		return extagent.ACPPermissionRequest{RequestID: id, Agent: extagent.AgentCodex, Dialog: &extagent.AgentDialog{Method: "question", Title: title, Header: "Scope", FreeText: true, Secret: secret}, Options: options}
	}
	public, free, secret := request("one", "Pick a folder", false), request("two", "Other detail", false), request("three", "Secret value", true)
	for _, q := range []extagent.ACPPermissionRequest{public, free, secret} {
		sink.EmitACPPermissionRequest(q)
	}
	public.Selected = "choice-0"
	sink.EmitACPPermissionResolution(public)
	timeline.recordInput(turn.Input{ID: "u", Kind: turn.FollowUp, Text: "next"})
	free.Selected, secret.Selected = "submit", "answered"
	sink.EmitACPPermissionResolution(secret)
	sink.EmitACPPermissionResolution(free)
	tools, _ := timeline.finish()
	if err := timeline.recordReply("done", tools, ""); err != nil {
		t.Fatal(err)
	}
	reader := NewService(ServiceOptions{WorkspaceRoot: root})
	h, err := reader.ConversationHistory(saved.ID(), 0)
	if err != nil || len(h.Messages) != 3 || len(h.Messages[0].Parts) != 3 || h.Messages[1].Content != "next" || h.Messages[2].Content != "done" {
		t.Fatal(err, h.Messages)
	}
	for i, part := range h.Messages[0].Parts {
		p := part.Prompt
		if p == nil || p.Method != "question" || p.Header != "Scope" || p.State != "selected" || len(p.Options) != 1 || p.Options[0].Description != "Source files" || p.Choice != []string{"src", "", ""}[i] {
			t.Fatal(p)
		}
	}
	page, err := json.Marshal(webHistoryPayload(h))
	var body struct{ Messages []conversationMessage }
	if err != nil || json.Unmarshal(page, &body) != nil || len(body.Messages) != 3 || len(body.Messages[0].Parts) != 3 ||
		body.Messages[0].Parts[0].Prompt.Options[0].Description != "Source files" || body.Messages[0].Parts[2].Prompt.Choice != "" {
		t.Fatal(err, string(page))
	}
}
