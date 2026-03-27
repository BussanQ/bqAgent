package server

import (
	"context"
	"fmt"
	"strings"

	"bqagent/internal/agent"
	"bqagent/internal/extagent"
	appruntime "bqagent/internal/runtime"
	"bqagent/internal/session"
	"bqagent/internal/tools"
)

type ServiceOptions struct {
	WorkspaceRoot   string
	Client          agent.ChatCompletionClient
	Model           string
	SystemPrompt    string
	Planner         *agent.Planner
	ToolDefinitions []tools.Definition
	Functions       map[string]tools.Function
	DefaultMaxTurns int
	ExternalBroker  *extagent.Broker
}

type Service struct {
	store           *session.Store
	workspaceRoot   string
	client          agent.ChatCompletionClient
	model           string
	systemPrompt    string
	planner         *agent.Planner
	toolDefinitions []tools.Definition
	functions       map[string]tools.Function
	maxTurns        int
	locker          *KeyedLocker
	externalBroker  *extagent.Broker
}

type TurnRequest struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
}

type TurnResponse struct {
	SessionID string `json:"session_id"`
	Reply     string `json:"reply"`
}

func NewService(options ServiceOptions) *Service {
	maxTurns := options.DefaultMaxTurns
	if maxTurns <= 0 {
		maxTurns = agent.DefaultMaxIterations
	}
	return &Service{
		store:           session.NewStore(options.WorkspaceRoot),
		workspaceRoot:   options.WorkspaceRoot,
		client:          options.Client,
		model:           options.Model,
		systemPrompt:    options.SystemPrompt,
		planner:         options.Planner,
		toolDefinitions: append([]tools.Definition{}, options.ToolDefinitions...),
		functions:       cloneFunctions(options.Functions),
		maxTurns:        maxTurns,
		locker:          NewKeyedLocker(),
		externalBroker:  options.ExternalBroker,
	}
}

func (service *Service) HandleTurn(ctx context.Context, request TurnRequest) (TurnResponse, error) {
	message := strings.TrimSpace(request.Message)
	if message == "" {
		return TurnResponse{}, fmt.Errorf("message is required")
	}

	sessionID := strings.TrimSpace(request.SessionID)
	createOptions := &session.CreateOptions{Task: message, Planned: service.planner != nil, Chat: true}
	if sessionID != "" {
		unlock := service.locker.Lock(sessionID)
		defer unlock()
	}
	conversation, err := appruntime.PrepareConversation(service.store, sessionID, createOptions, service.systemPrompt)
	if err != nil {
		return TurnResponse{}, err
	}

	logFile, err := conversation.Session.OpenOutputFile()
	if err != nil {
		_ = conversation.MarkFailed(err)
		return TurnResponse{}, err
	}
	defer logFile.Close()

	if err := conversation.AddUserMessage(message); err != nil {
		_ = conversation.MarkFailed(err)
		return TurnResponse{}, err
	}

	if service.externalBroker != nil {
		routedAgent, routedPrompt, _, routeErr := service.externalBroker.Resolve(message, conversation.Session.ID())
		if routeErr != nil {
			_ = conversation.MarkFailed(routeErr)
			return TurnResponse{}, routeErr
		}
		if routedAgent != "" {
			result, err := service.externalBroker.SendTurn(ctx, extagent.TurnRequest{
				BQSessionID: conversation.Session.ID(),
				Agent:       routedAgent,
				Prompt:      routedPrompt,
				CWD:         service.workspaceRoot,
			})
			if err != nil {
				_ = conversation.MarkFailed(err)
				return TurnResponse{}, err
			}
			assistantMessage := map[string]any{"role": "assistant", "content": result.Reply}
			if err := conversation.Session.RecordMessage(assistantMessage); err != nil {
				_ = conversation.MarkFailed(err)
				return TurnResponse{}, err
			}
			conversation.Messages = append(conversation.Messages, assistantMessage)
			if err := conversation.MarkCompleted(); err != nil {
				return TurnResponse{}, err
			}
			return TurnResponse{SessionID: conversation.Session.ID(), Reply: result.Reply}, nil
		}
	}

	app := agent.NewWithOptions(service.client, service.model, agent.Options{
		SystemPrompt:    service.systemPrompt,
		LogWriter:       logFile,
		ToolDefinitions: service.toolDefinitions,
		Functions:       service.functions,
		Planner:         service.planner,
		Recorder:        conversation.Recorder(),
	})

	result, _, err := app.RunConversationTurn(ctx, conversation.Messages, service.maxTurns)
	if err != nil {
		_ = conversation.MarkFailed(err)
		return TurnResponse{}, err
	}
	if err := conversation.MarkCompleted(); err != nil {
		return TurnResponse{}, err
	}

	return TurnResponse{SessionID: conversation.Session.ID(), Reply: result}, nil
}

func cloneFunctions(functions map[string]tools.Function) map[string]tools.Function {
	cloned := make(map[string]tools.Function, len(functions))
	for name, function := range functions {
		cloned[name] = function
	}
	return cloned
}
