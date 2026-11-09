package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"bqagent/internal/extagent"
	appruntime "bqagent/internal/runtime"
	"bqagent/internal/session"
	"bqagent/internal/turn"
)

func TestCodexMCPApprovalKeepsIdentityDetailAndDecisionAcrossReplayAndHistory(t *testing.T) {
	root := t.TempDir()
	s := NewService(ServiceOptions{WorkspaceRoot: root})
	saved, err := s.store.Create(session.CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	timeline := externalTimeline{conversation: &appruntime.Conversation{Session: saved}, name: extagent.AgentCodex}
	stream := httptest.NewRecorder()
	sink := timeline.permissions(sseACPPermissionSink{stream: &sseStream{writer: stream, flusher: stream}}).(externalPromptSink)
	tool := json.RawMessage(`{"source":"codex-app-server","kind":"mcp_tool_call","title":"Move file?","server":"drive","tool":"move_file","detail":"目标：文件"}`)
	req := extagent.ACPPermissionRequest{RequestID: "a", Agent: extagent.AgentCodex, BQSessionID: saved.ID(), ToolCall: tool, Options: []extagent.ACPPermissionOption{
		{OptionID: "accept", Name: "允许本次", Kind: "allow_once"}, {OptionID: "acceptForSession", Name: "本会话允许此工具", Kind: "allow_always"}, {OptionID: "cancel", Name: "拒绝本次调用", Kind: "reject_once"},
	}}
	sink.EmitACPPermissionRequest(req)
	timeline.recordInput(turn.Input{ID: "u", Kind: turn.FollowUp, Text: "next"})
	req.Selected = "acceptForSession"
	sink.EmitACPPermissionResolution(req)
	expired := req
	expired.RequestID, expired.Selected = "b", ""
	sink.EmitACPPermissionRequest(expired)
	expired.Expired = true
	sink.EmitACPPermissionResolution(expired)
	tools, _ := timeline.finish()
	if err := timeline.recordReply("done", tools, ""); err != nil {
		t.Fatal(err)
	}
	if body := stream.Body.String(); strings.Count(body, "event: acp_permission") != 4 || !strings.Contains(body, `"selected":"acceptForSession"`) || !strings.Contains(body, `"expired":true`) {
		t.Fatal(body)
	}
	h, err := NewService(ServiceOptions{WorkspaceRoot: root}).ConversationHistory(saved.ID(), 0)
	if err != nil || len(h.Messages) != 3 || len(h.Messages[0].Parts) != 1 || len(h.Messages[2].Parts) != 2 {
		t.Fatal(err, h.Messages)
	}
	p := h.Messages[0].Parts[0].Prompt
	if p == nil || p.Method != "mcp_approval" || p.Header != "drive / move_file" || p.Title != "Move file?" || p.Detail != "目标：文件" || len(p.Options) != 3 || p.Choice != "本会话允许此工具" || p.State != "selected" {
		t.Fatal(p)
	}
	if e := h.Messages[2].Parts[0].Prompt; e == nil || e.State != "cancelled" || e.Choice != "" {
		t.Fatal(e)
	}
	p.Detail = strings.Repeat("文字", 4096)
	budget := historyMessageBytes(h.Messages[0]) - len(p.Detail) + 120
	preview := truncateHistoryMessage(h.Messages[0], budget)
	if len(preview.Parts) != 1 || preview.Parts[0].Prompt.Choice != p.Choice || !utf8.ValidString(preview.Parts[0].Prompt.Detail) || historyMessageBytes(preview) > budget || len(p.Detail) <= 120 {
		t.Fatal("history budget lost the decision or mutated the original")
	}
}
