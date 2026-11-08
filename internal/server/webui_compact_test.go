package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bqagent/internal/extagent"
)

func TestWebUICompactRunsAsSessionControlAndRestoresHistory(t *testing.T) {
	root := t.TempDir()
	store := extagent.NewStateStore(root)
	b := extagent.NewBroker(store, map[extagent.AgentName]extagent.DetectionResult{
		extagent.AgentCodex:  {Preferred: &extagent.AgentTransport{Agent: extagent.AgentCodex, Kind: extagent.TransportAppServer, Command: extagent.CommandSpec{Command: "missing-test-codex"}}},
		extagent.AgentClaude: {Preferred: &extagent.AgentTransport{Agent: extagent.AgentClaude, Kind: extagent.TransportCLI, Command: extagent.CommandSpec{Command: "missing-test-claude"}}},
		extagent.AgentPi:     {Preferred: &extagent.AgentTransport{Agent: extagent.AgentPi, Kind: extagent.TransportCLI, Command: extagent.CommandSpec{Command: "missing-test-pi"}}},
	}, nil)
	defer b.Close()
	s := NewService(ServiceOptions{WorkspaceRoot: root, ExternalBroker: b})
	ch := NewWebUIChannel(s, true)
	id := ""
	for _, name := range []string{"codex", "pi", "claude"} {
		message := "/" + name + " /compact"
		if name == "pi" {
			message += " 保留决定"
		}
		body, _ := json.Marshal(map[string]string{"session_id": id, "agent": name, "agent_model": "future", "agent_effort": "high", "message": message, "turn_id": "compact-" + name})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/webui/chat?compact=1", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		ch.handleStreamChat(w, req)
		stream := w.Body.String()
		_, tail, _ := strings.Cut(stream, "event: done\ndata: ")
		var done webUIDoneEvent
		if json.Unmarshal([]byte(tail), &done) != nil || done.ReplyAgentCommand != "compact" || string(done.ActiveAgent) != name || string(done.ReplyAgent) != name || done.ReplyAgentModel != "" || done.ReplyAgentEffort != "" ||
			!strings.Contains(done.Reply, "还没有可压缩") || strings.Contains(stream, "event: external_command") || id != "" && done.SessionID != id {
			t.Fatal(stream)
		}
		id = done.SessionID
		resaved, err := store.Load(id)
		if err != nil || resaved.ExternalSessionID != "" || resaved.Model != "" || resaved.Effort != "" {
			t.Fatal(resaved, err)
		}
		replay := httptest.NewRecorder()
		ch.handleWebTurn(replay, httptest.NewRequest(http.MethodGet, "/api/v1/webui/turns?compact=1&turn_id=compact-"+name+"&after=0", nil))
		if !strings.Contains(replay.Body.String(), `"reply_agent_command":"compact"`) {
			t.Fatal(replay.Body.String())
		}
	}
	fresh := NewService(ServiceOptions{WorkspaceRoot: root})
	h, err := fresh.ConversationHistory(id, 0)
	if err != nil || len(h.Messages) != 6 || h.Messages[0].Content != "/codex /compact" || h.Messages[1].Sender != "codex" ||
		h.Messages[2].Content != "/pi /compact 保留决定" || h.Messages[3].Sender != "pi" || h.Messages[5].Sender != "claude" {
		t.Fatal(h.Messages, err)
	}
}

func TestWebUICompactRejectsUnsupportedInputAndKeepsACPCommandRouting(t *testing.T) {
	root := t.TempDir()
	store := extagent.NewStateStore(root)
	log := &groupACPLog{prompts: map[string][]string{}}
	detections := map[extagent.AgentName]extagent.DetectionResult{
		extagent.AgentCodex: {Preferred: &extagent.AgentTransport{Kind: extagent.TransportAppServer, Agent: extagent.AgentCodex}},
		extagent.AgentPi:    {Preferred: &extagent.AgentTransport{Kind: extagent.TransportACP, Agent: extagent.AgentPi}},
	}
	b := extagent.NewBroker(store, detections, func(context.Context, extagent.CommandSpec, string) (extagent.ACPClient, error) {
		return &groupFakeACP{name: "pi", log: log}, nil
	})
	defer b.Close()
	ch := NewWebUIChannel(NewService(ServiceOptions{WorkspaceRoot: root, ExternalBroker: b}), true)
	for _, tc := range []struct{ body, expect string }{
		{`{"agent":"codex","message":"/compact keep tests"}`, "Codex 的 /compact 不接受附加说明"},
		{`{"agent":"codex","message":"/compact","images":[{"mime_type":"image/png","data_base64":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="}]}`, "/compact 不接受附件"},
		{`{"agent":"pi","message":"/compact custom"}`, "pi 结论"},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/webui/chat", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		ch.handleStreamChat(w, req)
		if !strings.Contains(w.Body.String(), tc.expect) || strings.Contains(w.Body.String(), `"reply_agent_command":"compact"`) {
			t.Fatal(w.Body.String())
		}
	}
	if got := log.prompts["pi"]; len(got) != 1 || got[0] != "/compact custom" {
		t.Fatal(log.prompts)
	}
}

func TestWebUICompactDiscoveryKeepsControlOnCatalogFailure(t *testing.T) {
	root := t.TempDir()
	b := extagent.NewBroker(extagent.NewStateStore(root), map[extagent.AgentName]extagent.DetectionResult{
		extagent.AgentCodex: {Preferred: &extagent.AgentTransport{Agent: extagent.AgentCodex, Kind: extagent.TransportAppServer, Command: extagent.CommandSpec{Command: root + "/absent"}}},
	}, nil)
	defer b.Close()
	h := handler{service: NewService(ServiceOptions{WorkspaceRoot: root, ExternalBroker: b})}
	w := httptest.NewRecorder()
	h.handleWebUICommands(w, httptest.NewRequest(http.MethodGet, "/api/v1/webui/commands?agent=codex", nil))
	var got struct {
		Error    string
		Commands []webUIAgentCommand
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Error == "" || len(got.Commands) != 1 || got.Commands[0].Kind != "control" || got.Commands[0].Insert != "/codex /compact" {
		t.Fatal(w.Body.String())
	}
}
