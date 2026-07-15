package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bqagent/internal/agent"
)

func TestRunTraceHTTPAndFeedback(t *testing.T) {
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`))
	}))
	defer llm.Close()
	root := t.TempDir()
	service := NewService(ServiceOptions{WorkspaceRoot: root, Client: agent.NewClient("", llm.URL, nil), Model: "test", SystemPrompt: "test", RunTraceEnabled: true})
	api := httptest.NewServer(NewHandler(HandlerOptions{Service: service}))
	defer api.Close()
	response, err := http.Post(api.URL+"/api/v1/chat", "application/json", strings.NewReader(`{"message":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var chat chatResponse
	if err := json.NewDecoder(response.Body).Decode(&chat); err != nil {
		t.Fatal(err)
	}
	if chat.RunID == "" {
		t.Fatal("missing run_id")
	}
	feedback, err := http.Post(api.URL+"/api/v1/runs/"+chat.RunID+"/feedback", "application/json", strings.NewReader(`{"rating":"up"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer feedback.Body.Close()
	if feedback.StatusCode != http.StatusOK {
		t.Fatalf("feedback status=%d", feedback.StatusCode)
	}
	get, err := http.Get(api.URL + "/api/v1/runs/" + chat.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer get.Body.Close()
	if get.StatusCode != http.StatusOK {
		t.Fatalf("get status=%d", get.StatusCode)
	}
}

func TestRunTraceUsesEffectiveDefaultModel(t *testing.T) {
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer llm.Close()

	root := t.TempDir()
	service := NewService(ServiceOptions{WorkspaceRoot: root, Client: agent.NewClient("", llm.URL, nil), SystemPrompt: "test", RunTraceEnabled: true})
	api := httptest.NewServer(NewHandler(HandlerOptions{Service: service}))
	defer api.Close()

	response, err := http.Post(api.URL+"/api/v1/chat", "application/json", strings.NewReader(`{"message":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	var chat chatResponse
	if err := json.NewDecoder(response.Body).Decode(&chat); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	get, err := http.Get(api.URL + "/api/v1/runs/" + chat.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer get.Body.Close()
	var meta struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(get.Body).Decode(&meta); err != nil {
		t.Fatal(err)
	}
	if meta.Model != agent.DefaultModel {
		t.Fatalf("trace model = %q, want %q", meta.Model, agent.DefaultModel)
	}
}

func TestRunTraceDisabledByDefault(t *testing.T) {
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer llm.Close()

	root := t.TempDir()
	service := NewService(ServiceOptions{WorkspaceRoot: root, Client: agent.NewClient("", llm.URL, nil), SystemPrompt: "test"})
	api := httptest.NewServer(NewHandler(HandlerOptions{Service: service}))
	defer api.Close()

	response, err := http.Post(api.URL+"/api/v1/chat", "application/json", strings.NewReader(`{"message":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var chat chatResponse
	if err := json.NewDecoder(response.Body).Decode(&chat); err != nil {
		t.Fatal(err)
	}
	if chat.RunID != "" {
		t.Fatalf("run_id = %q, want empty when trace is disabled", chat.RunID)
	}
	if _, err := os.Stat(filepath.Join(root, ".agent", "runs")); !os.IsNotExist(err) {
		t.Fatalf("runs directory should not exist when trace is disabled, err=%v", err)
	}
}
