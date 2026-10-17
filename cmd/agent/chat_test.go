package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"bqagent/internal/agent"
	appserver "bqagent/internal/server"
	"bqagent/internal/tools"
	apptui "bqagent/internal/tui"
	"bqagent/internal/workspace"
)

// tuiMultiTurnClient is an in-process fake model: no network, no subprocess.
type tuiMultiTurnClient struct {
	sessionNoticeClient
	requests [][]map[string]any
}

func (client *tuiMultiTurnClient) CreateChatCompletionStreamWithOptions(_ context.Context, _ string, messages []map[string]any, _ []tools.Definition, _ agent.ChatCompletionOptions, onChunk func(string)) (agent.AssistantMessage, error) {
	client.requests = append(client.requests, append([]map[string]any(nil), messages...))
	reply := fmt.Sprintf("reply-%d", len(client.requests))
	if onChunk != nil {
		onChunk(reply)
	}
	return agent.AssistantMessage{Role: "assistant", Content: reply}, nil
}

func newMultiTurnBackend(t *testing.T, client *tuiMultiTurnClient) *tuiBackend {
	t.Helper()
	ws := &workspace.Workspace{Root: t.TempDir(), GlobalAgentDir: t.TempDir()}
	return &tuiBackend{workspace: ws, service: appserver.NewService(appserver.ServiceOptions{
		WorkspaceRoot: ws.Root, AgentDir: ws.AgentDir(), Client: client, Model: "test", SystemPrompt: "test",
	})}
}

func TestTUIBackendMultiTurnReusesSessionAndHistory(t *testing.T) {
	client := &tuiMultiTurnClient{}
	backend := newMultiTurnBackend(t, client)
	first, err := backend.RunTurn(context.Background(), "", "hello", apptui.TurnEvents{})
	if err != nil || first.SessionID == "" || first.Reply != "reply-1" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := backend.RunTurn(context.Background(), first.SessionID, "world", apptui.TurnEvents{})
	if err != nil || second.SessionID != first.SessionID || second.Reply != "reply-2" {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if len(client.requests) != 2 {
		t.Fatalf("model requests = %d, want 2", len(client.requests))
	}
	// The second request must carry the first user message and assistant reply.
	text := fmt.Sprint(client.requests[1])
	for _, want := range []string{"hello", "reply-1", "world"} {
		if !strings.Contains(text, want) {
			t.Fatalf("second request missing %q: %s", want, text)
		}
	}
	history, err := backend.History(first.SessionID, apptui.ResumeHistoryBudget)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, message := range history.Messages {
		got = append(got, message.Role+":"+message.Content)
	}
	want := []string{"user:hello", "assistant:reply-1", "user:world", "assistant:reply-2"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("history = %v, want %v", got, want)
	}
}

func TestRemovedChatFlagIsRejectedBeforeWorkspaceSetup(t *testing.T) {
	for _, args := range [][]string{{"--chat"}, {"--chat", "read README"}, {"--chat", "--resume", "saved-session"}} {
		var stdout, stderr bytes.Buffer
		code := runWithDeps(context.Background(), unreadableStdin{t}, &stdout, &stderr, args, func(string) string {
			t.Fatal("rejected flag must not read environment")
			return ""
		}, noSideEffectDeps(t))
		if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "flag provided but not defined: -chat") {
			t.Fatalf("args=%v exit=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestNonTTYWithoutTaskIsRejectedWithoutSideEffects(t *testing.T) {
	for _, args := range [][]string{nil, {" "}, {"--resume", "saved-session"}, {"--resume", "saved-session", " "}} {
		var stdout, stderr bytes.Buffer
		// strings.Reader is never a terminal, so the TUI cannot start.
		code := runWithDeps(context.Background(), unreadableStdin{t}, &stdout, &stderr, args, func(key string) string {
			if key != "TERM" {
				t.Fatalf("unexpected environment read %q before rejection", key)
			}
			return "xterm-256color"
		}, noSideEffectDeps(t))
		if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "TUI") || !strings.Contains(stderr.String(), "TTY") || !strings.Contains(stderr.String(), "任务描述") {
			t.Fatalf("args=%q exit=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestSessionRunWithoutTaskFailsEarlyWithoutDeps(t *testing.T) {
	for _, args := range [][]string{{"--session-run"}, {"--session-run", "--resume", "saved-session"}, {"--session-run", " "}} {
		var stdout, stderr bytes.Buffer
		code := runWithDeps(context.Background(), unreadableStdin{t}, &stdout, &stderr, args, func(string) string {
			t.Fatal("must not read environment")
			return ""
		}, noSideEffectDeps(t))
		if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "session runner requires a task") {
			t.Fatalf("args=%q exit=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestHelpHasNoEnvironmentOrWorkspaceSideEffects(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runWithDeps(context.Background(), nil, &stdout, &stderr, []string{"--help"}, func(string) string {
		t.Fatal("help must not read environment")
		return ""
	}, noSideEffectDeps(t))
	if code != 0 || stderr.Len() != 0 || stdout.Len() == 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func noSideEffectDeps(t *testing.T) runDeps {
	t.Helper()
	return runDeps{
		getwd:      func() (string, error) { t.Fatal("getwd called"); return "", nil },
		executable: func() (string, error) { t.Fatal("executable called"); return "", nil },
		setenv:     func(string, string) error { t.Fatal("setenv called"); return nil },
		startBackground: func(string, []string, string, string) error {
			t.Fatal("startBackground called")
			return nil
		},
	}
}

// unreadableStdin fails if the non-TTY rejection path consumes input.
type unreadableStdin struct{ t *testing.T }

func (stdin unreadableStdin) Read([]byte) (int, error) {
	stdin.t.Fatal("stdin must not be read")
	return 0, nil
}
