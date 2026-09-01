package extagent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConfigFromEnvDefaultsOpenCodeACP(t *testing.T) {
	config := ConfigFromEnv(func(string) string { return "" }, t.TempDir())
	openCode := config.Agents[AgentOpenCode]
	if openCode.ACP.Command != "opencode" {
		t.Fatalf("ACP command = %q, want %q", openCode.ACP.Command, "opencode")
	}
	if len(openCode.ACP.Args) != 1 || openCode.ACP.Args[0] != "acp" {
		t.Fatalf("ACP args = %#v, want [acp]", openCode.ACP.Args)
	}
	if openCode.CLI.Command != "" || len(openCode.CLI.Args) != 0 {
		t.Fatalf("CLI config = %#v, want empty", openCode.CLI)
	}
}

func TestConfigFromEnvDefaultsCursorACP(t *testing.T) {
	config := ConfigFromEnv(func(string) string { return "" }, t.TempDir())
	cursor := config.Agents[AgentCursor]
	if cursor.ACP.Command != "cursor-agent" {
		t.Fatalf("ACP command = %q, want %q", cursor.ACP.Command, "cursor-agent")
	}
	wantArgs := []string{"acp"}
	if len(cursor.ACP.Args) != len(wantArgs) || cursor.ACP.Args[0] != wantArgs[0] {
		t.Fatalf("ACP args = %#v, want %#v", cursor.ACP.Args, wantArgs)
	}
	if cursor.CLI.Command != "" || len(cursor.CLI.Args) != 0 {
		t.Fatalf("CLI config = %#v, want empty", cursor.CLI)
	}
}

func TestConfigFromEnvOverridesOpenCodeACP(t *testing.T) {
	env := map[string]string{
		"AGENT_OPENCODE_ACP_CMD":  "custom-opencode",
		"AGENT_OPENCODE_ACP_ARGS": "serve acp --verbose",
	}
	config := ConfigFromEnv(func(key string) string { return env[key] }, t.TempDir())
	openCode := config.Agents[AgentOpenCode]
	if openCode.ACP.Command != "custom-opencode" {
		t.Fatalf("ACP command = %q, want %q", openCode.ACP.Command, "custom-opencode")
	}
	wantArgs := []string{"serve", "acp", "--verbose"}
	if len(openCode.ACP.Args) != len(wantArgs) {
		t.Fatalf("ACP args = %#v, want %#v", openCode.ACP.Args, wantArgs)
	}
	for i := range wantArgs {
		if openCode.ACP.Args[i] != wantArgs[i] {
			t.Fatalf("ACP args = %#v, want %#v", openCode.ACP.Args, wantArgs)
		}
	}
}

func TestDetectIgnoresUnsupportedCLITransports(t *testing.T) {
	config := Config{
		WorkspaceRoot: t.TempDir(),
		Agents: map[AgentName]AgentConfig{
			AgentCursor:   {CLI: helperSpec(t, "cli-claude")},
			AgentOpenCode: {CLI: helperSpec(t, "cli-claude")},
		},
	}
	results := Detect(context.Background(), config, NewACPClient)
	for _, agent := range []AgentName{AgentCursor, AgentOpenCode} {
		if results[agent].CLI != nil || results[agent].Preferred != nil {
			t.Fatalf("%s detection = %#v, want unavailable", agent, results[agent])
		}
	}
}

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

func TestNewDetectingBrokerReturnsBeforeProbeCompletes(t *testing.T) {
	root := t.TempDir()
	client := &blockingInitializeACPClient{started: make(chan struct{}), release: make(chan struct{})}
	config := Config{
		WorkspaceRoot: root,
		Agents: map[AgentName]AgentConfig{
			AgentCodex: {ACP: CommandSpec{Command: os.Args[0]}},
		},
	}
	constructed := make(chan *Broker, 1)
	go func() {
		constructed <- NewDetectingBroker(context.Background(), NewStateStore(root), config, func(CommandSpec, string) (ACPClient, error) {
			return client, nil
		}, nil)
	}()

	var broker *Broker
	select {
	case broker = <-constructed:
	case <-time.After(time.Second):
		t.Fatal("broker construction waited for external-agent detection")
	}
	defer broker.Close()
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("background detection did not start")
	}

	detected := make(chan DetectionResult, 1)
	go func() { detected <- broker.Detection(AgentCodex) }()
	select {
	case result := <-detected:
		t.Fatalf("detection returned before probe completed: %#v", result)
	case <-time.After(20 * time.Millisecond):
	}
	_ = client.Close()
	select {
	case result := <-detected:
		if result.Preferred == nil || result.Preferred.Kind != TransportACP {
			t.Fatalf("detection = %#v, want ACP", result)
		}
	case <-time.After(time.Second):
		t.Fatal("detection result was not published")
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

func TestACPCollectsOnlyAgentMessageChunks(t *testing.T) {
	collector := &strings.Builder{}
	client := &stdioACPClient{collectors: map[string]*strings.Builder{"session-1": collector}}
	for _, update := range []string{
		`{"sessionId":"session-1","update":{"sessionUpdate":"agent_thought_chunk","content":{"text":"hidden thought"}}}`,
		`{"sessionId":"session-1","update":{"sessionUpdate":"tool_call","content":{"text":"tool details"}}}`,
		`{"sessionId":"session-1","update":{"sessionUpdate":"unknown_extension_update","content":{"text":"unknown"}}}`,
		`{"sessionId":"session-1","update":{"sessionUpdate":"agent_message_chunk","content":{"text":"visible reply"}}}`,
	} {
		client.handleSessionUpdate(json.RawMessage(update))
	}
	if got := collector.String(); got != "visible reply" {
		t.Fatalf("collected reply = %q, want only agent message content", got)
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

func TestBrokerUsesDistinctACPClientsAcrossAgents(t *testing.T) {
	root := t.TempDir()
	claudeStartLog := filepath.Join(root, "claude-starts.log")
	openCodeStartLog := filepath.Join(root, "opencode-starts.log")
	claudeSpec := helperSpec(t, "acp-good")
	claudeSpec.Args = append(claudeSpec.Args, claudeStartLog)
	openCodeSpec := helperSpec(t, "acp-good")
	openCodeSpec.Args = append(openCodeSpec.Args, openCodeStartLog)
	store := NewStateStore(root)
	broker := NewBroker(store, map[AgentName]DetectionResult{
		AgentClaude: {
			Agent:     AgentClaude,
			Preferred: &AgentTransport{Agent: AgentClaude, Kind: TransportACP, Command: claudeSpec},
		},
		AgentOpenCode: {
			Agent:     AgentOpenCode,
			Preferred: &AgentTransport{Agent: AgentOpenCode, Kind: TransportACP, Command: openCodeSpec},
		},
	}, NewACPClient)
	defer broker.Close()

	if _, err := broker.SendTurn(context.Background(), TurnRequest{
		BQSessionID: "session-1",
		Agent:       AgentClaude,
		Prompt:      "one",
		CWD:         root,
	}); err != nil {
		t.Fatalf("Claude turn error: %v", err)
	}
	response, err := broker.SendTurn(context.Background(), TurnRequest{
		BQSessionID: "session-1",
		Agent:       AgentOpenCode,
		Prompt:      "two",
		CWD:         root,
	})
	if err != nil {
		t.Fatalf("OpenCode turn error: %v", err)
	}
	if response.State.Agent != AgentOpenCode || response.State.Transport != TransportACP {
		t.Fatalf("state = %#v, want OpenCode ACP", response.State)
	}
	for name, path := range map[string]string{
		"Claude":   claudeStartLog,
		"OpenCode": openCodeStartLog,
	} {
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("failed to read %s start log: %v", name, readErr)
		}
		if count := strings.Count(string(content), "start\n"); count != 1 {
			t.Fatalf("%s ACP process start count = %d, want 1", name, count)
		}
	}
	stored, err := store.Load("session-1")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if stored.Agent != AgentOpenCode || stored.Transport != TransportACP {
		t.Fatalf("stored state = %#v, want OpenCode ACP", stored)
	}
}

func TestBrokerCoalescesACPInitializationAndLetsWaiterCancel(t *testing.T) {
	root := t.TempDir()
	client := &blockingInitializeACPClient{started: make(chan struct{}), release: make(chan struct{})}
	factoryCalls := 0
	broker := NewBroker(NewStateStore(root), nil, func(CommandSpec, string) (ACPClient, error) {
		factoryCalls++
		return client, nil
	})
	defer broker.Close()

	firstDone := make(chan error, 1)
	go func() {
		_, err := broker.acpClient(context.Background(), "session-1", AgentClaude, CommandSpec{Command: "claude-acp"}, root, 0)
		firstDone <- err
	}()
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ACP initialize")
	}

	waiterCtx, cancel := context.WithCancel(context.Background())
	waiterDone := make(chan error, 1)
	go func() {
		_, err := broker.acpClient(waiterCtx, "session-1", AgentClaude, CommandSpec{Command: "claude-acp"}, root, 0)
		waiterDone <- err
	}()
	cancel()
	select {
	case err := <-waiterDone:
		if err != context.Canceled {
			t.Fatalf("waiter error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter did not honor cancellation")
	}
	_ = client.Close()
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("initializer error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("initializer did not finish")
	}
	if factoryCalls != 1 {
		t.Fatalf("factory calls = %d, want exactly one", factoryCalls)
	}
}

func TestBrokerSerializesConcurrentTurnsForOneSession(t *testing.T) {
	root := t.TempDir()
	client := newTurnBlockingACPClient()
	broker := NewBroker(NewStateStore(root), map[AgentName]DetectionResult{
		AgentClaude: {Agent: AgentClaude, Preferred: &AgentTransport{Agent: AgentClaude, Kind: TransportACP, Command: CommandSpec{Command: "fake"}}},
	}, func(CommandSpec, string) (ACPClient, error) { return client, nil })
	defer broker.Close()

	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() {
		_, err := broker.SendTurn(context.Background(), TurnRequest{BQSessionID: "session-1", Agent: AgentClaude, Prompt: "first", CWD: root})
		first <- err
	}()
	select {
	case <-client.firstPromptStarted:
	case <-time.After(time.Second):
		t.Fatal("first turn did not reach Prompt")
	}
	go func() {
		_, err := broker.SendTurn(context.Background(), TurnRequest{BQSessionID: "session-1", Agent: AgentClaude, Prompt: "second", CWD: root})
		second <- err
	}()
	select {
	case <-client.secondPromptStarted:
		t.Fatal("same-session second turn reached Prompt before the first finished")
	case <-time.After(100 * time.Millisecond):
	}
	close(client.releaseFirst)
	for _, done := range []<-chan error{first, second} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("SendTurn returned error: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("concurrent turn did not finish")
		}
	}
	if max := client.maxInFlight(); max != 1 {
		t.Fatalf("same-session concurrent prompts = %d, want 1", max)
	}
}

func TestBrokerClearWaitsForTurnAndCannotBeUndoneByLateSave(t *testing.T) {
	root := t.TempDir()
	client := newTurnBlockingACPClient()
	store := NewStateStore(root)
	broker := NewBroker(store, map[AgentName]DetectionResult{
		AgentClaude: {Agent: AgentClaude, Preferred: &AgentTransport{Agent: AgentClaude, Kind: TransportACP, Command: CommandSpec{Command: "fake"}}},
	}, func(CommandSpec, string) (ACPClient, error) { return client, nil })
	defer broker.Close()

	turnDone := make(chan error, 1)
	go func() {
		_, err := broker.SendTurn(context.Background(), TurnRequest{BQSessionID: "session-1", Agent: AgentClaude, Prompt: "turn", CWD: root})
		turnDone <- err
	}()
	select {
	case <-client.firstPromptStarted:
	case <-time.After(time.Second):
		t.Fatal("turn did not reach Prompt")
	}
	clearDone := make(chan error, 1)
	go func() { clearDone <- broker.Clear("session-1") }()
	select {
	case err := <-clearDone:
		t.Fatalf("Clear returned before the active turn finished: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(client.releaseFirst)
	if err := <-turnDone; err != nil {
		t.Fatalf("SendTurn returned error: %v", err)
	}
	if err := <-clearDone; err != nil {
		t.Fatalf("Clear returned error: %v", err)
	}
	state, err := store.Load("session-1")
	if err != nil {
		t.Fatal(err)
	}
	if state.Agent != "" || state.ExternalSessionID != "" {
		t.Fatalf("Clear was undone by a late Save: %+v", state)
	}
}

func TestBrokerClearDoesNotReviveLateACPInitialization(t *testing.T) {
	root := t.TempDir()
	client := &blockingInitializeACPClient{started: make(chan struct{}), release: make(chan struct{})}
	broker := NewBroker(NewStateStore(root), nil, func(CommandSpec, string) (ACPClient, error) {
		return client, nil
	})
	defer broker.Close()

	done := make(chan error, 1)
	go func() {
		_, err := broker.acpClient(context.Background(), "session-1", AgentClaude, CommandSpec{Command: "claude-acp"}, root, 0)
		done <- err
	}()
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ACP initialize")
	}
	if err := broker.Clear("session-1"); err != nil {
		t.Fatalf("Clear returned error: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, errBrokerSessionCleared) {
			t.Fatalf("late initializer error = %v, want cleared session", err)
		}
	case <-time.After(time.Second):
		t.Fatal("late initializer did not finish")
	}
	if len(broker.acpClients) != 0 {
		t.Fatalf("cached clients = %d, want no late client revival", len(broker.acpClients))
	}
}

func TestBrokerClearClosesAllSessionACPClients(t *testing.T) {
	root := t.TempDir()
	var clients []*trackingACPClient
	factory := func(CommandSpec, string) (ACPClient, error) {
		client := &trackingACPClient{}
		clients = append(clients, client)
		return client, nil
	}
	broker := NewBroker(NewStateStore(root), map[AgentName]DetectionResult{
		AgentClaude: {
			Agent:     AgentClaude,
			Preferred: &AgentTransport{Agent: AgentClaude, Kind: TransportACP, Command: CommandSpec{Command: "claude-acp"}},
		},
		AgentOpenCode: {
			Agent:     AgentOpenCode,
			Preferred: &AgentTransport{Agent: AgentOpenCode, Kind: TransportACP, Command: CommandSpec{Command: "opencode-acp"}},
		},
	}, factory)

	for _, agent := range []AgentName{AgentClaude, AgentOpenCode} {
		if _, err := broker.SendTurn(context.Background(), TurnRequest{
			BQSessionID: "session-1",
			Agent:       agent,
			Prompt:      "hello",
			CWD:         root,
		}); err != nil {
			t.Fatalf("%s turn error: %v", agent, err)
		}
	}
	if err := broker.Clear("session-1"); err != nil {
		t.Fatalf("Clear returned error: %v", err)
	}
	if len(clients) != 2 {
		t.Fatalf("created clients = %d, want 2", len(clients))
	}
	for i, client := range clients {
		if client.closeCount != 1 {
			t.Fatalf("client %d close count = %d, want 1", i, client.closeCount)
		}
	}
	if len(broker.acpClients) != 0 {
		t.Fatalf("cached clients = %d, want 0", len(broker.acpClients))
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

func TestParseRouteSupportsOpenCode(t *testing.T) {
	agent, prompt, explicit, err := ParseRoute("/opencode explain this repository")
	if err != nil {
		t.Fatalf("ParseRoute returned error: %v", err)
	}
	if !explicit || agent != AgentOpenCode || prompt != "explain this repository" {
		t.Fatalf("route = (%v, %q, %v), want (opencode, explain this repository, true)", agent, prompt, explicit)
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

type turnBlockingACPClient struct {
	mu                  sync.Mutex
	firstPromptStarted  chan struct{}
	secondPromptStarted chan struct{}
	releaseFirst        chan struct{}
	prompts             int
	inFlight            int
	max                 int
}

func newTurnBlockingACPClient() *turnBlockingACPClient {
	return &turnBlockingACPClient{
		firstPromptStarted:  make(chan struct{}),
		secondPromptStarted: make(chan struct{}),
		releaseFirst:        make(chan struct{}),
	}
}

func (client *turnBlockingACPClient) Initialize(context.Context) error { return nil }
func (client *turnBlockingACPClient) LoadSessionSupported() bool       { return true }
func (client *turnBlockingACPClient) NewSession(context.Context, string) (string, error) {
	return "external-session", nil
}
func (client *turnBlockingACPClient) LoadSession(_ context.Context, sessionID, _ string) (string, error) {
	return sessionID, nil
}
func (client *turnBlockingACPClient) Prompt(_ context.Context, _ string, prompt string) (string, error) {
	client.mu.Lock()
	client.prompts++
	ordinal := client.prompts
	client.inFlight++
	if client.inFlight > client.max {
		client.max = client.inFlight
	}
	if ordinal == 1 {
		close(client.firstPromptStarted)
	} else if ordinal == 2 {
		close(client.secondPromptStarted)
	}
	client.mu.Unlock()
	if ordinal == 1 {
		<-client.releaseFirst
	}
	client.mu.Lock()
	client.inFlight--
	client.mu.Unlock()
	return "reply:" + prompt, nil
}
func (client *turnBlockingACPClient) Close() error { return nil }
func (client *turnBlockingACPClient) maxInFlight() int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.max
}

type blockingInitializeACPClient struct {
	started   chan struct{}
	release   chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
}

func (client *blockingInitializeACPClient) Initialize(ctx context.Context) error {
	client.startOnce.Do(func() { close(client.started) })
	select {
	case <-client.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (client *blockingInitializeACPClient) LoadSessionSupported() bool { return false }
func (client *blockingInitializeACPClient) NewSession(context.Context, string) (string, error) {
	return "blocking-session", nil
}
func (client *blockingInitializeACPClient) LoadSession(_ context.Context, sessionID, _ string) (string, error) {
	return sessionID, nil
}
func (client *blockingInitializeACPClient) Prompt(_ context.Context, _, prompt string) (string, error) {
	return "reply:" + prompt, nil
}
func (client *blockingInitializeACPClient) Close() error {
	client.closeOnce.Do(func() { close(client.release) })
	return nil
}

type trackingACPClient struct {
	closeCount int
}

type permissionRecordingSink struct {
	requests chan ACPPermissionRequest
}

func (sink permissionRecordingSink) EmitACPPermissionRequest(request ACPPermissionRequest) {
	sink.requests <- request
}

func TestBrokerForwardsACPRequestPermissionAndReturnsSelection(t *testing.T) {
	root := t.TempDir()
	broker := NewBroker(NewStateStore(root), map[AgentName]DetectionResult{
		AgentCursor: {Agent: AgentCursor, Preferred: &AgentTransport{Agent: AgentCursor, Kind: TransportACP, Command: helperSpec(t, "acp-permission")}},
	}, NewACPClient)
	defer broker.Close()
	sink := permissionRecordingSink{requests: make(chan ACPPermissionRequest, 1)}
	type turnResult struct {
		response TurnResponse
		err      error
	}
	done := make(chan turnResult, 1)
	go func() {
		response, err := broker.SendGroupTurn(context.Background(), TurnRequest{
			BQSessionID: "group-1", Agent: AgentCursor, Prompt: "edit files", CWD: root, PermissionSink: sink,
		})
		done <- turnResult{response: response, err: err}
	}()

	var request ACPPermissionRequest
	select {
	case request = <-sink.requests:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ACP permission request")
	}
	if request.Agent != AgentCursor || request.ExternalSessionID != "acp-session-1" || len(request.Options) != 2 {
		t.Fatalf("permission request = %#v", request)
	}
	if err := broker.RespondPermission(request.RequestID, "allow-once"); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if result.err != nil || result.response.Reply != "permission:allow-once" {
		t.Fatalf("turn response = %#v, error = %v", result.response, result.err)
	}
	if err := broker.RespondPermission(request.RequestID, "allow-once"); !errors.Is(err, ErrPermissionNotFound) {
		t.Fatalf("second response error = %v, want ErrPermissionNotFound", err)
	}

	go func() {
		response, err := broker.SendGroupTurn(context.Background(), TurnRequest{
			BQSessionID: "group-1", Agent: AgentCursor, Prompt: "reject edit", CWD: root, PermissionSink: sink,
		})
		done <- turnResult{response: response, err: err}
	}()
	request = <-sink.requests
	if err := broker.RespondPermission(request.RequestID, "reject-once"); err != nil {
		t.Fatal(err)
	}
	result = <-done
	if result.err != nil || result.response.Reply != "permission:reject-once" {
		t.Fatalf("rejected turn response = %#v, error = %v", result.response, result.err)
	}
}

func (client *trackingACPClient) Initialize(context.Context) error {
	return nil
}

func (client *trackingACPClient) LoadSessionSupported() bool {
	return true
}

func (client *trackingACPClient) NewSession(context.Context, string) (string, error) {
	return "tracking-session", nil
}

func (client *trackingACPClient) LoadSession(_ context.Context, sessionID, _ string) (string, error) {
	return sessionID, nil
}

func (client *trackingACPClient) Prompt(_ context.Context, _, prompt string) (string, error) {
	return "reply:" + prompt, nil
}

func (client *trackingACPClient) Close() error {
	client.closeCount++
	return nil
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
	case "acp-permission":
		runHelperACPPermission()
	}
	os.Exit(0)
}

func runHelperACPPermission() {
	scanner := bufio.NewScanner(os.Stdin)
	var promptID int64
	for scanner.Scan() {
		var envelope map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
			continue
		}
		id, _ := envelope["id"].(float64)
		method, _ := envelope["method"].(string)
		switch method {
		case "initialize":
			writeHelperEnvelope(map[string]any{"id": int64(id), "result": map[string]any{"agentCapabilities": map[string]any{"loadSession": true}}})
		case "session/new":
			writeHelperEnvelope(map[string]any{"id": int64(id), "result": map[string]any{"sessionId": "acp-session-1"}})
		case "session/load":
			writeHelperEnvelope(map[string]any{"id": int64(id), "result": map[string]any{"sessionId": "acp-session-1"}})
		case "session/prompt":
			promptID = int64(id)
			writeHelperEnvelope(map[string]any{
				"id":     900,
				"method": "session/request_permission",
				"params": map[string]any{
					"sessionId": "acp-session-1",
					"toolCall":  map[string]any{"toolCallId": "tool-1", "title": "修改文件", "kind": "edit", "rawInput": map[string]any{"path": "main.go"}},
					"options": []map[string]any{
						{"optionId": "allow-once", "name": "允许一次", "kind": "allow_once"},
						{"optionId": "reject-once", "name": "拒绝", "kind": "reject_once"},
					},
				},
			})
		case "":
			if int64(id) != 900 || promptID == 0 {
				continue
			}
			result, _ := envelope["result"].(map[string]any)
			outcome, _ := result["outcome"].(map[string]any)
			optionID, _ := outcome["optionId"].(string)
			writeHelperEnvelope(map[string]any{"method": "session/update", "params": map[string]any{
				"sessionId": "acp-session-1", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"text": "permission:" + optionID}},
			}})
			writeHelperEnvelope(map[string]any{"id": promptID, "result": map[string]any{"stopReason": "end_turn"}})
			promptID = 0
		}
	}
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
