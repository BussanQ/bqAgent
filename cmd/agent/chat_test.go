package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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

func TestChatModeSupportsExternalAgentRouting(t *testing.T) {
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestCount++
		t.Fatalf("LLM server should not be called for external agent route")
	}))
	defer server.Close()

	getenv := func(key string) string {
		switch key {
		case "OPENAI_BASE_URL":
			return server.URL
		case "AGENT_CLAUDE_CLI_CMD":
			return os.Args[0]
		case "AGENT_CLAUDE_CLI_ARGS":
			return "-test.run=TestChatExternalHelperProcess -- chat-cli-claude"
		default:
			return ""
		}
	}

	stdin := strings.NewReader("/claude hello\n/exit\n")
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
	if requestCount != 0 {
		t.Fatalf("LLM request count = %d, want 0", requestCount)
	}
	if !strings.Contains(stdout.String(), "claude reply") {
		t.Fatalf("stdout = %q, want external agent reply", stdout.String())
	}
}

func TestChatModeCanSwitchBackToDefaultModel(t *testing.T) {
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestCount++
		writer.Header().Set("Content-Type", "application/json")
		response := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "default reply"}},
			},
		}
		_ = json.NewEncoder(writer).Encode(response)
	}))
	defer server.Close()

	getenv := func(key string) string {
		switch key {
		case "OPENAI_BASE_URL":
			return server.URL
		case "AGENT_CLAUDE_CLI_CMD":
			return os.Args[0]
		case "AGENT_CLAUDE_CLI_ARGS":
			return "-test.run=TestChatExternalHelperProcess -- chat-cli-claude"
		default:
			return ""
		}
	}

	stdin := strings.NewReader("/claude hello\n/default\nhello model\n/exit\n")
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
	if requestCount != 1 {
		t.Fatalf("LLM request count = %d, want 1 after /default", requestCount)
	}
	output := stdout.String()
	if !strings.Contains(output, "claude reply") {
		t.Fatalf("stdout = %q, want external agent reply", output)
	}
	if !strings.Contains(output, "switched to default model") {
		t.Fatalf("stdout = %q, want switch confirmation", output)
	}
	if !strings.Contains(output, "default reply") {
		t.Fatalf("stdout = %q, want default model reply", output)
	}
}

func TestChatExternalHelperProcess(t *testing.T) {
	if len(os.Args) < 4 || os.Args[2] != "--" {
		return
	}
	if os.Args[3] != "chat-cli-claude" {
		return
	}
	_, _ = os.Stdout.WriteString(`{"result":"claude reply","session_id":"claude-session-1"}`)
	os.Exit(0)
}
