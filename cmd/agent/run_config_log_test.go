package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appmemory "bqagent/internal/memory"
)

func TestBackgroundSessionLogsRunConfigWithoutAutoMemory(t *testing.T) {
	root := t.TempDir()
	memoryDir := filepath.Join(root, ".agent", "memory")
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
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
		}
		return ""
	}
	// The background child's stdout/stderr are its session output.log.
	var stdout, stderr bytes.Buffer
	code := runWithDeps(context.Background(), nil, &stdout, &stderr, []string{"--session-run", "hello"}, getenv, runDeps{
		getwd:           func() (string, error) { return root, nil },
		executable:      func() (string, error) { return "bqagent-test", nil },
		setenv:          func(string, string) error { return nil },
		startBackground: func(string, []string, string, string) error { return nil },
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	log := stderr.String()
	for _, want := range []string{"[Session] id=", "mode=background", "[Runtime] api_type=openai model=test-model provider=env workspace=", "max_iterations=", "investigation_round_limit=12", "task_tool=", "run_trace=false", "context="} {
		if !strings.Contains(log, want) {
			t.Fatalf("run log missing %q:\n%s", want, log)
		}
	}
	entries, err := appmemory.NewStore(memoryDir).Active()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Content, "Task: ") {
			t.Fatalf("finished task was saved to memory: %+v", entry)
		}
	}
}
