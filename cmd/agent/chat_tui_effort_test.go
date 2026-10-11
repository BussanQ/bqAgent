package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"bqagent/internal/agent"
	appserver "bqagent/internal/server"
	"bqagent/internal/tools"
	apptui "bqagent/internal/tui"
	"bqagent/internal/workspace"
)

func TestTUIEffortCommand(t *testing.T) {
	// Commands work before a session/provider exists and never call the model.
	backend := &tuiBackend{}
	for _, test := range []struct{ command, want string }{
		{"/effort", "auto"}, {"/effort low", "low"}, {"/effort medium", "medium"},
		{"/effort high", "high"}, {" /EFFORT MAX ", "max"}, {"/effort", "max"},
	} {
		result, err := backend.RunTurn(context.Background(), "session", test.command, apptui.TurnEvents{})
		if err != nil || result.SessionID != "session" || !strings.Contains(result.Reply, "当前推理努力程度："+test.want) {
			t.Fatalf("%s: result=%+v err=%v", test.command, result, err)
		}
	}
	for _, command := range []string{"/effort invalid", "/effort high extra"} {
		if _, err := backend.RunTurn(context.Background(), "", command, apptui.TurnEvents{}); err == nil || backend.currentEffort() != agent.ReasoningEffortMax {
			t.Fatalf("invalid command changed preference: %s, err=%v", command, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := backend.RunTurn(ctx, "", "/effort auto", apptui.TurnEvents{}); !errors.Is(err, context.Canceled) || backend.currentEffort() != agent.ReasoningEffortMax {
		t.Fatalf("cancelled command changed preference: %v", err)
	}
	if _, err := backend.RunTurn(context.Background(), "", "/effort auto", apptui.TurnEvents{}); err != nil || backend.currentEffort() != agent.ReasoningEffortAuto {
		t.Fatalf("reset effort: %v", err)
	}
	if _, handled, _ := backend.handleEffortCommand("/effortless"); handled {
		t.Fatal("matched unrelated command")
	}
}

type tuiEffortClient struct {
	sessionNoticeClient
	seen []agent.ReasoningEffort
}

func (client *tuiEffortClient) CreateChatCompletionStreamWithOptions(_ context.Context, _ string, _ []map[string]any, _ []tools.Definition, options agent.ChatCompletionOptions, onChunk func(string)) (agent.AssistantMessage, error) {
	client.seen = append(client.seen, options.ReasoningEffort)
	if len(client.seen)%2 == 1 {
		return agent.AssistantMessage{ToolCalls: []agent.ToolCall{{ID: "lookup", Function: agent.FunctionCall{Name: "lookup", Arguments: `{}`}}}}, nil
	}
	if onChunk != nil {
		onChunk("done")
	}
	return agent.AssistantMessage{Role: "assistant", Content: "done"}, nil
}

func TestTUIEffortReusesModelOptionsAcrossToolRounds(t *testing.T) {
	client := &tuiEffortClient{sessionNoticeClient: sessionNoticeClient{onCall: func() { t.Fatal("options path was bypassed") }}}
	ws := &workspace.Workspace{Root: t.TempDir(), GlobalAgentDir: t.TempDir()}
	backend := &tuiBackend{workspace: ws, service: appserver.NewService(appserver.ServiceOptions{
		WorkspaceRoot: ws.Root, AgentDir: ws.AgentDir(), Client: client, Model: "test", SystemPrompt: "test",
		Functions: map[string]tools.Function{"lookup": func(context.Context, map[string]any) (string, error) { return "found", nil }},
	})}
	for _, effort := range []string{"max", "high", "auto"} {
		if _, err := backend.RunTurn(context.Background(), "", "/effort "+effort, apptui.TurnEvents{}); err != nil {
			t.Fatal(err)
		}
		before := len(client.seen)
		result, err := backend.RunTurn(context.Background(), "", "look up an answer", apptui.TurnEvents{})
		if err != nil || result.Reply != "done" || len(client.seen) != before+2 {
			t.Fatalf("effort=%s result=%+v seen=%v err=%v", effort, result, client.seen, err)
		}
		want, _ := agent.ParseReasoningEffort(effort)
		for _, got := range client.seen[before:] {
			if got != want {
				t.Fatalf("effort=%s model received %q", effort, got)
			}
		}
	}
	for _, command := range backend.Commands() {
		if command.Name == "/effort" {
			var values []string
			for _, arg := range command.Arguments {
				values = append(values, arg.Value)
			}
			if strings.Join(values, ",") != "auto,low,medium,high,max" {
				t.Fatalf("effort completions=%v", values)
			}
			return
		}
	}
	t.Fatal("/effort not discoverable")
}
