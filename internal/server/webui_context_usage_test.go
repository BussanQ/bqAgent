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

type usageACP struct{ groupFakeACP }

func (c *usageACP) Prompt(ctx context.Context, id, _ string) (string, error) {
	sink := extagent.ActivitySinkFromContext(ctx)
	sink(extagent.Activity{NativeSession: id, ContextUsage: &extagent.ContextUsage{Used: 345, Window: 900, Estimated: true}})
	return "done", nil
}

func TestWebUIContextUsageStreamsAndRestoresTheSameAgentsMeasurement(t *testing.T) {
	root := t.TempDir()
	newChannel := func() *WebUIChannel {
		native := &extagent.AgentTransport{Agent: extagent.AgentOpenCode, Kind: extagent.TransportACP}
		other := &extagent.AgentTransport{Agent: extagent.AgentPi, Kind: extagent.TransportCLI}
		b := extagent.NewBroker(extagent.NewStateStore(root), map[extagent.AgentName]extagent.DetectionResult{
			extagent.AgentOpenCode: {Preferred: native, ACP: native}, extagent.AgentPi: {Preferred: other, CLI: other},
		}, func(context.Context, extagent.CommandSpec, string) (extagent.ACPClient, error) {
			return &usageACP{groupFakeACP{name: "opencode"}}, nil
		})
		t.Cleanup(func() { _ = b.Close() })
		return NewWebUIChannel(NewService(ServiceOptions{WorkspaceRoot: root, ExternalBroker: b}), true)
	}
	ch := newChannel()
	out := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webui/chat?compact=1", strings.NewReader(`{"message":"hello","agent":"opencode"}`))
	req.Header.Set("Content-Type", "application/json")
	ch.handleStreamChat(out, req)
	_, frames, ok := strings.Cut(out.Body.String(), "event: external_context_usage\ndata: ")
	if !ok || !strings.Contains(frames, `"used":345`) {
		t.Fatal(out.Body.String())
	}
	_, tail, ok := strings.Cut(frames, "event: done\ndata: ")
	var done webUIDoneEvent
	if !ok || json.Unmarshal([]byte(tail), &done) != nil || done.SessionID == "" || done.Reply != "done" {
		t.Fatal(tail)
	}
	fresh := newChannel()
	idle := httptest.NewRecorder()
	fresh.handleWebTurn(idle, httptest.NewRequest(http.MethodGet, "/api/v1/webui/turns?view=context&agent=opencode&session_id="+done.SessionID, nil))
	var status struct {
		Usage *extagent.ContextUsage `json:"context_usage"`
	}
	if err := json.Unmarshal(idle.Body.Bytes(), &status); err != nil || status.Usage == nil || status.Usage.Used != 345 {
		t.Fatal(idle.Body.String(), err)
	}
	for _, name := range []string{"opencode", "pi"} {
		w := httptest.NewRecorder()
		fresh.handleAgentModels(w, httptest.NewRequest(http.MethodGet, "/api/v1/webui/agent-models?view=capabilities&agent="+name+"&session_id="+done.SessionID, nil))
		var menu extagent.ModelCatalog
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &menu) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		if name == "opencode" && (menu.ContextUsage == nil || menu.ContextUsage.Used != 345 || menu.ContextUsage.Window != 900) || name == "pi" && menu.ContextUsage != nil {
			t.Fatal(name, menu.ContextUsage)
		}
	}
}
