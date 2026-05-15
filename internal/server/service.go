package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"bqagent/internal/agent"
	"bqagent/internal/extagent"
	appruntime "bqagent/internal/runtime"
	"bqagent/internal/session"
	"bqagent/internal/tools"
)

type ServiceOptions struct {
	WorkspaceRoot       string
	Client              agent.ChatCompletionClient
	Model               string
	SystemPrompt        string
	SystemPromptBuilder func() (string, error)
	Planner             *agent.Planner
	ToolDefinitions     []tools.Definition
	Functions           map[string]tools.Function
	DefaultMaxTurns     int
	ExternalBroker      *extagent.Broker
	MemoryAppend        func(task, result string) error
	Context             agent.ContextConfig
	ServerLogWriter     io.Writer
}

type Service struct {
	store               *session.Store
	workspaceRoot       string
	client              agent.ChatCompletionClient
	model               string
	systemPrompt        string
	systemPromptBuilder func() (string, error)
	planner             *agent.Planner
	toolDefinitions     []tools.Definition
	functions           map[string]tools.Function
	maxTurns            int
	locker              *KeyedLocker
	externalBroker      *extagent.Broker
	memoryAppend        func(task, result string) error
	context             agent.ContextConfig
	serverLogWriter     io.Writer
}

type TurnRequest struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
}

type TurnResponse struct {
	SessionID string `json:"session_id"`
	Reply     string `json:"reply"`
	Streamed  bool   `json:"-"`
}

type TurnOptions struct {
	OutputWriter io.Writer
	Stream       bool
}

func NewService(options ServiceOptions) *Service {
	maxTurns := options.DefaultMaxTurns
	if maxTurns <= 0 {
		maxTurns = agent.DefaultMaxIterations
	}
	return &Service{
		store:               session.NewStore(options.WorkspaceRoot),
		workspaceRoot:       options.WorkspaceRoot,
		client:              options.Client,
		model:               options.Model,
		systemPrompt:        options.SystemPrompt,
		systemPromptBuilder: options.SystemPromptBuilder,
		planner:             options.Planner,
		toolDefinitions:     append([]tools.Definition{}, options.ToolDefinitions...),
		functions:           cloneFunctions(options.Functions),
		maxTurns:            maxTurns,
		locker:              NewKeyedLocker(),
		externalBroker:      options.ExternalBroker,
		memoryAppend:        options.MemoryAppend,
		context:             options.Context,
		serverLogWriter:     options.ServerLogWriter,
	}
}

func (service *Service) HandleTurn(ctx context.Context, request TurnRequest) (TurnResponse, error) {
	return service.HandleTurnWithOptions(ctx, request, TurnOptions{})
}

func (service *Service) HandleTurnWithOptions(ctx context.Context, request TurnRequest, options TurnOptions) (TurnResponse, error) {
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
	systemPrompt, err := service.currentSystemPrompt()
	if err != nil {
		return TurnResponse{}, err
	}
	conversation, err := appruntime.PrepareConversation(service.store, sessionID, createOptions, systemPrompt)
	if err != nil {
		return TurnResponse{}, err
	}

	logFile, err := conversation.Session.OpenOutputFile()
	if err != nil {
		_ = conversation.MarkFailed(err)
		return TurnResponse{}, err
	}
	defer logFile.Close()

	logWriter := service.turnLogWriter(conversation.Session.ID(), logFile, options.OutputWriter)
	turnErrorWriter := service.turnErrorWriter(conversation.Session.ID(), logFile, options.OutputWriter)

	if err := conversation.AddUserMessage(message); err != nil {
		writeTurnError(turnErrorWriter, err)
		_ = conversation.MarkFailed(err)
		return TurnResponse{}, err
	}

	if reply, handled, skillErr := service.handleSkillSlash(ctx, conversation.Messages, message, conversation.Recorder(), logWriter, systemPrompt); handled {
		if skillErr != nil {
			writeTurnError(turnErrorWriter, skillErr)
			_ = conversation.MarkFailed(skillErr)
			return TurnResponse{}, skillErr
		}
		assistantMessage := map[string]any{"role": "assistant", "content": reply}
		if err := conversation.Session.RecordMessage(assistantMessage); err != nil {
			writeTurnError(turnErrorWriter, err)
			_ = conversation.MarkFailed(err)
			return TurnResponse{}, err
		}
		conversation.Messages = append(conversation.Messages, assistantMessage)
		if err := conversation.MarkCompleted(); err != nil {
			writeTurnError(turnErrorWriter, err)
			return TurnResponse{}, err
		}
		service.appendMemory(message, reply)
		writeTurnReply(logWriter, reply, false)
		return TurnResponse{SessionID: conversation.Session.ID(), Reply: reply}, nil
	}

	if service.externalBroker != nil {
		routedAgent, routedPrompt, _, routeErr := service.externalBroker.Resolve(message, conversation.Session.ID())
		if routeErr != nil {
			writeTurnError(turnErrorWriter, routeErr)
			_ = conversation.MarkFailed(routeErr)
			return TurnResponse{}, routeErr
		}
		if routedAgent == extagent.AgentDefault {
			if err := service.externalBroker.Clear(conversation.Session.ID()); err != nil {
				writeTurnError(turnErrorWriter, err)
				_ = conversation.MarkFailed(err)
				return TurnResponse{}, err
			}
			reply := "switched to default model"
			assistantMessage := map[string]any{"role": "assistant", "content": reply}
			if err := conversation.Session.RecordMessage(assistantMessage); err != nil {
				writeTurnError(turnErrorWriter, err)
				_ = conversation.MarkFailed(err)
				return TurnResponse{}, err
			}
			conversation.Messages = append(conversation.Messages, assistantMessage)
			if err := conversation.MarkCompleted(); err != nil {
				writeTurnError(turnErrorWriter, err)
				return TurnResponse{}, err
			}
			writeTurnReply(logWriter, reply, false)
			return TurnResponse{SessionID: conversation.Session.ID(), Reply: reply}, nil
		}
		if routedAgent != "" {
			result, err := service.externalBroker.SendTurn(ctx, extagent.TurnRequest{
				BQSessionID: conversation.Session.ID(),
				Agent:       routedAgent,
				Prompt:      routedPrompt,
				CWD:         service.workspaceRoot,
			})
			if err != nil {
				writeTurnError(turnErrorWriter, err)
				_ = conversation.MarkFailed(err)
				return TurnResponse{}, err
			}
			assistantMessage := map[string]any{"role": "assistant", "content": result.Reply}
			if err := conversation.Session.RecordMessage(assistantMessage); err != nil {
				writeTurnError(turnErrorWriter, err)
				_ = conversation.MarkFailed(err)
				return TurnResponse{}, err
			}
			conversation.Messages = append(conversation.Messages, assistantMessage)
			if err := conversation.MarkCompleted(); err != nil {
				writeTurnError(turnErrorWriter, err)
				return TurnResponse{}, err
			}
			service.appendMemory(message, result.Reply)
			writeTurnReply(logWriter, result.Reply, false)
			return TurnResponse{SessionID: conversation.Session.ID(), Reply: result.Reply}, nil
		}
	}

	app := agent.NewWithOptions(service.client, service.model, agent.Options{
		SystemPrompt:    systemPrompt,
		LogWriter:       logWriter,
		ToolDefinitions: service.toolDefinitions,
		Functions:       service.functions,
		Planner:         service.planner,
		Recorder:        conversation.Recorder(),
		Stream:          options.Stream,
		WorkspaceRoot:   service.workspaceRoot,
		Context:         service.context,
	})

	result, _, err := app.RunConversationTurn(ctx, conversation.Messages, service.maxTurns)
	if err != nil {
		writeTurnError(turnErrorWriter, err)
		_ = conversation.MarkFailed(err)
		return TurnResponse{}, err
	}
	if err := conversation.MarkCompleted(); err != nil {
		writeTurnError(turnErrorWriter, err)
		return TurnResponse{}, err
	}
	service.appendMemory(message, result)
	writeTurnReply(logWriter, result, options.Stream)

	return TurnResponse{SessionID: conversation.Session.ID(), Reply: result, Streamed: options.Stream}, nil
}

func (service *Service) handleSkillSlash(ctx context.Context, _ []map[string]any, message string, recorder agent.MessageRecorder, logWriter io.Writer, systemPrompt string) (string, bool, error) {
	message = strings.TrimSpace(message)
	if !strings.HasPrefix(message, "/skill") {
		return "", false, nil
	}
	fields := strings.Fields(message)
	if len(fields) < 2 {
		return "", true, fmt.Errorf("skill name is required after /skill")
	}
	skillID := strings.TrimSpace(fields[1])
	args := ""
	if len(fields) > 2 {
		args = strings.TrimSpace(strings.Join(fields[2:], " "))
	}

	app := agent.NewWithOptions(service.client, service.model, agent.Options{
		SystemPrompt:    systemPrompt,
		LogWriter:       logWriter,
		ToolDefinitions: service.toolDefinitions,
		Functions:       service.functions,
		Planner:         service.planner,
		Recorder:        recorder,
		WorkspaceRoot:   service.workspaceRoot,
		Context:         service.context,
	})
	result, err := app.RunSkill(ctx, skillID, args, service.maxTurns)
	return result, true, err
}

func (service *Service) currentSystemPrompt() (string, error) {
	if service == nil {
		return "", nil
	}
	if service.systemPromptBuilder == nil {
		return service.systemPrompt, nil
	}
	prompt, err := service.systemPromptBuilder()
	if err != nil {
		return "", err
	}
	return prompt, nil
}

func cloneFunctions(functions map[string]tools.Function) map[string]tools.Function {
	cloned := make(map[string]tools.Function, len(functions))
	for name, function := range functions {
		cloned[name] = function
	}
	return cloned
}

func (service *Service) appendMemory(task, result string) {
	if service == nil || service.memoryAppend == nil {
		return
	}
	if err := service.memoryAppend(task, result); err != nil {
		log.Printf("memory append failed: %v", err)
	}
}

func (service *Service) turnLogWriter(sessionID string, sessionWriter io.Writer, outputWriter io.Writer) io.Writer {
	if service == nil {
		return sessionWriter
	}
	if service.serverLogWriter != nil {
		serverWriter := io.Writer(newServerLogWriter(service.serverLogWriter, sessionID))
		if outputWriter != nil {
			return io.MultiWriter(outputWriter, serverWriter)
		}
		return serverWriter
	}
	if outputWriter != nil {
		return io.MultiWriter(outputWriter, sessionWriter)
	}
	return sessionWriter
}

func (service *Service) turnErrorWriter(sessionID string, sessionWriter io.Writer, outputWriter io.Writer) io.Writer {
	if service == nil || service.serverLogWriter == nil {
		return sessionWriter
	}
	serverWriter := io.Writer(newServerLogWriter(service.serverLogWriter, sessionID))
	if outputWriter != nil {
		return io.MultiWriter(outputWriter, serverWriter)
	}
	return serverWriter
}

type serverLogWriter struct {
	writer    io.Writer
	sessionID string
	buffer    bytes.Buffer
}

func newServerLogWriter(writer io.Writer, sessionID string) *serverLogWriter {
	return &serverLogWriter{writer: writer, sessionID: strings.TrimSpace(sessionID)}
}

func (writer *serverLogWriter) Write(data []byte) (int, error) {
	if _, err := writer.buffer.Write(data); err != nil {
		return 0, err
	}
	for {
		line, err := writer.buffer.ReadString('\n')
		if err != nil {
			writer.buffer.WriteString(line)
			return len(data), nil
		}
		if _, writeErr := io.WriteString(writer.writer, writer.formatLine(strings.TrimSuffix(line, "\n"))+"\n"); writeErr != nil {
			return 0, writeErr
		}
	}
}

func (writer *serverLogWriter) formatLine(line string) string {
	line = strings.TrimRight(line, "\r")
	return fmt.Sprintf("[%s] [session:%s] %s", time.Now().UTC().Format(time.RFC3339), writer.sessionID, line)
}

func writeTurnReply(writer io.Writer, reply string, streamed bool) {
	if writer == nil {
		return
	}
	if streamed {
		_, _ = io.WriteString(writer, "\n")
		return
	}
	_, _ = fmt.Fprintln(writer, reply)
}

func writeTurnError(writer io.Writer, err error) {
	if writer == nil || err == nil {
		return
	}
	_, _ = fmt.Fprintln(writer, err)
}
