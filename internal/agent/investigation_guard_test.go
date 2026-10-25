package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bqagent/internal/tools"
	"bqagent/internal/turn"
)

func investigationTestCall(name string, args map[string]any) ToolCall {
	encoded, _ := json.Marshal(args)
	return guardCall(name, string(encoded))
}

func investigationTestRound(g *investigationGuard, call ToolCall, result string, fresh bool) string {
	g.beginRound()
	g.observe(call, result, fresh)
	return g.finishRound()
}

func TestInvestigationVariedSearchesAndCooldown(t *testing.T) {
	guard := newLoopGuard(StageConfig{})
	for i := 1; i <= DefaultInvestigationRoundLimit*2; i++ {
		guard.investigation.beginRound()
		// Parallel lookups count as a single model round. Different arguments
		// and fresh results must not keep buying an unlimited investigation.
		for j := 0; j < 3; j++ {
			call := investigationTestCall("bash", map[string]any{"command": fmt.Sprintf("rg pattern%d file%d.go", i, j)})
			if action := guard.observeTool(call, fmt.Sprintf("match %d %d", i, j)); action.checkpointReason != "" {
				t.Fatal(action)
			}
		}
		prompt := guard.investigation.finishRound()
		if (prompt != "") != (i%DefaultInvestigationRoundLimit == 0) {
			t.Fatalf("round %d prompt=%q", i, prompt)
		}
		if prompt != "" {
			for _, required := range []string{"specific unresolved question", "authorized implementation", "read-only work", "do not edit files", "missing information or authorization", "requirement to run tests"} {
				if !strings.Contains(prompt, required) {
					t.Fatalf("missing %q in prompt", required)
				}
			}
		}
	}
}

func TestInvestigationMilestonesAndNonProgress(t *testing.T) {
	cases := []struct {
		name, tool, args, result string
		fresh, resets            bool
	}{
		{"edit", "edit_file", `{"path":"a.go"}`, "ok", true, true},
		{"write", "write_file", `{"path":"a.go"}`, "ok", true, true},
		{"verification", "bash", `{"command":"go test ./internal/agent -run '^TestOne$'"}`, "ok", true, true},
		{"repeated edit", "edit_file", `{"path":"a.go"}`, "ok", false, false},
		{"failed edit", "edit_file", `{"path":"a.go"}`, "Error: missing path", true, false},
		{"failed verification", "bash", `{"command":"go test ./x"}`, "Error: exit status 1", true, false},
		{"masked verification", "bash", `{"command":"go test ./x || true"}`, "FAIL", true, false},
		{"background running", "bash", `{"command":"go test ./x"}`, `{"job_id":"job","status":"running","command":"go test ./x"}`, true, false},
		{"background completed", "bash_wait", `{"job_id":"job"}`, `{"job_id":"job","status":"completed","exit_code":0,"command":"go test ./x","output":"ok"}`, true, true},
		{"background failed", "bash_wait", `{"job_id":"job"}`, `{"job_id":"job","status":"failed","exit_code":1,"command":"go test ./x"}`, true, false},
		{"unknown command", "bash", `{"command":"python3 inspect.py"}`, "source", true, false},
		{"bookkeeping", "task_update", `{}`, "saved", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := &investigationGuard{limit: DefaultInvestigationRoundLimit, rounds: DefaultInvestigationRoundLimit - 1}
			investigationTestRound(g, guardCall(tc.tool, tc.args), tc.result, tc.fresh)
			if (g.rounds == 0) != tc.resets {
				t.Fatalf("rounds=%d", g.rounds)
			}
		})
	}
}

func TestRunConversationInvestigationReminderAfterBatch(t *testing.T) {
	for _, sequential := range []bool{false, true} {
		t.Run(fmt.Sprintf("sequential=%t", sequential), func(t *testing.T) {
			client := &stubClient{}
			for i := 0; i < DefaultInvestigationRoundLimit; i++ {
				calls := []ToolCall{}
				for j := 0; j < 2; j++ {
					call := investigationTestCall("read_file", map[string]any{"path": fmt.Sprintf("%d-%d.go", i, j)})
					call.ID = fmt.Sprintf("read-%d-%d", i, j)
					calls = append(calls, call)
				}
				if sequential {
					calls = append(calls, taskUpdateStallCall(fmt.Sprintf("update-%d", i), i))
				}
				client.responses = append(client.responses, AssistantMessage{Content: "I will investigate further.", ToolCalls: calls})
			}
			client.responses = append(client.responses, AssistantMessage{Content: "review complete"})
			recorder := &capturingRecorder{}
			app := NewWithOptions(client, "", Options{
				Recorder: recorder, Context: ContextConfig{Enabled: false},
				Functions: map[string]tools.Function{
					"read_file":   func(context.Context, map[string]any) (string, error) { return "source", nil },
					"task_update": func(context.Context, map[string]any) (string, error) { return "saved", nil },
				},
			})
			result, history, err := app.RunConversationTurn(context.Background(), []map[string]any{{"role": "user", "content": "review only"}}, 20)
			if err != nil || result != "review complete" {
				t.Fatalf("result=%s err=%v", result, err)
			}
			for _, messages := range []([]map[string]any){history, recorder.recorded, client.messages[len(client.messages)-1]} {
				reminders := 0
				for i, message := range messages {
					if message["role"] == "developer" && strings.Contains(contentText(message["content"]), investigationRecoveryPrompt) {
						reminders++
						if i == 0 || messages[i-1]["role"] != "tool" {
							t.Fatal("reminder not after tool results")
						}
						want := "read-11-1"
						if sequential {
							want = "update-11"
						}
						if messages[i-1]["tool_call_id"] != want {
							t.Fatalf("last tool=%v want=%s", messages[i-1], want)
						}
					}
				}
				if reminders != 1 {
					t.Fatalf("reminders=%d", reminders)
				}
			}
		})
	}
}

func TestInvestigationReminderFollowsImages(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "shot.png"), []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), 0o644); err != nil {
		t.Fatal(err)
	}
	client := &stubClient{}
	for i := 0; i < DefaultInvestigationRoundLimit; i++ {
		call := investigationTestCall("bash", map[string]any{"command": fmt.Sprintf("rg pattern%d a.go", i)})
		call.ID = fmt.Sprint(i)
		calls := []ToolCall{call}
		if i == DefaultInvestigationRoundLimit-1 {
			calls = append(calls, ToolCall{ID: "image", Function: FunctionCall{Name: "read_file", Arguments: `{"path":"shot.png"}`}})
		}
		client.responses = append(client.responses, AssistantMessage{ToolCalls: calls})
	}
	client.responses = append(client.responses, AssistantMessage{Content: "done"})
	app := NewWithOptions(client, "", Options{Context: ContextConfig{Enabled: false}, Functions: map[string]tools.Function{
		"bash":      func(context.Context, map[string]any) (string, error) { return "match", nil },
		"read_file": tools.ReadFileFromRoot(root),
	}})
	_, _, err := app.RunConversationTurn(context.Background(), []map[string]any{{"role": "user", "content": "inspect"}}, 20)
	if err != nil {
		t.Fatal(err)
	}
	request := client.messages[len(client.messages)-1]
	for i, message := range request {
		if strings.Contains(contentText(message["content"]), investigationRecoveryPrompt) {
			if i < 3 || !IsToolImagesMessage(request[i-1]) || request[i-2]["tool_call_id"] != "image" || request[i-3]["role"] != "tool" {
				t.Fatal("reminder split tool results and image message")
			}
			return
		}
	}
	t.Fatal("missing reminder")
}

func TestInvestigationReminderProgressDescribesRuntimeAction(t *testing.T) {
	client := &stubClient{}
	for i := 0; i < DefaultInvestigationRoundLimit; i++ {
		call := investigationTestCall("read_file", map[string]any{"path": fmt.Sprintf("%d.go", i)})
		call.ID = fmt.Sprint(i)
		client.responses = append(client.responses, AssistantMessage{ToolCalls: []ToolCall{call}})
	}
	client.responses = append(client.responses, AssistantMessage{Content: "done"})
	var progress bytes.Buffer
	app := NewWithOptions(client, "", Options{
		Context:        ContextConfig{Enabled: false},
		ProgressWriter: &progress,
		Stage:          StageConfig{MaxIterations: 20, EmitProgress: true},
		Functions: map[string]tools.Function{
			"read_file": func(context.Context, map[string]any) (string, error) { return "source", nil },
		},
	})
	if _, _, err := app.RunConversationTurn(context.Background(), []map[string]any{{"role": "user", "content": "inspect"}}, 20); err != nil {
		t.Fatal(err)
	}
	// The progress line is shown to the user, so it must report what the
	// runtime did instead of addressing the model.
	if text := progress.String(); strings.Count(text, "调查已持续多轮，已提示模型收敛并推进下一步。") != 1 || strings.Contains(text, "请归纳") {
		t.Fatalf("progress = %q", text)
	}
}

func TestRunConversationInvestigationRoundLimitOption(t *testing.T) {
	for _, tc := range []struct {
		option, rounds, reminders int
	}{
		{option: 3, rounds: 7, reminders: 2},
		{option: 0, rounds: DefaultInvestigationRoundLimit, reminders: 1},
		{option: -1, rounds: DefaultInvestigationRoundLimit * 2, reminders: 0},
	} {
		t.Run(fmt.Sprint(tc.option), func(t *testing.T) {
			client := &stubClient{}
			for i := 0; i < tc.rounds; i++ {
				call := investigationTestCall("read_file", map[string]any{"path": fmt.Sprintf("%d.go", i)})
				call.ID = fmt.Sprint(i)
				client.responses = append(client.responses, AssistantMessage{ToolCalls: []ToolCall{call}})
			}
			client.responses = append(client.responses, AssistantMessage{Content: "done"})
			app := NewWithOptions(client, "", Options{
				Context:                 ContextConfig{Enabled: false},
				InvestigationRoundLimit: tc.option,
				Functions: map[string]tools.Function{
					"read_file": func(context.Context, map[string]any) (string, error) { return "source", nil },
				},
			})
			_, history, err := app.RunConversationTurn(context.Background(), []map[string]any{{"role": "user", "content": "inspect"}}, 50)
			if err != nil {
				t.Fatal(err)
			}
			reminders := 0
			for _, message := range history {
				if strings.Contains(contentText(message["content"]), investigationRecoveryPrompt) {
					reminders++
				}
			}
			if reminders != tc.reminders {
				t.Fatalf("reminders = %d, want %d", reminders, tc.reminders)
			}
		})
	}
}

func TestRunConversationInvestigationResetOnNewInput(t *testing.T) {
	for _, kind := range []turn.InputKind{turn.Steering, turn.FollowUp} {
		t.Run(string(kind), func(t *testing.T) {
			q := &turn.InputQueue{}
			client := &stubClient{}
			for i := 0; i < DefaultInvestigationRoundLimit+2; i++ {
				call := investigationTestCall("read_file", map[string]any{"path": fmt.Sprintf("%d.go", i)})
				call.ID = fmt.Sprint(i)
				client.responses = append(client.responses, AssistantMessage{ToolCalls: []ToolCall{call}})
				if i == DefaultInvestigationRoundLimit-2 && kind == turn.FollowUp {
					client.responses = append(client.responses, AssistantMessage{Content: "first review done"})
				}
			}
			client.responses = append(client.responses, AssistantMessage{Content: "done"})
			reads := 0
			app := NewWithOptions(client, "", Options{Context: ContextConfig{Enabled: false}, Functions: map[string]tools.Function{
				"read_file": func(context.Context, map[string]any) (string, error) {
					reads++
					if reads == DefaultInvestigationRoundLimit-1 {
						if err := q.Add(turn.Input{Kind: kind, Text: "inspect a different objective"}); err != nil {
							t.Error(err)
						}
					}
					return "source", nil
				},
			}})
			result, err := app.RunTurn(turn.WithInputQueue(context.Background(), q), []map[string]any{{"role": "user", "content": "inspect"}}, 20)
			if err != nil || result.Reply != "done" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			for _, request := range client.messages {
				for _, message := range request {
					if strings.Contains(contentText(message["content"]), investigationRecoveryPrompt) {
						t.Fatal("new input did not reset investigation")
					}
				}
			}
		})
	}
}
