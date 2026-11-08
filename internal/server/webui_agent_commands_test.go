package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"bqagent/internal/extagent"
)

type nativeCommandsACP struct{ groupFakeACP }

func (c *nativeCommandsACP) AvailableCommands(_ context.Context, id string) ([]extagent.NativeCommand, error) {
	return []extagent.NativeCommand{{Name: "model", Text: "/model", Description: id, Hint: "[name]"}}, nil
}

func TestWebUIAgentCommandCompletionRoutesPastCollidingBuiltin(t *testing.T) {
	root := t.TempDir()
	log := &groupACPLog{prompts: map[string][]string{}}
	count := 0
	broker := extagent.NewBroker(extagent.NewStateStore(root), map[extagent.AgentName]extagent.DetectionResult{
		extagent.AgentPi: {Preferred: &extagent.AgentTransport{Agent: extagent.AgentPi, Kind: extagent.TransportACP}},
	}, func(context.Context, extagent.CommandSpec, string) (extagent.ACPClient, error) {
		count++
		return &nativeCommandsACP{groupFakeACP{name: "pi", log: log}}, nil
	})
	defer broker.Close()
	s := NewService(ServiceOptions{WorkspaceRoot: root, ExternalBroker: broker})
	h := &handler{service: s}
	read := func(id string) webUIAgentCommand {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/webui/commands?agent=pi&session_id="+id, nil)
		w := httptest.NewRecorder()
		h.handleWebUICommands(w, req)
		var result struct{ Commands []webUIAgentCommand }
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Commands) != 1 {
			t.Fatal(w.Code, w.Body.String())
		}
		row := result.Commands[0]
		if row.Agent != "pi" || row.Insert != "/pi /model" || row.Hint != "[name]" || row.Description != "pi-session" {
			t.Fatal(row)
		}
		return row
	}
	row := read("")
	if len(log.prompts) != 0 || count != 1 {
		t.Fatal("discovery executed a prompt", log.prompts, count)
	}
	ch := NewWebUIChannel(s, true)
	body, _ := json.Marshal(map[string]string{"agent": "pi", "message": row.Insert + " custom", "turn_id": "native-command"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webui/chat", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	ch.handleStreamChat(w, req)
	_, tail, _ := strings.Cut(w.Body.String(), "event: done\ndata: ")
	var done webUIDoneEvent
	if json.Unmarshal([]byte(tail), &done) != nil || done.ReplyAgent != "pi" || done.Reply != "pi 结论" || !slices.Equal(log.prompts["pi"], []string{"/model custom"}) {
		t.Fatal(w.Body.String(), log.prompts)
	}
	read(done.SessionID)
	if count != 2 {
		t.Fatal("active ACP session was reopened", count)
	}
	for _, query := range []string{"agent=missing", "agent=pi&conversation_type=group", "agent=pi&session_id=..%2Fother"} {
		w := httptest.NewRecorder()
		h.handleWebUICommands(w, httptest.NewRequest(http.MethodGet, "/api/v1/webui/commands?"+query, nil))
		if w.Code != http.StatusBadRequest {
			t.Fatal(query, w.Code, w.Body.String())
		}
	}
	if count != 2 {
		t.Fatal("invalid request started a probe")
	}
}
