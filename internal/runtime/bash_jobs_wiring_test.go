package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"bqagent/internal/agent"
	"bqagent/internal/tools"
)

type bashWiringClient struct {
	mu       sync.Mutex
	calls    int
	toolText string
}

func (c *bashWiringClient) CreateChatCompletion(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition) (agent.AssistantMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls == 1 {
		return agent.AssistantMessage{Role: "assistant", ToolCalls: []agent.ToolCall{{ID: "call_1", Type: "function", Function: agent.FunctionCall{Name: "execute_bash", Arguments: `{"command":"sleep 5"}`}}}}, nil
	}
	for _, message := range messages {
		if message["role"] == "tool" {
			if text, ok := message["content"].(string); ok {
				c.toolText = text
			}
		}
	}
	return agent.AssistantMessage{Role: "assistant", Content: "done"}, nil
}

func (c *bashWiringClient) CreateChatCompletionStream(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, onChunk func(string)) (agent.AssistantMessage, error) {
	return c.CreateChatCompletion(ctx, model, messages, definitions)
}

func TestBashYieldTimeoutEnvReachesRuntimeAgentExecuteBash(t *testing.T) {
	env := map[string]string{"BASH_YIELD_TIMEOUT": "200ms", "BASH_MAX_CONCURRENT": "2"}
	config := ConfigFromEnv(func(key string) string { return env[key] })
	rt := Factory{Config: config, WorkspaceRoot: t.TempDir(), Getenv: func(string) string { return "" }}.Build(context.Background())
	if rt.BashJobs.YieldTimeout != 200*time.Millisecond || rt.BashJobs.MaxConcurrent != 2 {
		t.Fatalf("runtime.BashJobs = %+v", rt.BashJobs)
	}
	client := &bashWiringClient{}
	rt.Client = client

	start := time.Now()
	app := rt.NewAgent(nil, "", nil, false)
	if _, err := app.RunTurn(context.Background(), []map[string]any{{"role": "user", "content": "run it"}}, 5); err != nil {
		t.Fatalf("RunTurn error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("turn took %v; yield timeout was not applied or job was not reclaimed", elapsed)
	}
	var result struct {
		Status string `json:"status"`
		JobID  string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(client.toolText), &result); err != nil || result.Status != "running" || strings.TrimSpace(result.JobID) == "" {
		t.Fatalf("execute_bash result = %q (err %v), want running with job_id", client.toolText, err)
	}
}
