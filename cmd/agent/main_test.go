package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunUsesDefaultHelloTask(t *testing.T) {
	t.Helper()

	var seenRequest struct {
		Messages []map[string]any `json:"messages"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&seenRequest); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
	}))
	defer server.Close()

	getenv := func(key string) string {
		switch key {
		case "OPENAI_BASE_URL":
			return server.URL
		default:
			return ""
		}
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(context.Background(), nil, &stdout, &stderr, nil, getenv)
	if code != 0 {
		t.Fatalf("run returned code %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if len(seenRequest.Messages) != 2 {
		t.Fatalf("messages length = %d, want 2", len(seenRequest.Messages))
	}
	if seenRequest.Messages[1]["content"] != "Hello" {
		t.Fatalf("user message = %#v, want Hello", seenRequest.Messages[1]["content"])
	}
	if !strings.Contains(stdout.String(), "[Model] request=chat") {
		t.Fatalf("stdout = %q, want model timing log", stdout.String())
	}
	if !strings.Contains(stdout.String(), "[Agent] done") {
		t.Fatalf("stdout = %q, want agent log", stdout.String())
	}
	if !strings.Contains(stdout.String(), "[Turn] iterations=1 allow_plan=true") {
		t.Fatalf("stdout = %q, want turn timing log", stdout.String())
	}
	if !strings.HasSuffix(stdout.String(), "done\n") {
		t.Fatalf("stdout = %q, want final result", stdout.String())
	}
}

func TestRunJoinsArgumentsIntoSingleTask(t *testing.T) {
	t.Helper()

	var seenRequest struct {
		Messages []map[string]any `json:"messages"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&seenRequest); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	getenv := func(key string) string {
		switch key {
		case "OPENAI_BASE_URL":
			return server.URL
		default:
			return ""
		}
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(context.Background(), nil, &stdout, &stderr, []string{"read", "README.md"}, getenv)
	if code != 0 {
		t.Fatalf("run returned code %d, want 0", code)
	}
	if seenRequest.Messages[1]["content"] != "read README.md" {
		t.Fatalf("user message = %#v, want joined argv string", seenRequest.Messages[1]["content"])
	}
}

func TestRunWritesErrorsToStderr(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte(`{"error":"upstream failed"}`))
	}))
	defer server.Close()

	getenv := func(key string) string {
		switch key {
		case "OPENAI_BASE_URL":
			return server.URL
		default:
			return ""
		}
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(context.Background(), nil, &stdout, &stderr, []string{"hello"}, getenv)
	if code != 1 {
		t.Fatalf("run returned code %d, want 1", code)
	}
	if !strings.Contains(stdout.String(), "[Model] request=chat") {
		t.Fatalf("stdout = %q, want model timing log", stdout.String())
	}
	if !strings.Contains(stdout.String(), "[Turn] iterations=1 allow_plan=true") {
		t.Fatalf("stdout = %q, want turn timing log", stdout.String())
	}
	if stderr.Len() == 0 {
		t.Fatal("stderr was empty, want error output")
	}
}

func TestRunLoadsDotEnvFromWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("OPENAI_BASE_URL=http://example.invalid\nOPENAI_MODEL=dotenv-model\n"), 0o644); err != nil {
		t.Fatalf("failed to write .env: %v", err)
	}

	var seenRequest struct {
		Model    string           `json:"model"`
		Messages []map[string]any `json:"messages"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&seenRequest); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
	}))
	defer server.Close()

	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("OPENAI_BASE_URL="+server.URL+"\nOPENAI_MODEL=dotenv-model\n"), 0o644); err != nil {
		t.Fatalf("failed to rewrite .env: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runWithDeps(context.Background(), nil, &stdout, &stderr, []string{"hello"}, func(string) string { return "" }, runDeps{
		getwd:           func() (string, error) { return root, nil },
		executable:      func() (string, error) { return "bqagent-test", nil },
		startBackground: func(string, []string, string, string) error { return nil },
	})
	if code != 0 {
		t.Fatalf("runWithDeps returned code %d, want 0; stderr=%q", code, stderr.String())
	}
	if seenRequest.Model != "dotenv-model" {
		t.Fatalf("model = %q, want dotenv model", seenRequest.Model)
	}
	if len(seenRequest.Messages) != 2 || seenRequest.Messages[1]["content"] != "hello" {
		t.Fatalf("messages = %#v, want hello request", seenRequest.Messages)
	}
}
