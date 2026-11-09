package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bqagent/internal/extagent"
	"bqagent/internal/session"
)

func TestWebUIQueueCapturesAgentModeAndRefusesCrossModeSteering(t *testing.T) {
	root := t.TempDir()
	broker := extagent.NewBroker(extagent.NewStateStore(root), map[extagent.AgentName]extagent.DetectionResult{
		extagent.AgentCodex:  {Preferred: &extagent.AgentTransport{Agent: extagent.AgentCodex, Kind: extagent.TransportAppServer}},
		extagent.AgentClaude: {Preferred: &extagent.AgentTransport{Agent: extagent.AgentClaude, Kind: extagent.TransportCLI}},
		extagent.AgentPi:     {Preferred: &extagent.AgentTransport{Agent: extagent.AgentPi, Kind: extagent.TransportCLI}},
	}, nil)
	defer broker.Close()
	s := NewService(ServiceOptions{WorkspaceRoot: root, ExternalBroker: broker})
	saved, err := s.store.Create(session.CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ch := NewWebUIChannel(s, true)
	ch.Start(ctx)
	parent, _, _ := s.webTurns.start(ctx, "parent", saved.ID())
	parent.bind(s, saved.ID())
	defer s.webTurns.expire(parent)
	f := &queueFixture{ch: ch, s: s, saved: saved, parent: parent}
	for _, agent := range []extagent.AgentName{extagent.AgentCodex, extagent.AgentClaude} {
		req := f.submit("planned-"+string(agent), agent, "review changes")
		req.Kind, req.InteractionMode = "message", "plan"
		view, code, body := f.req(http.MethodPost, req)
		if code != 200 || len(view.Items) == 0 {
			t.Fatal(agent, code, body)
		}
		row := view.Items[len(view.Items)-1]
		if row.ID != req.ID || row.Picks == nil || row.Picks.InteractionMode != "plan" {
			t.Fatal(agent, row)
		}
		target := extagent.SessionState{Agent: agent, InteractionMode: "default"}
		if queueTargetMatches(target, &row, "") {
			t.Fatal(agent, "steered a planning request into an execution turn")
		}
		target.InteractionMode = "plan"
		if !queueTargetMatches(target, &row, "") {
			t.Fatal(agent, "same-mode steering blocked")
		}
		req.InteractionMode = "default"
		if _, code, _ := f.req(http.MethodPost, req); code != 409 {
			t.Fatal(agent, "reused receipt for different mode", code)
		}
	}
	req := f.submit("unsupported", extagent.AgentPi, "review")
	req.Kind, req.InteractionMode = "message", "plan"
	if _, code, _ := f.req(http.MethodPost, req); code != 400 {
		t.Fatal("accepted mode for Pi", code)
	}
}

func TestWebUIInteractionModeFollowsTheActualResponderAndStaysOutOfOtherAgents(t *testing.T) {
	root := t.TempDir()
	log := &groupACPLog{prompts: map[string][]string{}}
	broker := newGroupTestBroker(root, log, extagent.AgentCodex, extagent.AgentPi)
	defer broker.Close()
	ch := NewWebUIChannel(NewService(ServiceOptions{WorkspaceRoot: root, ExternalBroker: broker}), true)
	for _, tc := range []struct{ message, event string }{
		{"/pi other agent", "done"}, {"plan this", "error"},
	} {
		data, _ := json.Marshal(webUIChatRequest{Agent: extagent.AgentCodex, AgentInteractionMode: "plan", Message: tc.message})
		r := httptest.NewRequest(http.MethodPost, "/api/v1/webui/chat", strings.NewReader(string(data)))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		ch.handleStreamChat(w, r)
		body := w.Body.String()
		if !strings.Contains(body, "event: "+tc.event+"\n") || !strings.Contains(body, `"agent_interaction_mode":"plan"`) {
			t.Fatal(body)
		}
		if tc.event == "done" {
			if !strings.Contains(body, `"reply_agent":"pi"`) || strings.Contains(body, `"reply_agent_interaction_mode":"plan"`) {
				t.Fatal(body)
			}
		} else if !strings.Contains(body, "不支持切换执行或规划模式") {
			t.Fatal(body)
		}
	}
}
