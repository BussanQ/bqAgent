package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bqagent/internal/providerconfig"
)

func TestRunUsesExplicitHelloTask(t *testing.T) {
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
		case "LLM_MODEL":
			return "test-model"
		default:
			return ""
		}
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(context.Background(), nil, &stdout, &stderr, []string{"Hello"}, getenv)
	if code != 0 {
		t.Fatalf("run returned code %d, want 0", code)
	}
	if !strings.Contains(stderr.String(), "[Runtime] api_type=openai model=test-model") {
		t.Fatalf("stderr = %q, want effective runtime model", stderr.String())
	}
	if len(seenRequest.Messages) < 2 || len(seenRequest.Messages) > 3 {
		t.Fatalf("messages length = %d, want stable prompt, optional memory snapshot, and user message", len(seenRequest.Messages))
	}
	if seenRequest.Messages[len(seenRequest.Messages)-1]["content"] != "Hello" {
		t.Fatalf("user message = %#v, want Hello", seenRequest.Messages[len(seenRequest.Messages)-1]["content"])
	}
	if systemPrompt, _ := seenRequest.Messages[0]["content"].(string); !strings.Contains(systemPrompt, "Current runtime model: test-model (API type: openai).") {
		t.Fatalf("system prompt = %q, want current model identity", systemPrompt)
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

func TestRunWithoutArgumentsStartsChatMode(t *testing.T) {
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestCount++
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"unexpected"}}]}`))
	}))
	defer server.Close()

	getenv := func(key string) string {
		if key == "OPENAI_BASE_URL" {
			return server.URL
		}
		return ""
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(context.Background(), strings.NewReader("/exit\n"), &stdout, &stderr, nil, getenv)
	if code != 0 {
		t.Fatalf("run returned code %d, want 0; stderr=%q", code, stderr.String())
	}
	if requestCount != 0 {
		t.Fatalf("API received %d requests, want 0 before chat input", requestCount)
	}
	if stdout.String() != "> " {
		t.Fatalf("stdout = %q, want chat prompt", stdout.String())
	}
}

func TestRunModesUseSavedProvider(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		stdin string
	}{
		{name: "single task", args: []string{"hello"}},
		{name: "chat", args: []string{"--chat"}, stdin: "hello\n/exit\n"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requestCount int
			var requestedModel string
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requestCount++
				var payload struct {
					Model string `json:"model"`
				}
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Fatalf("decode request: %v", err)
				}
				requestedModel = payload.Model
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
			}))
			defer server.Close()

			home := t.TempDir()
			agentDir := filepath.Join(home, ".agent")
			store := providerconfig.NewStore(agentDir)
			if err := store.Save(providerconfig.Config{
				ActiveProvider: "saved",
				Providers: []providerconfig.Provider{{
					ID:           "saved",
					Name:         "Saved Provider",
					APIType:      "openai",
					BaseURL:      server.URL,
					Models:       []string{"saved-model"},
					DefaultModel: "saved-model",
				}},
			}); err != nil {
				t.Fatal(err)
			}

			root := t.TempDir()
			getenv := func(key string) string {
				switch key {
				case "HOME":
					return home
				case "LLM_BASE_URL":
					return "http://127.0.0.1:1"
				case "LLM_MODEL":
					return "env-model"
				default:
					return ""
				}
			}
			var stdin io.Reader
			if test.stdin != "" {
				stdin = strings.NewReader(test.stdin)
			}
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := runWithDeps(context.Background(), stdin, &stdout, &stderr, test.args, getenv, runDeps{
				getwd: func() (string, error) { return root, nil },
			})
			if code != 0 {
				t.Fatalf("runWithDeps returned code %d, want 0; stderr=%q", code, stderr.String())
			}
			if requestCount != 1 {
				t.Fatalf("provider received %d requests, want 1", requestCount)
			}
			if requestedModel != "saved-model" {
				t.Fatalf("requested model = %q, want saved-model", requestedModel)
			}
		})
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
	if seenRequest.Messages[len(seenRequest.Messages)-1]["content"] != "read README.md" {
		t.Fatalf("user message = %#v, want joined argv string", seenRequest.Messages[len(seenRequest.Messages)-1]["content"])
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
	exported := map[string]string{}
	code := runWithDeps(context.Background(), nil, &stdout, &stderr, []string{"hello"}, func(string) string { return "" }, runDeps{
		getwd:      func() (string, error) { return root, nil },
		executable: func() (string, error) { return "bqagent-test", nil },
		setenv: func(key, value string) error {
			exported[key] = value
			return nil
		},
		startBackground: func(string, []string, string, string) error { return nil },
	})
	if code != 0 {
		t.Fatalf("runWithDeps returned code %d, want 0; stderr=%q", code, stderr.String())
	}
	if seenRequest.Model != "dotenv-model" {
		t.Fatalf("model = %q, want dotenv model", seenRequest.Model)
	}
	if len(seenRequest.Messages) < 2 || seenRequest.Messages[len(seenRequest.Messages)-1]["content"] != "hello" {
		t.Fatalf("messages = %#v, want hello request", seenRequest.Messages)
	}
	if exported["OPENAI_BASE_URL"] != server.URL || exported["OPENAI_MODEL"] != "dotenv-model" {
		t.Fatalf("exported environment = %#v, want dotenv values", exported)
	}
}

func TestLoadWorkspaceDotEnvCanBeSkippedForSubagentWorker(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("SELECTED_ONLY=value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	values := loadWorkspaceDotEnv(func(key string) string {
		if key == "BQAGENT_SKIP_WORKSPACE_DOTENV" {
			return "1"
		}
		return ""
	}, root, true)
	if len(values) != 0 {
		t.Fatalf("loadWorkspaceDotEnv returned %#v, want empty when skipped", values)
	}
	values = loadWorkspaceDotEnv(func(key string) string {
		if key == "BQAGENT_SKIP_WORKSPACE_DOTENV" {
			return "1"
		}
		return ""
	}, root, false)
	if values["SELECTED_ONLY"] != "value" {
		t.Fatalf("loadWorkspaceDotEnv returned %#v, want workspace value", values)
	}
}

func TestApplyDotEnvPreservesExistingProcessEnvironment(t *testing.T) {
	exported := map[string]string{}
	err := applyDotEnv(func(key string) string {
		if key == "LLM_MODEL" {
			return "process-model"
		}
		return ""
	}, func(key, value string) error {
		exported[key] = value
		return nil
	}, map[string]string{
		"LLM_MODEL":    "dotenv-model",
		"LLM_BASE_URL": "https://example.test/v1",
	})
	if err != nil {
		t.Fatalf("applyDotEnv returned error: %v", err)
	}
	if _, ok := exported["LLM_MODEL"]; ok {
		t.Fatalf("exported environment = %#v, want existing LLM_MODEL preserved", exported)
	}
	if exported["LLM_BASE_URL"] != "https://example.test/v1" {
		t.Fatalf("exported environment = %#v, want dotenv base URL", exported)
	}
}
