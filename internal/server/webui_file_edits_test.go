package server

import (
	"testing"

	"bqagent/internal/agent"
	"bqagent/internal/extagent"
	appruntime "bqagent/internal/runtime"
	"bqagent/internal/session"
	"bqagent/internal/transcript"
	"bqagent/internal/turn"
)

type editEventSink []agent.ToolEvent

func TestBoundedHistoryDropsEditPreviewBeforeToolIdentity(t *testing.T) {
	message := ConversationHistoryMessage{Role: "assistant", Tools: []agent.HistoryTool{{ID: "tool", Name: "Edit", Status: "succeeded", Edits: &transcript.FileEdits{Files: []transcript.EditFile{{Path: "file", Lines: []transcript.EditLine{{Kind: "add", Text: string(make([]byte, 1000))}}}}}}}}
	trimmed := truncateHistoryMessage(message, 128)
	if historyMessageBytes(trimmed) > 128 || len(trimmed.Tools) != 1 || trimmed.Tools[0].ID != "tool" || trimmed.Tools[0].Edits != nil || !trimmed.Tools[0].Truncated || message.Tools[0].Edits == nil {
		t.Fatal(trimmed)
	}
}

func (events *editEventSink) EmitToolEvent(e agent.ToolEvent) { *events = append(*events, e) }

func TestWebUIFileEditsPublishOnlySuccessfulChangesAndRestoreOriginalTool(t *testing.T) {
	root := t.TempDir()
	svc := NewService(ServiceOptions{WorkspaceRoot: root})
	saved, err := svc.store.Create(session.CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	conv := &appruntime.Conversation{Session: saved}
	if err := conv.AddUserMessage("edit files"); err != nil {
		t.Fatal(err)
	}
	var events editEventSink
	timeline := &externalTimeline{conversation: conv, name: extagent.AgentCodex, sink: &events}
	edit := func(text string) *transcript.FileEdits {
		return &transcript.FileEdits{Files: []transcript.EditFile{{Path: "main.go", Kind: "added", Added: 1, Lines: []transcript.EditLine{{Kind: "add", Text: text, New: 1}}}}}
	}
	timeline.observe(extagent.Activity{CallID: "call", Name: "fileChange", Status: "running", Edits: edit("draft")})
	if events[0].Edits != nil {
		t.Fatal("draft published")
	}
	timeline.recordInput(turn.Input{ID: "correction", Text: "then continue"})
	timeline.observe(extagent.Activity{CallID: "call", Status: "running", Edits: edit("revised")})
	timeline.observe(extagent.Activity{CallID: "call", Status: "succeeded"})
	timeline.observe(extagent.Activity{CallID: "failed", Name: "Edit", Status: "running", Edits: edit("never")})
	timeline.observe(extagent.Activity{CallID: "failed", Status: "failed"})
	timeline.recordInput(turn.Input{ID: "next", Text: "continue"})
	timeline.observe(extagent.Activity{CallID: "call", Status: "succeeded", Edits: edit("final")})
	last := events[len(events)-1]
	if last.Kind != "tool_result" || last.TurnID == "" || last.Edits.Files[0].Lines[0].Text != "final" {
		t.Fatal(last)
	}
	tools, _ := timeline.finish()
	if err := timeline.recordReply("done", tools, ""); err != nil {
		t.Fatal(err)
	}
	fresh := NewService(ServiceOptions{WorkspaceRoot: root})
	history, err := fresh.ConversationHistory(saved.ID(), 0)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for i, msg := range history.Messages {
		for _, tool := range msg.Tools {
			if tool.ID == "failed" && tool.Edits != nil {
				t.Fatal(tool)
			}
			if tool.ID != "call" {
				continue
			}
			found++
			if i != 1 || tool.Edits == nil || tool.Edits.Files[0].Lines[0].Text != "final" || tool.TurnID != last.TurnID {
				t.Fatal(i, tool)
			}
		}
	}
	if found != 1 || history.Messages[len(history.Messages)-1].ExternalTurn != last.TurnID {
		t.Fatal(found, history.Messages)
	}
}
