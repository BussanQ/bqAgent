package server

import (
	"context"
	"fmt"
	"strings"

	"bqagent/internal/agent"
	"bqagent/internal/session"
	"bqagent/internal/tools"
)

type ServiceOptions struct {
	WorkspaceRoot    string
	Client           agent.ChatCompletionClient
	Model            string
	SystemPrompt     string
	Planner          *agent.Planner
	ToolDefinitions  []tools.Definition
	Functions        map[string]tools.Function
	DefaultMaxTurns  int
}

type Service struct {
	store           *session.Store
	client          agent.ChatCompletionClient
	model           string
	systemPrompt    string
	planner         *agent.Planner
	toolDefinitions []tools.Definition
	functions       map[string]tools.Function
	maxTurns        int
	locker          *KeyedLocker
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
		client:          options.Client,
		model:           options.Model,
		systemPrompt:    options.SystemPrompt,
		planner:         options.Planner,
		toolDefinitions: append([]tools.Definition{}, options.ToolDefinitions...),
		functions:       cloneFunctions(options.Functions),
		maxTurns:        maxTurns,
		locker:          NewKeyedLocker(),
	}
}

func (service *Service) HandleTurn(ctx context.Context, request TurnRequest) (TurnResponse, error) {
	message := strings.TrimSpace(request.Message)
	if message == "" {
		return TurnResponse{}, fmt.Errorf("message is required")
	}

	sessionID := strings.TrimSpace(request.SessionID)
	var (
		savedSession *session.Session
		err          error
	)
	if sessionID != "" {
		unlock := service.locker.Lock(sessionID)
		defer unlock()

		savedSession, err = service.store.Open(sessionID)
	} else {
		savedSession, err = service.store.Create(session.CreateOptions{Task: message, Planned: service.planner != nil, Chat: true})
	}
	if err != nil {
		return TurnResponse{}, err
	}

	if err := savedSession.MarkRunning(); err != nil {
		return TurnResponse{}, err
	}

	messages, err := savedSession.LoadMessages()
	if err != nil {
		_ = savedSession.MarkFailed(err)
		return TurnResponse{}, err
	}

	logFile, err := savedSession.OpenOutputFile()
	if err != nil {
		_ = savedSession.MarkFailed(err)
		return TurnResponse{}, err
	}
	defer logFile.Close()

	if len(messages) == 0 {
		systemMessage := map[string]any{"role": "system", "content": service.systemPrompt}
		messages = append(messages, systemMessage)
		if err := savedSession.RecordMessage(systemMessage); err != nil {
			_ = savedSession.MarkFailed(err)
			return TurnResponse{}, err
		}
	}

	userMessage := map[string]any{"role": "user", "content": message}
	messages = append(messages, userMessage)
	if err := savedSession.RecordMessage(userMessage); err != nil {
		_ = savedSession.MarkFailed(err)
		return TurnResponse{}, err
	}

	app := agent.NewWithOptions(service.client, service.model, agent.Options{
		SystemPrompt:    service.systemPrompt,
		LogWriter:       logFile,
		ToolDefinitions: service.toolDefinitions,
		Functions:       service.functions,
		Planner:         service.planner,
		Recorder:        savedSession,
	})

	result, _, err := app.RunConversationTurn(ctx, messages, service.maxTurns)
	if err != nil {
		_ = savedSession.MarkFailed(err)
		return TurnResponse{}, err
	}
	if err := savedSession.MarkCompleted(); err != nil {
		return TurnResponse{}, err
	}

	return TurnResponse{SessionID: savedSession.ID(), Reply: result}, nil
}

func cloneFunctions(functions map[string]tools.Function) map[string]tools.Function {
	cloned := make(map[string]tools.Function, len(functions))
	for name, function := range functions {
		cloned[name] = function
	}
	return cloned
}
