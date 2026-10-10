package runtime

import (
	"context"
	"strings"
	"testing"

	"bqagent/internal/agent"
	"bqagent/internal/session"
	"bqagent/internal/taskstate"
	"bqagent/internal/tools"
)

type runtimeTaskClient struct {
	t     *testing.T
	calls int
}

func (c *runtimeTaskClient) CreateChatCompletion(_ context.Context, _ string, messages []map[string]any, definitions []tools.Definition) (agent.AssistantMessage, error) {
	c.calls++
	foundTool, foundGoal := false, false
	for _, d := range definitions {
		if d.Function.Name == "task_update" {
			foundTool = true
		}
	}
	for _, m := range messages {
		content, _ := m["content"].(string)
		if strings.Contains(content, "saved coding objective") {
			foundGoal = true
		}
	}
	if !foundTool || !foundGoal {
		c.t.Fatalf("runtime did not restore task: tool=%v goal=%v", foundTool, foundGoal)
	}
	return agent.AssistantMessage{Role: "assistant", Content: "needs input"}, nil
}
func (c *runtimeTaskClient) CreateChatCompletionStream(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, _ func(string)) (agent.AssistantMessage, error) {
	return c.CreateChatCompletion(ctx, model, messages, definitions)
}

func TestRuntimeRestoresTaskFromSessionRecorder(t *testing.T) {
	root := t.TempDir()
	saved, err := session.NewStore(root).Create(session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = saved.SaveTaskState(taskstate.State{Goal: "saved coding objective", Status: "awaiting_input", Reason: "missing requirement", Criteria: []taskstate.Criterion{{Description: "implement requirement"}}}); err != nil {
		t.Fatal(err)
	}
	client := &runtimeTaskClient{t: t}
	runtime := Runtime{Client: client, Model: "test", WorkspaceRoot: root}
	app := runtime.NewAgent(nil, "test", saved, false)
	reply, err := app.Run(context.Background(), "status", 10)
	if err != nil || reply != "needs input" || client.calls != 1 {
		t.Fatalf("reply=%q err=%v calls=%d", reply, err, client.calls)
	}
}
