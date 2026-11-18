package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bqagent/internal/acp"
	"bqagent/internal/agent"
	appserver "bqagent/internal/server"
	"bqagent/internal/session"
	"bqagent/internal/tools"
	"bqagent/internal/workspace"
)

func TestACPCLIHandshakeWithoutWorkspaceOrTTY(t *testing.T) {
	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{}}}` + "\n")
	var stdout, stderr bytes.Buffer
	code := runWithDeps(context.Background(), input, &stdout, &stderr, []string{"--acp"}, func(string) string {
		t.Fatal("initialize must not access configuration")
		return ""
	}, noSideEffectDeps(t))
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	var response struct {
		ID     int `json:"id"`
		Result struct {
			ProtocolVersion   int `json:"protocolVersion"`
			AgentCapabilities struct {
				LoadSession bool `json:"loadSession"`
			} `json:"agentCapabilities"`
		} `json:"result"`
	}
	decoder := json.NewDecoder(&stdout)
	if err := decoder.Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.ID != 1 || response.Result.ProtocolVersion != 1 || !response.Result.AgentCapabilities.LoadSession {
		t.Fatalf("handshake=%+v", response)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		t.Fatalf("unexpected stdout after handshake: %v", err)
	}
}

func TestACPCLIRejectsConflictingModes(t *testing.T) {
	for _, extra := range [][]string{{"--server"}, {"--resume", "saved"}, {"--background"}, {"--doctor"}, {"--listen", "127.0.0.1:1"}, {"--session-run"}, {"task"}} {
		args := append([]string{"--acp"}, extra...)
		var stdout, stderr bytes.Buffer
		code := runWithDeps(context.Background(), unreadableStdin{t}, &stdout, &stderr, args, func(string) string {
			t.Fatal("invalid ACP invocation must not read environment")
			return ""
		}, noSideEffectDeps(t))
		if code == 0 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "--acp cannot be combined") {
			t.Fatalf("args=%v exit=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
}

func newACPTestBackend(t *testing.T, client agent.ChatCompletionClient) (*acpBackend, string) {
	t.Helper()
	root, home := t.TempDir(), t.TempDir()
	backend := &acpBackend{
		getenv: func(key string) string {
			if key == "HOME" {
				return home
			}
			return ""
		},
		diagnostics: io.Discard,
		newService: func(_ context.Context, _ func(string) string, ws *workspace.Workspace, _ string, _ io.Writer) (*appserver.Service, func()) {
			return appserver.NewService(appserver.ServiceOptions{
				WorkspaceRoot: ws.Root, AgentDir: ws.AgentDir(), Client: client,
				Model: "model-a", Models: []string{"other=model-b"}, SystemPrompt: "test",
			}), nil
		},
	}
	return backend, root
}

func acpCollectedText(updates []acp.Update, kind string) string {
	var result strings.Builder
	for _, update := range updates {
		if update["sessionUpdate"] == kind {
			result.WriteString(update["content"].(acp.ContentBlock).Text)
		}
	}
	return result.String()
}

func TestACPBackendPersistsAndResumesConversation(t *testing.T) {
	client := &tuiMultiTurnClient{}
	backend, root := newACPTestBackend(t, client)
	conversation, err := backend.Open(context.Background(), root, "")
	if err != nil {
		t.Fatal(err)
	}
	defer conversation.Close()
	var updates []acp.Update
	emit := func(update acp.Update) error { updates = append(updates, update); return nil }
	stop, err := conversation.Prompt(context.Background(), []acp.ContentBlock{{Type: "text", Text: "hello"}}, emit)
	if err != nil || stop != "end_turn" {
		t.Fatalf("stop=%q err=%v", stop, err)
	}
	if text := acpCollectedText(updates, "agent_message_chunk"); text != "reply-1" {
		t.Fatalf("stream duplicated or lost text: %q", text)
	}
	saved, err := conversation.(*acpConversation).store.Open(conversation.ID())
	if err != nil || saved.Meta().CreatedVia != "acp" {
		t.Fatalf("saved=%v err=%v", saved, err)
	}
	conversation.Close()
	loaded, err := backend.Open(context.Background(), root, conversation.ID())
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close()
	updates = nil
	if err := loaded.Replay(context.Background(), emit); err != nil {
		t.Fatal(err)
	}
	if acpCollectedText(updates, "user_message_chunk") != "hello" || acpCollectedText(updates, "agent_message_chunk") != "reply-1" {
		t.Fatalf("replay=%+v", updates)
	}
	_, err = loaded.Prompt(context.Background(), []acp.ContentBlock{{Type: "text", Text: "world"}}, emit)
	if err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 2 {
		t.Fatalf("model requests=%d", len(client.requests))
	}
	for _, want := range []string{"hello", "reply-1", "world"} {
		if !strings.Contains(stringifyMessages(client.requests[1]), want) {
			t.Fatalf("continued context missing %q", want)
		}
	}
}

func stringifyMessages(messages []map[string]any) string {
	data, _ := json.Marshal(messages)
	return string(data)
}

func TestACPBackendModelSelectionSurvivesReload(t *testing.T) {
	backend, root := newACPTestBackend(t, &tuiMultiTurnClient{})
	conversation, err := backend.Open(context.Background(), root, "")
	if err != nil {
		t.Fatal(err)
	}
	defer conversation.Close()
	if err := conversation.SetModel(context.Background(), "model-b"); err != nil {
		t.Fatal(err)
	}
	if err := conversation.SetModel(context.Background(), "not-configured"); err == nil {
		t.Fatal("accepted unknown model")
	}
	loaded, err := backend.Open(context.Background(), root, conversation.ID())
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close()
	if loaded.Models().CurrentModelID != "model-b" {
		t.Fatalf("model=%+v", loaded.Models())
	}
	// The provider default is selectable even when it is not repeated in Models.
	if err := loaded.SetModel(context.Background(), "model-a"); err != nil {
		t.Fatal(err)
	}
	if loaded.Models().CurrentModelID != "model-a" {
		t.Fatal("default model not restored")
	}
}

func TestACPBackendValidatesWorkspaceAndSession(t *testing.T) {
	backend, root := newACPTestBackend(t, &tuiMultiTurnClient{})
	if _, err := backend.Open(context.Background(), "relative", ""); err == nil {
		t.Fatal("accepted relative cwd")
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Open(context.Background(), file, ""); err == nil {
		t.Fatal("accepted file cwd")
	}
	conversation, err := backend.Open(context.Background(), root, "")
	if err != nil {
		t.Fatal(err)
	}
	defer conversation.Close()
	if _, err := backend.Open(context.Background(), t.TempDir(), conversation.ID()); err == nil {
		t.Fatal("loaded another workspace's session")
	}
	if _, err := backend.Open(context.Background(), root, "../../escape"); err == nil {
		t.Fatal("accepted invalid session id")
	}
	if _, err := backend.Open(context.Background(), root, "00000000-0000-4000-8000-000000000000"); err == nil {
		t.Fatal("silently recreated missing session")
	}
}

type acpCancellationClient struct {
	sessionNoticeClient
	started chan struct{}
	calls   int
}

func (client *acpCancellationClient) CreateChatCompletionStreamWithOptions(ctx context.Context, _ string, _ []map[string]any, _ []tools.Definition, _ agent.ChatCompletionOptions, onChunk func(string)) (agent.AssistantMessage, error) {
	client.calls++
	if client.calls == 1 {
		close(client.started)
		<-ctx.Done()
		return agent.AssistantMessage{}, ctx.Err()
	}
	onChunk("continued")
	return agent.AssistantMessage{Role: "assistant", Content: "continued"}, nil
}

func TestACPBackendCancelsModelAndContinuesSession(t *testing.T) {
	client := &acpCancellationClient{started: make(chan struct{})}
	backend, root := newACPTestBackend(t, client)
	conversation, err := backend.Open(context.Background(), root, "")
	if err != nil {
		t.Fatal(err)
	}
	defer conversation.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); <-client.started; cancel() }()
	stop, err := conversation.Prompt(ctx, []acp.ContentBlock{{Type: "text", Text: "first"}}, func(acp.Update) error { return nil })
	<-done
	if err != nil || stop != "cancelled" {
		t.Fatalf("stop=%q err=%v", stop, err)
	}
	stop, err = conversation.Prompt(context.Background(), []acp.ContentBlock{{Type: "text", Text: "continue"}}, func(acp.Update) error { return nil })
	if err != nil || stop != "end_turn" {
		t.Fatalf("continuation stop=%q err=%v", stop, err)
	}
	saved, err := conversation.(*acpConversation).store.Open(conversation.ID())
	if err != nil {
		t.Fatal(err)
	}
	if saved.Meta().Status != session.StatusCompleted {
		t.Fatalf("status=%s", saved.Meta().Status)
	}
}
