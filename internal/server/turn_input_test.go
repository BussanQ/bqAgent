package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"bqagent/internal/agent"
	"bqagent/internal/tools"
	"bqagent/internal/turn"
)

func TestServiceAddTurnInputRejectsInvalidAndUnknown(t *testing.T) {
	service := newTestService(t.TempDir(), "http://example.invalid")
	if _, err := service.AddTurnInput("missing-turn", turn.Steering, "hi"); !errors.Is(err, ErrTurnNotFound) {
		t.Fatalf("unknown turn error = %v, want ErrTurnNotFound", err)
	}
	if _, err := service.AddTurnInput("missing-turn", turn.InputKind("bogus"), "hi"); err == nil || errors.Is(err, ErrTurnNotFound) {
		t.Fatalf("invalid kind error = %v", err)
	}
	if _, err := service.AddTurnInput("missing-turn", turn.Steering, "  "); err == nil || errors.Is(err, ErrTurnNotFound) {
		t.Fatalf("empty text error = %v", err)
	}
}

func TestHandleTurnReusesProvidedInputQueueAndReturnsPending(t *testing.T) {
	client := &cancelAwareTurnClient{started: make(chan struct{}), canceled: make(chan struct{})}
	service := newTestService(t.TempDir(), "http://example.invalid")
	service.client = client
	queue := &turn.InputQueue{}

	type result struct {
		response TurnResponse
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, err := service.HandleTurnWithOptions(context.Background(), TurnRequest{Message: "wait", TurnID: "queue-turn-1"}, TurnOptions{Stream: true, Inputs: queue})
		done <- result{response, err}
	}()
	select {
	case <-client.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for turn to start")
	}
	input, err := service.AddTurnInput("queue-turn-1", turn.FollowUp, " later ")
	if err != nil || input.ID == "" || input.Text != "later" {
		t.Fatalf("AddTurnInput = %+v, %v", input, err)
	}
	if !service.StopTurn("queue-turn-1") {
		t.Fatal("StopTurn reported no active turn")
	}
	select {
	case got := <-done:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("err = %v, want canceled", got.err)
		}
		if len(got.response.PendingInputs) != 1 || got.response.PendingInputs[0].ID != input.ID {
			t.Fatalf("pending = %+v", got.response.PendingInputs)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for turn")
	}
	if err := queue.Add(turn.Input{ID: "x", Kind: turn.Steering, Text: "late"}); !errors.Is(err, turn.ErrInputClosed) {
		t.Fatalf("late Add = %v, want ErrInputClosed", err)
	}
	if _, err := service.AddTurnInput("queue-turn-1", turn.Steering, "late"); !errors.Is(err, ErrTurnNotFound) {
		t.Fatalf("finished turn error = %v, want ErrTurnNotFound", err)
	}
}

func TestTurnInputRouteStatuses(t *testing.T) {
	client := &cancelAwareTurnClient{started: make(chan struct{}), canceled: make(chan struct{})}
	service := newTestService(t.TempDir(), "http://example.invalid")
	service.client = client
	channel := NewWebUIChannel(service, true)
	apiServer := httptest.NewServer(NewHandler(HandlerOptions{Service: service, Channels: []Channel{channel}}))
	defer apiServer.Close()

	streamDone := make(chan string, 1)
	go func() {
		response, err := http.Post(apiServer.URL+"/api/v1/webui/chat", "application/json", strings.NewReader(`{"message":"wait","turn_id":"turn-input-1"}`))
		if err != nil {
			streamDone <- err.Error()
			return
		}
		defer response.Body.Close()
		payload, _ := io.ReadAll(response.Body)
		streamDone <- string(payload)
	}()
	select {
	case <-client.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for turn to start")
	}

	post := func(body string) (int, string) {
		response, err := http.Post(apiServer.URL+"/api/v1/chat/input", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST input: %v", err)
		}
		defer response.Body.Close()
		payload, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(payload)
	}
	cases := []struct {
		name   string
		body   string
		status int
		want   string
	}{
		{"accepted", `{"turn_id":"turn-input-1","kind":"steering","text":"use Go"}`, http.StatusOK, `"status":"accepted"`},
		{"followup", `{"turn_id":"turn-input-1","kind":"follow_up","text":"then docs"}`, http.StatusOK, `"status":"accepted"`},
		{"empty", `{"turn_id":"turn-input-1","kind":"steering","text":"  "}`, http.StatusBadRequest, `"status":"rejected"`},
		{"badkind", `{"turn_id":"turn-input-1","kind":"x","text":"a"}`, http.StatusBadRequest, `"status":"rejected"`},
		{"attachments", `{"turn_id":"turn-input-1","kind":"steering","text":"a","images":[{"mime_type":"image/png"}]}`, http.StatusBadRequest, "attachments are not supported"},
		{"notfound", `{"turn_id":"nope-1","kind":"steering","text":"a"}`, http.StatusNotFound, `"status":"notfound"`},
	}
	for _, tc := range cases {
		status, body := post(tc.body)
		if status != tc.status || !strings.Contains(body, tc.want) {
			t.Fatalf("%s: status=%d body=%s", tc.name, status, body)
		}
	}

	if !service.StopTurn("turn-input-1") {
		t.Fatal("StopTurn reported no active turn")
	}
	select {
	case body := <-streamDone:
		for _, want := range []string{"event: stopped", `"pending_inputs"`, "use Go", "then docs"} {
			if !strings.Contains(body, want) {
				t.Fatalf("stopped stream missing %q:\n%s", want, body)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stopped stream")
	}
}

func TestStopTurnClosesInputQueueImmediately(t *testing.T) {
	registry := newActiveTurnRegistry()
	queue := &turn.InputQueue{}
	canceled := false
	if _, ok := registry.RegisterWithInputs("stop-close-1", func() { canceled = true }, queue); !ok {
		t.Fatal("register failed")
	}
	if err := registry.AddInput("stop-close-1", turn.Input{ID: "a", Kind: turn.Steering, Text: "before"}); err != nil {
		t.Fatal(err)
	}
	if !registry.Stop("stop-close-1") || !canceled {
		t.Fatal("stop did not cancel")
	}
	if err := registry.AddInput("stop-close-1", turn.Input{ID: "b", Kind: turn.Steering, Text: "after"}); !errors.Is(err, turn.ErrInputClosed) {
		t.Fatalf("AddInput after stop = %v, want ErrInputClosed", err)
	}
	if pending := queue.Close(); len(pending) != 1 || pending[0].ID != "a" {
		t.Fatalf("pending = %+v", pending)
	}
}

type recordingToolClient struct {
	mu        sync.Mutex
	responses []agent.AssistantMessage
	requests  [][]map[string]any
}

func (client *recordingToolClient) next(messages []map[string]any) agent.AssistantMessage {
	client.mu.Lock()
	defer client.mu.Unlock()
	copied := make([]map[string]any, len(messages))
	copy(copied, messages)
	client.requests = append(client.requests, copied)
	response := client.responses[0]
	client.responses = client.responses[1:]
	return response
}

func (client *recordingToolClient) CreateChatCompletion(_ context.Context, _ string, messages []map[string]any, _ []tools.Definition) (agent.AssistantMessage, error) {
	return client.next(messages), nil
}

func (client *recordingToolClient) CreateChatCompletionStream(_ context.Context, _ string, messages []map[string]any, _ []tools.Definition, _ func(string)) (agent.AssistantMessage, error) {
	return client.next(messages), nil
}

func TestWebUISteeringDuringToolReachesNextModelRequestAndSSE(t *testing.T) {
	root := t.TempDir()
	client := &recordingToolClient{responses: []agent.AssistantMessage{
		{ToolCalls: []agent.ToolCall{{ID: "tool-1", Function: agent.FunctionCall{Name: "execute_bash", Arguments: `{"command":"x"}`}}}},
		{Content: "corrected result"},
	}}
	catalog := tools.NewCatalog(tools.Options{WorkspaceRoot: root})
	functions := catalog.Registry()
	toolStarted := make(chan struct{})
	releaseTool := make(chan struct{})
	functions["execute_bash"] = func(ctx context.Context, _ map[string]any) (string, error) {
		close(toolStarted)
		select {
		case <-releaseTool:
			return "ok", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	service := NewService(ServiceOptions{WorkspaceRoot: root, Client: client, SystemPrompt: "system", ToolDefinitions: catalog.Definitions(), Functions: functions})
	channel := NewWebUIChannel(service, true)
	apiServer := httptest.NewServer(NewHandler(HandlerOptions{Service: service, Channels: []Channel{channel}}))
	defer apiServer.Close()

	streamDone := make(chan string, 1)
	go func() {
		response, err := http.Post(apiServer.URL+"/api/v1/webui/chat", "application/json", strings.NewReader(`{"message":"original task","turn_id":"turn-tool-1"}`))
		if err != nil {
			streamDone <- err.Error()
			return
		}
		defer response.Body.Close()
		payload, _ := io.ReadAll(response.Body)
		streamDone <- string(payload)
	}()
	select {
	case <-toolStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for tool")
	}
	response, err := http.Post(apiServer.URL+"/api/v1/chat/input", "application/json", strings.NewReader(`{"turn_id":"turn-tool-1","kind":"steering","text":"only analyze"}`))
	if err != nil {
		t.Fatal(err)
	}
	var accepted turnInputResponse
	if err := json.NewDecoder(response.Body).Decode(&accepted); err != nil || accepted.Status != "accepted" || accepted.ID == "" {
		t.Fatalf("accepted = %+v err=%v", accepted, err)
	}
	response.Body.Close()
	close(releaseTool)

	var body string
	select {
	case body = <-streamDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for stream")
	}
	client.mu.Lock()
	requests := client.requests
	client.mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("model requests = %d, want 2", len(requests))
	}
	has := func(messages []map[string]any) bool {
		for _, message := range messages {
			if message["role"] == "user" && message["content"] == "only analyze" {
				return true
			}
		}
		return false
	}
	if has(requests[0]) || !has(requests[1]) {
		t.Fatalf("steering visibility wrong: first=%v second=%v", has(requests[0]), has(requests[1]))
	}
	appliedAt := strings.Index(body, `"kind":"input.applied"`)
	doneAt := strings.Index(body, "event: done")
	if appliedAt < 0 || doneAt < 0 || appliedAt > doneAt {
		t.Fatalf("input.applied must precede done:\n%s", body)
	}
	for _, want := range []string{`"item_id":"` + accepted.ID + `"`, `"phase":"steering"`, `"text":"only analyze"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream missing %s:\n%s", want, body)
		}
	}
	if strings.Contains(body, "pending_inputs") {
		t.Fatalf("applied input must not be reported pending:\n%s", body)
	}
}

type inputProbeWriter struct {
	queue *turn.InputQueue
	mu    sync.Mutex
	errs  []error
}

func (writer *inputProbeWriter) Write(data []byte) (int, error) {
	err := writer.queue.Add(turn.Input{ID: "probe", Kind: turn.Steering, Text: "probe"})
	writer.mu.Lock()
	writer.errs = append(writer.errs, err)
	writer.mu.Unlock()
	return len(data), nil
}

// External agents and slash commands never consume the core input queue, so the
// queue must already be closed (409 closed for HTTP, fallback for the TUI) while
// the turn is still running.
func TestExternalRoutedTurnsClosePrivateInputQueueEarly(t *testing.T) {
	root := t.TempDir()
	service := newTestServiceWithPersistentExternal(root)
	defer func() { _ = service.externalBroker.Close() }()

	run := func(request TurnRequest) (TurnResponse, []error) {
		queue := &turn.InputQueue{}
		probe := &inputProbeWriter{queue: queue}
		response, err := service.HandleTurnWithOptions(context.Background(), request, TurnOptions{Inputs: queue, OutputWriter: probe})
		if err != nil {
			t.Fatalf("HandleTurnWithOptions(%q): %v", request.Message, err)
		}
		probe.mu.Lock()
		defer probe.mu.Unlock()
		return response, append([]error(nil), probe.errs...)
	}
	assertClosed := func(label string, errs []error) {
		if len(errs) == 0 {
			t.Fatalf("%s: output writer never observed the turn", label)
		}
		for _, err := range errs {
			if !errors.Is(err, turn.ErrInputClosed) {
				t.Fatalf("%s: Add during external turn = %v, want ErrInputClosed", label, err)
			}
		}
	}
	first, errs := run(TurnRequest{Message: "/claude hello external"})
	assertClosed("slash route", errs)
	_, errs = run(TurnRequest{SessionID: first.SessionID, Message: "follow up"})
	assertClosed("sticky route", errs)
}
