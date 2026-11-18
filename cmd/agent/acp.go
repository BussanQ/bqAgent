package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"bqagent/internal/acp"
	"bqagent/internal/extagent"
	appruntime "bqagent/internal/runtime"
	appserver "bqagent/internal/server"
	"bqagent/internal/session"
	"bqagent/internal/turn"
	"bqagent/internal/workspace"
)

func runACP(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	backend := &acpBackend{getenv: getenv, diagnostics: stderr}
	if err := acp.Serve(ctx, stdin, stdout, backend); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

type acpServiceFactory func(context.Context, func(string) string, *workspace.Workspace, string, io.Writer) (*appserver.Service, func())

type acpBackend struct {
	getenv      func(string) string
	diagnostics io.Writer
	newService  acpServiceFactory
}

func (backend *acpBackend) Open(ctx context.Context, cwd, id string) (acp.Conversation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(cwd) {
		return nil, fmt.Errorf("ACP cwd must be an absolute directory")
	}
	info, err := os.Stat(cwd)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("ACP cwd must be a directory")
	}
	ws, err := workspace.Discover(cwd)
	if err != nil {
		return nil, err
	}
	ws.GlobalAgentDir = globalAgentDirFromEnv(backend.getenv)
	getenv := appruntime.MergeEnv(backend.getenv, appruntime.LoadDotEnv(ws.Root))
	options := session.DefaultOptions()
	options.AgentDir = ws.AgentDir()
	store := session.NewStore(ws.Root, options)
	var saved *session.Session
	if id != "" {
		saved, err = store.Open(id)
		if err != nil {
			return nil, err
		}
		if kind := saved.Meta().ConversationType; kind != "" && kind != string(appserver.ConversationTypeDefault) {
			return nil, fmt.Errorf("ACP only supports single-agent conversations")
		}
	}
	if err := ws.EnsureDefaults(); err != nil {
		return nil, err
	}
	prompt, err := ws.BuildSystemPrompt("")
	if err != nil {
		return nil, err
	}
	if saved == nil {
		saved, err = store.Create(session.CreateOptions{CreatedVia: "acp", Chat: true})
		if err != nil {
			return nil, err
		}
	}
	factory := backend.newService
	if factory == nil {
		factory = func(ctx context.Context, getenv func(string) string, ws *workspace.Workspace, prompt string, output io.Writer) (*appserver.Service, func()) {
			service, broker := newConversationService(ctx, getenv, ws, prompt, output)
			return service, func() { _ = broker.Close() }
		}
	}
	lifetime, cancel := context.WithCancel(ctx)
	service, closeService := factory(lifetime, getenv, ws, prompt, backend.diagnostics)
	return &acpConversation{service: service, store: store, id: saved.ID(), close: func() {
		cancel()
		if closeService != nil {
			closeService()
		}
	}}, nil
}

type acpConversation struct {
	service *appserver.Service
	store   *session.Store
	id      string
	close   func()
	once    sync.Once
}

func (conversation *acpConversation) ID() string { return conversation.id }

func (conversation *acpConversation) Close() {
	conversation.once.Do(func() {
		if conversation.close != nil {
			conversation.close()
		}
	})
}

func (conversation *acpConversation) Models() acp.ModelState {
	current := conversation.service.RuntimeLLMInfoForSession(conversation.id).Model
	state := acp.ModelState{CurrentModelID: current, AvailableModels: []acp.Model{}}
	seen := map[string]bool{}
	for _, option := range conversation.service.ModelOptions() {
		if option.ID == "" || seen[option.ID] {
			continue
		}
		seen[option.ID] = true
		name := option.DisplayName
		if name == "" {
			name = option.ID
		}
		state.AvailableModels = append(state.AvailableModels, acp.Model{ModelID: option.ID, Name: name})
	}
	if current != "" && !seen[current] {
		state.AvailableModels = append(state.AvailableModels, acp.Model{ModelID: current, Name: current})
	}
	return state
}

func (conversation *acpConversation) SetModel(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, model := range conversation.Models().AvailableModels {
		if model.ModelID == id {
			saved, err := conversation.store.Open(conversation.id)
			if err != nil {
				return err
			}
			return saved.SetCurrentModel(id)
		}
	}
	return fmt.Errorf("unknown model %q", id)
}

func (conversation *acpConversation) Replay(ctx context.Context, emit acp.Emit) error {
	history, err := conversation.service.ConversationHistory(conversation.id, 0)
	if err != nil {
		return err
	}
	for index, message := range history.Messages {
		if err := ctx.Err(); err != nil {
			return err
		}
		kind := "agent_message_chunk"
		if message.Role == "user" {
			kind = "user_message_chunk"
		} else if message.Role != "assistant" {
			continue
		}
		if err := emitACPText(emit, kind, fmt.Sprintf("history/%d", index), message.Content); err != nil {
			return err
		}
		for toolIndex, tool := range message.Tools {
			id := fmt.Sprintf("history/%d/tool/%d", index, toolIndex)
			update := acpToolUpdate(id, tool.Name, tool.Status, tool.Arguments, tool.Result, tool.ToolExecution)
			update["sessionUpdate"] = "tool_call"
			if err := emit(update); err != nil {
				return err
			}
		}
	}
	return nil
}

func (conversation *acpConversation) Prompt(ctx context.Context, blocks []acp.ContentBlock, emit acp.Emit) (string, error) {
	var texts []string
	for _, block := range blocks {
		if block.Type != "text" {
			return "", fmt.Errorf("unsupported ACP content type %q", block.Type)
		}
		texts = append(texts, block.Text)
	}
	message := strings.Join(texts, "\n")
	if strings.TrimSpace(message) == "" {
		return "", fmt.Errorf("ACP prompt must contain text")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream := &acpTurnStream{emit: emit, cancel: cancel, snapshots: map[string]acpTextSnapshot{}, tools: map[string]bool{}}
	response, err := conversation.service.HandleTurnWithOptions(ctx, appserver.TurnRequest{
		SessionID: conversation.id,
		Message:   message,
		Agent:     extagent.AgentName("bqagent"),
	}, appserver.TurnOptions{
		MessageSink:    stream.message,
		ToolEventSink:  stream,
		OutputWriter:   io.Discard,
		ProgressWriter: io.Discard,
		TokenSink:      io.Discard,
		Stream:         true,
	})
	if streamErr := stream.failure(); streamErr != nil {
		return "", streamErr
	}
	if errors.Is(err, context.Canceled) || ctx.Err() != nil || response.Outcome.Status == turn.Interrupted {
		return "cancelled", nil
	}
	if err != nil {
		return "", err
	}
	if response.SessionID != "" && response.SessionID != conversation.id {
		return "", fmt.Errorf("ACP session cannot be replaced by this command")
	}
	if !stream.contains(response.Reply) {
		if err := emitACPText(emit, "agent_message_chunk", "reply/"+response.TurnID, response.Reply); err != nil {
			return "", err
		}
	}
	if response.Outcome.Notice != "" {
		if err := emitACPText(emit, "agent_message_chunk", "outcome/"+response.TurnID, response.Outcome.Notice); err != nil {
			return "", err
		}
	}
	switch response.Outcome.Status {
	case turn.BudgetLimited:
		return "max_turn_requests", nil
	case turn.Failed:
		return "", fmt.Errorf("turn failed: %s", response.Outcome.Reason)
	default:
		return "end_turn", nil
	}
}
