package extagent

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDetectPrefersACPOverCLI(t *testing.T) {
	config := Config{
		WorkspaceRoot: t.TempDir(),
		Agents: map[AgentName]AgentConfig{
			AgentCodex: {
				ACP: helperSpec(t, "acp-good"),
				CLI: helperSpec(t, "cli-codex"),
			},
		},
	}
	results := Detect(context.Background(), config, NewACPClient)
	if got := results[AgentCodex].Preferred; got == nil || got.Kind != TransportACP {
		t.Fatalf("preferred = %#v, want ACP", got)
	}
}

func TestDetectFallsBackToCLIOnACPStartupFailure(t *testing.T) {
	config := Config{
		WorkspaceRoot: t.TempDir(),
		Agents: map[AgentName]AgentConfig{
			AgentClaude: {
				ACP: helperSpec(t, "acp-fail-init"),
				CLI: helperSpec(t, "cli-claude"),
			},
		},
	}
	results := Detect(context.Background(), config, NewACPClient)
	if got := results[AgentClaude].Preferred; got == nil || got.Kind != TransportCLI {
		t.Fatalf("preferred = %#v, want CLI", got)
	}
	if !results[AgentClaude].CLIFallback {
		t.Fatal("want CLI fallback to be marked")
	}
}

func TestCLIAdapterPersistsCodexResumeID(t *testing.T) {
	adapter := CLIAdapter{}
	state := SessionState{Agent: AgentCodex}
	result, err := adapter.SendTurn(context.Background(), helperSpec(t, "cli-codex"), state, t.TempDir(), "hello")
	if err != nil {
		t.Fatalf("SendTurn returned error: %v", err)
	}
	if result.Reply != "codex reply" {
		t.Fatalf("reply = %q, want %q", result.Reply, "codex reply")
	}
	if result.State.ExternalSessionID != "019d2fd4-3674-7ce0-b724-66139be0d160" {
		t.Fatalf("session id = %q, want %q", result.State.ExternalSessionID, "019d2fd4-3674-7ce0-b724-66139be0d160")
	}
}

func TestCLIAdapterIncludesCodexFlagsOnResume(t *testing.T) {
	root := t.TempDir()
	argsLog := filepath.Join(root, "args.log")
	spec := helperSpec(t, "cli-codex")
	spec.Args = append(spec.Args, argsLog)
	adapter := CLIAdapter{}

	_, err := adapter.SendTurn(context.Background(), spec, SessionState{
		Agent:             AgentCodex,
		ExternalSessionID: "resume-session-1",
	}, root, "hello again")
	if err != nil {
		t.Fatalf("SendTurn returned error: %v", err)
	}

	content, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatalf("failed to read args log: %v", err)
	}
	got := string(content)
	if !strings.Contains(got, "exec\nresume\nresume-session-1\n") {
		t.Fatalf("args log = %q, want resume invocation", got)
	}
	if !strings.Contains(got, "--json\n") {
		t.Fatalf("args log = %q, want --json to be preserved", got)
	}
	if !strings.Contains(got, "--skip-git-repo-check\n") {
		t.Fatalf("args log = %q, want --skip-git-repo-check to be preserved", got)
	}
}

func TestCLIAdapterIncludesStderrInErrors(t *testing.T) {
	adapter := CLIAdapter{}
	_, err := adapter.SendTurn(context.Background(), helperSpec(t, "cli-codex-fail"), SessionState{Agent: AgentCodex}, t.TempDir(), "hello")
	if err == nil {
		t.Fatal("SendTurn returned nil error, want CLI failure")
	}
	if !strings.Contains(err.Error(), "trusted directory") {
		t.Fatalf("error = %q, want stderr details", err.Error())
	}
}

func TestBrokerReusesACPClientAcrossTurns(t *testing.T) {
	root := t.TempDir()
	startLog := filepath.Join(root, "starts.log")
	spec := helperSpec(t, "acp-good")
	spec.Args = append(spec.Args, startLog)
	broker := NewBroker(NewStateStore(root), map[AgentName]DetectionResult{
		AgentClaude: {Agent: AgentClaude, Preferred: &AgentTransport{Agent: AgentClaude, Kind: TransportACP, Command: spec}},
	}, NewACPClient)
	defer broker.Close()

	first, err := broker.SendTurn(context.Background(), TurnRequest{BQSessionID: "session-1", Agent: AgentClaude, Prompt: "one", CWD: root})
	if err != nil {
		t.Fatalf("first turn error: %v", err)
	}
	second, err := broker.SendTurn(context.Background(), TurnRequest{BQSessionID: "session-1", Agent: AgentClaude, Prompt: "two", CWD: root})
	if err != nil {
		t.Fatalf("second turn error: %v", err)
	}
	if first.State.ExternalSessionID != second.State.ExternalSessionID {
		t.Fatalf("session ids differ: %q vs %q", first.State.ExternalSessionID, second.State.ExternalSessionID)
	}
	content, err := os.ReadFile(startLog)
	if err != nil {
		t.Fatalf("failed to read start log: %v", err)
	}
	if count := strings.Count(string(content), "start\n"); count != 1 {
		t.Fatalf("acp process start count = %d, want 1", count)
	}
}

func TestBrokerDoesNotFallbackToCLIOnRequestTimeACPFailure(t *testing.T) {
	root := t.TempDir()
	cliLog := filepath.Join(root, "cli.log")
	cliSpec := helperSpec(t, "cli-codex")
	cliSpec.Args = append(cliSpec.Args, cliLog)
	broker := NewBroker(NewStateStore(root), map[AgentName]DetectionResult{
		AgentCodex: {
			Agent:       AgentCodex,
			Preferred:   &AgentTransport{Agent: AgentCodex, Kind: TransportACP, Command: helperSpec(t, "acp-fail-request")},
			ACP:         &AgentTransport{Agent: AgentCodex, Kind: TransportACP, Command: helperSpec(t, "acp-fail-request")},
			CLI:         &AgentTransport{Agent: AgentCodex, Kind: TransportCLI, Command: cliSpec},
			CLIFallback: false,
		},
	}, NewACPClient)
	defer broker.Close()

	_, err := broker.SendTurn(context.Background(), TurnRequest{BQSessionID: "session-1", Agent: AgentCodex, Prompt: "boom", CWD: root})
	if err == nil {
		t.Fatal("SendTurn returned nil error, want ACP request failure")
	}
	if _, statErr := os.Stat(cliLog); !os.IsNotExist(statErr) {
		t.Fatalf("cli fallback should not run, stat err = %v", statErr)
	}
}

func TestParseRoute(t *testing.T) {
	agent, prompt, explicit, err := ParseRoute("/claude hello world")
	if err != nil {
		t.Fatalf("ParseRoute returned error: %v", err)
	}
	if !explicit || agent != AgentClaude || prompt != "hello world" {
		t.Fatalf("route = (%v, %q, %v), want (claude, hello world, true)", agent, prompt, explicit)
	}
}

func TestParseRouteSupportsDefaultReset(t *testing.T) {
	agent, prompt, explicit, err := ParseRoute("/default")
	if err != nil {
		t.Fatalf("ParseRoute returned error: %v", err)
	}
	if !explicit || agent != AgentDefault || prompt != "" {
		t.Fatalf("route = (%v, %q, %v), want (default, \"\", true)", agent, prompt, explicit)
	}
}

func TestParseRouteRejectsDefaultWithMessage(t *testing.T) {
	_, _, explicit, err := ParseRoute("/default hello")
	if !explicit {
		t.Fatal("want explicit route")
	}
	if err == nil {
		t.Fatal("ParseRoute returned nil error, want validation failure")
	}
}

func TestParseRouteRejectsEmptyAgentMessage(t *testing.T) {
	_, _, explicit, err := ParseRoute("/codex")
	if !explicit {
		t.Fatal("want explicit route")
	}
	if err == nil {
		t.Fatal("ParseRoute returned nil error, want validation failure")
	}
}

func TestBrokerClearRemovesSessionBinding(t *testing.T) {
	root := t.TempDir()
	store := NewStateStore(root)
	if err := store.Save(SessionState{
		BQSessionID:       "session-1",
		Agent:             AgentClaude,
		ExternalSessionID: "claude-session-1",
	}); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	broker := NewBroker(store, nil, nil)

	if err := broker.Clear("session-1"); err != nil {
		t.Fatalf("Clear returned error: %v", err)
	}

	agent, prompt, explicit, err := broker.Resolve("hello", "session-1")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if explicit || agent != "" || prompt != "hello" {
		t.Fatalf("resolve = (%q, %q, %v), want (\"\", \"hello\", false)", agent, prompt, explicit)
	}
}

func helperSpec(t *testing.T, mode string) CommandSpec {
	t.Helper()
	return CommandSpec{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestExternalHelperProcess", "--", mode},
	}
}

func TestExternalHelperProcess(t *testing.T) {
	if len(os.Args) < 4 || os.Args[2] != "--" {
		return
	}
	mode := os.Args[3]
	switch mode {
	case "cli-codex":
		runHelperCLICodex(os.Args[4:])
	case "cli-codex-fail":
		runHelperCLICodexFail()
	case "cli-claude":
		runHelperCLIClaude()
	case "acp-good":
		runHelperACP(true, false, os.Args[4:])
	case "acp-fail-init":
		runHelperACP(false, false, nil)
	case "acp-fail-request":
		runHelperACP(true, true, nil)
	}
	os.Exit(0)
}

func runHelperCLIClaude() {
	_, _ = os.Stdout.WriteString(`{"result":"claude reply","session_id":"claude-session-1"}`)
}

func runHelperCLICodex(extra []string) {
	if len(extra) > 0 {
		_ = os.WriteFile(extra[0], []byte(strings.Join(os.Args[4:], "\n")+"\n"), 0o644)
	}
	args := os.Args[4:]
	outputPath := ""
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--output-last-message" {
			outputPath = args[i+1]
		}
	}
	if outputPath != "" {
		_ = os.WriteFile(outputPath, []byte("codex reply"), 0o644)
	}
	_, _ = os.Stdout.WriteString("{\"type\":\"thread.started\",\"thread_id\":\"019d2fd4-3674-7ce0-b724-66139be0d160\"}\n")
	_, _ = os.Stdout.WriteString("{\"type\":\"item.completed\",\"item\":{\"id\":\"item_0\",\"type\":\"agent_message\",\"text\":\"codex reply\"}}\n")
}

func runHelperCLICodexFail() {
	_, _ = os.Stderr.WriteString("Not inside a trusted directory and --skip-git-repo-check was not specified.\n")
	os.Exit(1)
}

func runHelperACP(initOK bool, failPrompt bool, extra []string) {
	if len(extra) > 0 {
		file, err := os.OpenFile(extra[0], os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err == nil {
			_, _ = file.WriteString("start\n")
			_ = file.Close()
		}
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		var request map[string]any
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			continue
		}
		id := int64(request["id"].(float64))
		method, _ := request["method"].(string)
		switch method {
		case "initialize":
			if !initOK {
				writeHelperEnvelope(map[string]any{"id": id, "error": map[string]any{"message": "init failed"}})
				continue
			}
			writeHelperEnvelope(map[string]any{"id": id, "result": map[string]any{"agentCapabilities": map[string]any{"loadSession": true}}})
		case "session/new":
			writeHelperEnvelope(map[string]any{"id": id, "result": map[string]any{"sessionId": "acp-session-1"}})
		case "session/load":
			writeHelperEnvelope(map[string]any{"id": id, "result": map[string]any{"sessionId": "acp-session-1"}})
		case "session/prompt":
			if failPrompt {
				writeHelperEnvelope(map[string]any{"id": id, "error": map[string]any{"message": "prompt failed"}})
				continue
			}
			params := request["params"].(map[string]any)
			sessionID := params["sessionId"].(string)
			prompt := params["prompt"].([]any)[0].(map[string]any)["text"].(string)
			writeHelperEnvelope(map[string]any{
				"method": "session/update",
				"params": map[string]any{
					"sessionId": sessionID,
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]any{"text": "reply:" + prompt},
					},
				},
			})
			writeHelperEnvelope(map[string]any{"id": id, "result": map[string]any{"stopReason": "end_turn"}})
		}
	}
}

func writeHelperEnvelope(payload map[string]any) {
	data, _ := json.Marshal(payload)
	_, _ = os.Stdout.Write(append(data, '\n'))
	time.Sleep(5 * time.Millisecond)
}
