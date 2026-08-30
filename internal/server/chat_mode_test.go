package server

import (
	"context"
	"strings"
	"testing"

	"bqagent/internal/agent"
	"bqagent/internal/tools"
)

type chatModeRecordingClient struct {
	definitions [][]tools.Definition
	messages    [][]map[string]any
}

func (client *chatModeRecordingClient) CreateChatCompletion(_ context.Context, _ string, messages []map[string]any, definitions []tools.Definition) (agent.AssistantMessage, error) {
	client.definitions = append(client.definitions, append([]tools.Definition(nil), definitions...))
	client.messages = append(client.messages, append([]map[string]any(nil), messages...))
	return agent.AssistantMessage{Role: "assistant", Content: "只读结果"}, nil
}

func (client *chatModeRecordingClient) CreateChatCompletionStream(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, onChunk func(string)) (agent.AssistantMessage, error) {
	if onChunk != nil {
		onChunk("只读结果")
	}
	return client.CreateChatCompletion(ctx, model, messages, definitions)
}

func TestAskAndRunCommandsSelectAndPersistSessionMode(t *testing.T) {
	service := NewService(ServiceOptions{WorkspaceRoot: t.TempDir(), SystemPrompt: "base prompt"})

	ask, err := service.HandleTurn(context.Background(), TurnRequest{Message: "/ask"})
	if err != nil {
		t.Fatalf("enable ask returned error: %v", err)
	}
	if ask.Mode != ChatModeAsk || !strings.Contains(ask.Reply, "只读问答") {
		t.Fatalf("ask response = %#v", ask)
	}
	saved, err := service.store.Open(ask.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Meta().CurrentMode != "ask" {
		t.Fatalf("CurrentMode = %q, want ask", saved.Meta().CurrentMode)
	}
	if mode := service.RuntimeLLMInfoForSession(ask.SessionID).Mode; mode != ChatModeAsk {
		t.Fatalf("runtime mode = %q, want ask", mode)
	}

	stillAsk, err := service.HandleTurn(context.Background(), TurnRequest{SessionID: ask.SessionID, Message: "/ask"})
	if err != nil {
		t.Fatalf("repeat ask returned error: %v", err)
	}
	if stillAsk.Mode != ChatModeAsk {
		t.Fatalf("repeated /ask mode = %q, want ask", stillAsk.Mode)
	}

	runMode, err := service.HandleTurn(context.Background(), TurnRequest{SessionID: ask.SessionID, Message: "/run"})
	if err != nil {
		t.Fatalf("select run returned error: %v", err)
	}
	if runMode.Mode != ChatModeRun || !strings.Contains(runMode.Reply, "Run 模式") {
		t.Fatalf("run response = %#v", runMode)
	}
	saved, err = service.store.Open(ask.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Meta().CurrentMode != "" {
		t.Fatalf("CurrentMode = %q, want empty run default", saved.Meta().CurrentMode)
	}
	if mode := service.RuntimeLLMInfoForSession(ask.SessionID).Mode; mode != ChatModeRun {
		t.Fatalf("runtime mode = %q, want run", mode)
	}
}

func TestAskModeExposesOnlyReadOnlyToolsAndSkipsMemoryWrite(t *testing.T) {
	root := t.TempDir()
	catalog := tools.NewCatalog(tools.Options{WorkspaceRoot: root})
	client := &chatModeRecordingClient{}
	memoryWrites := 0
	service := NewService(ServiceOptions{
		WorkspaceRoot:   root,
		Client:          client,
		Model:           "test-model",
		SystemPrompt:    "base prompt",
		ToolDefinitions: catalog.Definitions(),
		Functions:       catalog.Registry(),
		MemoryAppend: func(_, _ string) error {
			memoryWrites++
			return nil
		},
	})

	response, err := service.HandleTurn(context.Background(), TurnRequest{Message: "分析这个项目", Mode: ChatModeAsk})
	if err != nil {
		t.Fatalf("ask turn returned error: %v", err)
	}
	if response.Mode != ChatModeAsk {
		t.Fatalf("mode = %q, want ask", response.Mode)
	}
	if memoryWrites != 0 {
		t.Fatalf("memory writes = %d, want 0", memoryWrites)
	}
	if len(client.definitions) != 1 {
		t.Fatalf("model calls = %d, want 1", len(client.definitions))
	}
	gotNames := make(map[string]bool)
	for _, definition := range client.definitions[0] {
		gotNames[definition.Function.Name] = true
	}
	for _, name := range []string{"read_file", "grep", "glob", "web_search", "web_fetch"} {
		if !gotNames[name] {
			t.Errorf("read-only tool %q was not exposed: %#v", name, gotNames)
		}
	}
	for _, name := range []string{"execute_bash", "write_file", "edit_file", "todo_write", "install_skill", "mem_save"} {
		if gotNames[name] {
			t.Errorf("mutating tool %q was exposed in ask mode", name)
		}
	}
	_, filteredFunctions := toolsetForChatMode(ChatModeAsk, catalog.Definitions(), catalog.Registry())
	for _, name := range []string{"execute_bash", "write_file", "edit_file"} {
		if _, ok := filteredFunctions[name]; ok {
			t.Errorf("mutating function %q remained executable in ask mode", name)
		}
	}
	if len(client.messages) != 1 || len(client.messages[0]) == 0 || !strings.Contains(messageText(client.messages[0][0]), "The current conversation is in Ask mode") {
		t.Fatalf("ask system prompt = %#v", client.messages)
	}
}

func TestRunModeRemainsDefaultWithFullTools(t *testing.T) {
	root := t.TempDir()
	catalog := tools.NewCatalog(tools.Options{WorkspaceRoot: root})
	client := &chatModeRecordingClient{}
	service := NewService(ServiceOptions{
		WorkspaceRoot: root, Client: client, Model: "test-model", SystemPrompt: "base prompt",
		ToolDefinitions: catalog.Definitions(), Functions: catalog.Registry(),
	})

	response, err := service.HandleTurn(context.Background(), TurnRequest{Message: "修改文件"})
	if err != nil {
		t.Fatalf("run turn returned error: %v", err)
	}
	if response.Mode != ChatModeRun {
		t.Fatalf("mode = %q, want default run", response.Mode)
	}
	gotNames := make(map[string]bool)
	for _, definition := range client.definitions[0] {
		gotNames[definition.Function.Name] = true
	}
	for _, name := range []string{"execute_bash", "write_file", "edit_file"} {
		if !gotNames[name] {
			t.Errorf("full run tool %q was not exposed", name)
		}
	}
}

func TestAskModeBlocksExternalAgentCommandsBeforeExecution(t *testing.T) {
	client := &chatModeRecordingClient{}
	service := NewService(ServiceOptions{WorkspaceRoot: t.TempDir(), Client: client, SystemPrompt: "base prompt"})

	response, err := service.HandleTurn(context.Background(), TurnRequest{Message: "/agent spawn codex -- 修改文件", Mode: ChatModeAsk})
	if err != nil {
		t.Fatalf("blocked command returned error: %v", err)
	}
	if !strings.Contains(response.Reply, "不能修改文件、执行命令或启动外部 Agent") {
		t.Fatalf("blocked command reply = %q", response.Reply)
	}
	if len(client.messages) != 0 {
		t.Fatalf("blocked command called model %d times, want 0", len(client.messages))
	}
}
