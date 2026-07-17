package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bqagent/internal/agent"
	"bqagent/internal/tools"
)

func TestWebUIServesIndex(t *testing.T) {
	root := t.TempDir()
	service := newTestService(root, "http://example.invalid")
	handler := NewHandler(HandlerOptions{Service: service, Channels: []Channel{NewWebUIChannel(service, true)}})
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	response, err := http.Get(apiServer.URL + "/")
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	if ct := response.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content-type = %q, want text/html", ct)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body failed: %v", err)
	}
	if !strings.Contains(string(body), "/api/v1/webui/chat") {
		t.Fatal("served page does not reference the chat endpoint")
	}
	page := string(body)
	for _, expected := range []string{
		`id="theme-toggle"`,
		`function renderMarkdown(source)`,
		`class="table-wrap"`,
		`class="copy-code"`,
		`row.className = "message-actions"`,
		`/api/v1/chat/stop`,
		`/api/v1/status`,
		`id="runtime-model"`,
		`loadRuntimeModel()`,
		`class="stop-icon"`,
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("served page missing WebUI feature %q", expected)
		}
	}
	if strings.Contains(page, "<script src=") {
		t.Fatal("served page should remain self-contained without external scripts")
	}
}

func TestWebUIDisabledDoesNotServeIndex(t *testing.T) {
	root := t.TempDir()
	service := newTestService(root, "http://example.invalid")
	handler := NewHandler(HandlerOptions{Service: service, Channels: []Channel{NewWebUIChannel(service, false)}})
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	response, err := http.Get(apiServer.URL + "/")
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when web UI disabled", response.StatusCode)
	}
}

func TestWebUIStreamChat(t *testing.T) {
	llmServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := writer.(http.Flusher)
		for _, chunk := range []string{"Hello", ", world"} {
			fmt.Fprintf(writer, "data: %s\n\n", streamDeltaJSON(chunk))
			if flusher != nil {
				flusher.Flush()
			}
		}
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer llmServer.Close()

	root := t.TempDir()
	service := newTestService(root, llmServer.URL)
	handler := NewHandler(HandlerOptions{Service: service, Channels: []Channel{NewWebUIChannel(service, true)}})
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	first := postWebUIChat(t, apiServer.URL, `{"message":"hi"}`)
	if ct := first.contentType; !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
	if !strings.Contains(first.raw, `"delta":"Hello"`) {
		t.Fatalf("stream missing delta events:\n%s", first.raw)
	}
	if !strings.Contains(first.raw, "event: progress") || !strings.Contains(first.raw, `"message":`) {
		t.Fatalf("stream missing progress event:\n%s", first.raw)
	}
	if first.done.Reply != "Hello, world" {
		t.Fatalf("reply = %q, want %q", first.done.Reply, "Hello, world")
	}
	if first.done.SessionID == "" {
		t.Fatal("done event missing session_id")
	}

	// A follow-up carrying the session_id must reuse the same conversation.
	second := postWebUIChat(t, apiServer.URL, fmt.Sprintf(`{"session_id":%q,"message":"again"}`, first.done.SessionID))
	if second.done.SessionID != first.done.SessionID {
		t.Fatalf("second session_id = %q, want %q", second.done.SessionID, first.done.SessionID)
	}
}

func TestWebUIStopCancelsActiveTurn(t *testing.T) {
	root := t.TempDir()
	client := &cancelAwareTurnClient{started: make(chan struct{}), canceled: make(chan struct{})}
	service := newTestService(root, "http://example.invalid")
	service.client = client
	channel := NewWebUIChannel(service, true)
	handler := NewHandler(HandlerOptions{Service: service, Channels: []Channel{channel}})
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	type streamResult struct {
		body string
		err  error
	}
	streamDone := make(chan streamResult, 1)
	go func() {
		response, err := http.Post(apiServer.URL+"/api/v1/webui/chat", "application/json", strings.NewReader(`{"message":"wait","turn_id":"turn-stop-1"}`))
		if err != nil {
			streamDone <- streamResult{err: err}
			return
		}
		defer response.Body.Close()
		payload, readErr := io.ReadAll(response.Body)
		streamDone <- streamResult{body: string(payload), err: readErr}
	}()

	select {
	case <-client.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for LLM request to start")
	}

	stopResponse, err := http.Post(apiServer.URL+"/api/v1/chat/stop", "application/json", strings.NewReader(`{"turn_id":"turn-stop-1"}`))
	if err != nil {
		t.Fatalf("POST stop failed: %v", err)
	}
	defer stopResponse.Body.Close()
	var stopPayload struct {
		Stopped bool `json:"stopped"`
	}
	if err := json.NewDecoder(stopResponse.Body).Decode(&stopPayload); err != nil {
		t.Fatalf("decode stop response: %v", err)
	}
	if !stopPayload.Stopped {
		t.Fatal("stop response reported no active turn")
	}

	select {
	case <-client.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for LLM request cancellation")
	}
	select {
	case result := <-streamDone:
		if result.err != nil {
			t.Fatalf("read stopped stream: %v", result.err)
		}
		if !strings.Contains(result.body, "event: stopped") {
			t.Fatalf("stream missing stopped event:\n%s", result.body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stopped stream to finish")
	}

	idleResponse, err := http.Post(apiServer.URL+"/api/v1/chat/stop", "application/json", strings.NewReader(`{"turn_id":"turn-stop-1"}`))
	if err != nil {
		t.Fatalf("POST idle stop failed: %v", err)
	}
	defer idleResponse.Body.Close()
	stopPayload.Stopped = true
	if err := json.NewDecoder(idleResponse.Body).Decode(&stopPayload); err != nil {
		t.Fatalf("decode idle stop response: %v", err)
	}
	if stopPayload.Stopped {
		t.Fatal("completed turn still reported as active")
	}
}

type cancelAwareTurnClient struct {
	started  chan struct{}
	canceled chan struct{}
}

func (client *cancelAwareTurnClient) CreateChatCompletion(ctx context.Context, _ string, _ []map[string]any, _ []tools.Definition) (agent.AssistantMessage, error) {
	return client.wait(ctx)
}

func (client *cancelAwareTurnClient) CreateChatCompletionStream(ctx context.Context, _ string, _ []map[string]any, _ []tools.Definition, _ func(string)) (agent.AssistantMessage, error) {
	return client.wait(ctx)
}

func (client *cancelAwareTurnClient) wait(ctx context.Context) (agent.AssistantMessage, error) {
	close(client.started)
	<-ctx.Done()
	close(client.canceled)
	return agent.AssistantMessage{}, ctx.Err()
}

type webUIResult struct {
	raw         string
	contentType string
	done        doneEvent
}

type doneEvent struct {
	SessionID string `json:"session_id"`
	Reply     string `json:"reply"`
}

func postWebUIChat(t *testing.T, baseURL, body string) webUIResult {
	t.Helper()
	response, err := http.Post(baseURL+"/api/v1/webui/chat", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/v1/webui/chat failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read stream failed: %v", err)
	}
	result := webUIResult{raw: string(payload), contentType: response.Header.Get("Content-Type")}
	result.done = parseDoneEvent(t, result.raw)
	return result
}

func parseDoneEvent(t *testing.T, raw string) doneEvent {
	t.Helper()
	idx := strings.Index(raw, "event: done")
	if idx < 0 {
		t.Fatalf("stream missing done event:\n%s", raw)
	}
	for _, line := range strings.Split(raw[idx:], "\n") {
		if strings.HasPrefix(line, "data: ") {
			var event doneEvent
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				t.Fatalf("failed to decode done payload: %v", err)
			}
			return event
		}
	}
	t.Fatalf("done event had no data line:\n%s", raw)
	return doneEvent{}
}

func streamDeltaJSON(content string) string {
	encoded, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{
			{"delta": map[string]string{"content": content}},
		},
	})
	return string(encoded)
}
