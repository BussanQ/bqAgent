package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"bqagent/internal/agent"
	"bqagent/internal/turn"
)

func TestChatEndpointPreservesFailureAndCancellationOutcome(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status turn.Status
	}{
		{name: "failure", err: errors.New("provider failed"), status: turn.Failed},
		{name: "cancellation", err: context.Canceled, status: turn.Interrupted},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := NewService(ServiceOptions{WorkspaceRoot: t.TempDir(), Client: failingTurnClient{test.err}, Model: "test", SystemPrompt: "test"})
			request := httptest.NewRequest(http.MethodPost, "/api/v1/chat", strings.NewReader(`{"message":"work","turn_id":"http-turn"}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			(&handler{service: service}).handleChat(response, request)
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			var payload chatResponse
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.TurnID != "http-turn" || payload.SessionID == "" || !strings.Contains(payload.Error, test.err.Error()) {
				t.Fatalf("missing turn identity or error: %+v", payload)
			}
			if payload.Outcome == nil || payload.Outcome.Status != test.status || !strings.Contains(payload.Outcome.Reason, test.err.Error()) || payload.Outcome.Notice == "" {
				t.Fatalf("outcome = %+v, want %s with reason and notice", payload.Outcome, test.status)
			}
			saved, err := service.store.Open(payload.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Meta().LastTurnOutcome == nil || *saved.Meta().LastTurnOutcome != *payload.Outcome {
				t.Fatalf("HTTP outcome differs from persisted outcome: %+v, %+v", payload.Outcome, saved.Meta().LastTurnOutcome)
			}
		})
	}
}

func TestChatEndpointTaskRelationJSONAndForm(t *testing.T) {
	for _, contentType := range []string{"application/json", "application/x-www-form-urlencoded"} {
		for _, test := range []struct {
			name string
			raw  string
			want turn.Relation
		}{
			{name: "omitted", want: turn.ContinueTask},
			{name: "continue", raw: "continue", want: turn.ContinueTask},
			{name: "side question", raw: "side_question", want: turn.SideQuestion},
			{name: "trim whitespace", raw: " side_question ", want: turn.SideQuestion},
			{name: "invalid", raw: "replace_task"},
		} {
			t.Run(contentType+"/"+test.name, func(t *testing.T) {
				values := map[string]string{"message": "当前项目用什么语言？"}
				if test.raw != "" {
					values["task_relation"] = test.raw
				}
				var body string
				if contentType == "application/json" {
					data, err := json.Marshal(values)
					if err != nil {
						t.Fatal(err)
					}
					body = string(data)
				} else {
					form := url.Values{}
					for key, value := range values {
						form.Set(key, value)
					}
					body = form.Encode()
				}
				client := &taskScriptClient{responses: []agent.AssistantMessage{{Content: "主要用 Go", Phase: "final_answer"}}}
				service := NewService(ServiceOptions{WorkspaceRoot: t.TempDir(), Client: client, Model: "test", SystemPrompt: "test"})
				request := httptest.NewRequest(http.MethodPost, "/api/v1/chat", strings.NewReader(body))
				request.Header.Set("Content-Type", contentType)
				response := httptest.NewRecorder()
				(&handler{service: service}).handleChat(response, request)
				var payload chatResponse
				if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
					t.Fatal(err)
				}
				if test.name == "invalid" {
					if response.Code != http.StatusBadRequest || payload.Error != "invalid task_relation" || payload.TurnID != "" {
						t.Fatalf("invalid relation was not rejected before starting a turn: status=%d payload=%+v", response.Code, payload)
					}
					return
				}
				if response.Code != http.StatusOK || payload.Outcome == nil || payload.Outcome.TaskRelation != test.want || payload.Reply != "主要用 Go" {
					t.Fatalf("status=%d payload=%+v outcome=%+v, want relation %s", response.Code, payload, payload.Outcome, test.want)
				}
			})
		}
	}
}

func TestParseTurnRequestDefaultsReasoningEffortToHigh(t *testing.T) {
	turnRequest, err := parseTurnRequest(map[string]string{"message": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if turnRequest.ReasoningEffort != agent.DefaultReasoningEffort || agent.DefaultReasoningEffort != agent.ReasoningEffortHigh {
		t.Fatalf("reasoning effort = %q, want high", turnRequest.ReasoningEffort)
	}
}
