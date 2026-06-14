package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
