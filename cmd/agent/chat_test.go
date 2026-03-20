package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatModeMultiTurnConversation(t *testing.T) {
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestCount++
		reply := "reply-" + string(rune('0'+requestCount))
		writer.Header().Set("Content-Type", "application/json")
		response := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": reply}},
			},
		}
		_ = json.NewEncoder(writer).Encode(response)
	}))
	defer server.Close()

	getenv := func(key string) string {
		if key == "OPENAI_BASE_URL" {
			return server.URL
		}
		return ""
	}

	stdin := strings.NewReader("hello\nworld\n/exit\n")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	root := t.TempDir()

	code := runWithDeps(context.Background(), stdin, &stdout, &stderr, []string{"--chat"}, getenv, runDeps{
		getwd:           func() (string, error) { return root, nil },
		executable:      func() (string, error) { return "bqagent-test", nil },
		startBackground: func(string, []string, string, string) error { return nil },
	})
	if code != 0 {
		t.Fatalf("runWithDeps returned code %d, want 0; stderr=%q", code, stderr.String())
	}
	if requestCount != 2 {
		t.Fatalf("API received %d requests, want 2", requestCount)
	}
	output := stdout.String()
	if !strings.Contains(output, "reply-1") {
		t.Fatalf("output missing reply-1: %q", output)
	}
	if !strings.Contains(output, "reply-2") {
		t.Fatalf("output missing reply-2: %q", output)
	}
}

func TestChatModeWithInitialTask(t *testing.T) {
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestCount++
		writer.Header().Set("Content-Type", "application/json")
		response := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "done"}},
			},
		}
		_ = json.NewEncoder(writer).Encode(response)
	}))
	defer server.Close()

	getenv := func(key string) string {
		if key == "OPENAI_BASE_URL" {
			return server.URL
		}
		return ""
	}

	stdin := strings.NewReader("/exit\n")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	root := t.TempDir()

	code := runWithDeps(context.Background(), stdin, &stdout, &stderr, []string{"--chat", "read README"}, getenv, runDeps{
		getwd:           func() (string, error) { return root, nil },
		executable:      func() (string, error) { return "bqagent-test", nil },
		startBackground: func(string, []string, string, string) error { return nil },
	})
	if code != 0 {
		t.Fatalf("runWithDeps returned code %d, want 0; stderr=%q", code, stderr.String())
	}
	if requestCount != 1 {
		t.Fatalf("API received %d requests, want 1 (initial task)", requestCount)
	}
}

func TestChatModeExitsOnEOF(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		response := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "ok"}},
			},
		}
		_ = json.NewEncoder(writer).Encode(response)
	}))
	defer server.Close()

	getenv := func(key string) string {
		if key == "OPENAI_BASE_URL" {
			return server.URL
		}
		return ""
	}

	stdin := strings.NewReader("") // immediate EOF
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	root := t.TempDir()

	code := runWithDeps(context.Background(), stdin, &stdout, &stderr, []string{"--chat"}, getenv, runDeps{
		getwd:           func() (string, error) { return root, nil },
		executable:      func() (string, error) { return "bqagent-test", nil },
		startBackground: func(string, []string, string, string) error { return nil },
	})
	if code != 0 {
		t.Fatalf("runWithDeps returned code %d, want 0 on EOF; stderr=%q", code, stderr.String())
	}
}

func TestChatCannotCombineWithBackground(t *testing.T) {
	_, _, err := parseCLI([]string{"--chat", "--background", "task"})
	if err == nil {
		t.Fatal("parseCLI returned nil error, want --chat/--background conflict")
	}
	if !strings.Contains(err.Error(), "--chat cannot be combined with --background") {
		t.Fatalf("error = %q, want chat/background conflict message", err.Error())
	}
}
