package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"

	"bqagent/internal/agent"
	"bqagent/internal/extagent"
	"bqagent/internal/logging"
	appruntime "bqagent/internal/runtime"
	"bqagent/internal/session"
	"bqagent/internal/tools"
	"bqagent/internal/workspace"
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
	processGroupStops   *processGroupStopRegistry
	environmentCommands *environmentCommandGuard
}

type TurnRequest struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
	// PeerKey identifies the async channel peer that owns this turn. It is used
	// for control commands such as /stop and is not accepted from HTTP JSON.
	PeerKey string `json:"-"`
	// Images are decoded inbound images attached to this turn. They are set by
	// channels (e.g. iLink) and sent to the model as a multimodal user message.
	Images []agent.ImageAttachment `json:"-"`
}

// imageOnlyPlaceholder is the synthetic task/memory text used when a turn carries
// images but no text. It keeps session bookkeeping and memory readable; the model
// still receives the actual image content.
const imageOnlyPlaceholder = "[图片]"

type TurnResponse struct {
	SessionID string `json:"session_id"`
	Reply     string `json:"reply"`
	Streamed  bool   `json:"-"`
}

type TurnOptions struct {
	OutputWriter   io.Writer
	ProgressWriter io.Writer
	TokenSink      io.Writer
	Stream         bool
	MaxIterations  int
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
		processGroupStops:   newProcessGroupStopRegistry(),
		environmentCommands: newEnvironmentCommandGuard(0),
	}
}

func (service *Service) HandleTurn(ctx context.Context, request TurnRequest) (TurnResponse, error) {
	return service.HandleTurnWithOptions(ctx, request, TurnOptions{})
}

func (service *Service) stopProcessGroupReply(peerKey string, sessionID string) string {
	if service.stopProcessGroup(peerKey, sessionID) > 0 {
		return stopCommandStoppedReply
	}
	return stopCommandIdleReply
}

func (service *Service) stopProcessGroup(peerKey string, sessionID string) int {
	if service == nil || service.processGroupStops == nil {
		return 0
	}
	return service.processGroupStops.Stop(stopKeys(peerKey, sessionID))
}

func (service *Service) functionsForTurn(peerKey string, sessionID string) map[string]tools.Function {
	functions := cloneFunctions(service.functions)
	original, ok := functions["execute_bash"]
	keys := stopKeys(peerKey, sessionID)
	if !ok || len(keys) == 0 || service.processGroupStops == nil {
		return functions
	}
	functions["execute_bash"] = func(ctx context.Context, args map[string]any) (string, error) {
		command := commandFromArgs(args)
		if message, blocked := service.environmentCommands.BlockedMessage(keys, command); blocked {
			return message, fmt.Errorf("environment install/repair command blocked after previous failure")
		}
		toolCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		tracked := false
		if _, ok := classifyEnvironmentCommand(command); ok {
			tracked = true
			timeoutCtx, timeoutCancel := context.WithTimeout(toolCtx, service.environmentCommands.CommandTimeout())
			defer timeoutCancel()
			toolCtx = timeoutCtx
		}
		unregister := service.processGroupStops.Register(keys, command, cancel)
		defer unregister()
		output, err := original(toolCtx, args)
		if tracked {
			service.environmentCommands.Record(keys, command, output, err)
			if err != nil && errors.Is(err, context.DeadlineExceeded) {
				return output, fmt.Errorf("%s", environmentCommandTimeoutMessage(command, service.environmentCommands.CommandTimeout()))
			}
		}
		return output, err
	}
	return functions
}

func commandFromArgs(args map[string]any) string {
	command, _ := args["command"].(string)
	return command
}

func (service *Service) HandleTurnWithOptions(ctx context.Context, request TurnRequest, options TurnOptions) (TurnResponse, error) {
	message := strings.TrimSpace(request.Message)
	if isStopCommand(message) {
		reply := service.stopProcessGroupReply(request.PeerKey, request.SessionID)
		return TurnResponse{SessionID: strings.TrimSpace(request.SessionID), Reply: reply}, nil
	}
	if message == "" && len(request.Images) == 0 {
		return TurnResponse{}, fmt.Errorf("message is required")
	}
	// effectiveText is what session bookkeeping and memory record; for image-only
	// turns it falls back to a placeholder so those stay readable.
	effectiveText := message
	if effectiveText == "" {
		effectiveText = imageOnlyPlaceholder
	}

	sessionID := strings.TrimSpace(request.SessionID)
	createOptions := &session.CreateOptions{Task: effectiveText, Planned: service.planner != nil, Chat: true}
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
		markConversationFailed(conversation, err)
		return TurnResponse{}, err
	}
	defer logFile.Close()

	sessionLogWriter := logging.NewTimestampWriter(logFile)
	progressWriter := options.ProgressWriter
	if progressWriter == nil {
		progressWriter = options.OutputWriter
	}
	logWriter := service.turnLogWriter(sessionLogWriter, options.OutputWriter)
	turnErrorWriter := service.turnErrorWriter(sessionLogWriter, options.OutputWriter)

	if err := conversation.AddUserMessageWithImages(message, request.Images); err != nil {
		writeTurnError(turnErrorWriter, err)
		markConversationFailed(conversation, err)
		return TurnResponse{}, err
	}

	if reply, handled, skillErr := service.handleSkillCommand(ctx, message, conversation.Recorder(), logWriter, progressWriter, systemPrompt); handled {
		if skillErr != nil {
			writeTurnError(turnErrorWriter, skillErr)
			markConversationFailed(conversation, skillErr)
			return TurnResponse{}, skillErr
		}
		assistantMessage := map[string]any{"role": "assistant", "content": reply}
		if err := conversation.Session.RecordMessage(assistantMessage); err != nil {
			writeTurnError(turnErrorWriter, err)
			markConversationFailed(conversation, err)
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
			markConversationFailed(conversation, routeErr)
			return TurnResponse{}, routeErr
		}
		if routedAgent == extagent.AgentDefault {
			if err := service.externalBroker.Clear(conversation.Session.ID()); err != nil {
				writeTurnError(turnErrorWriter, err)
				markConversationFailed(conversation, err)
				return TurnResponse{}, err
			}
			reply := "switched to default model"
			assistantMessage := map[string]any{"role": "assistant", "content": reply}
			if err := conversation.Session.RecordMessage(assistantMessage); err != nil {
				writeTurnError(turnErrorWriter, err)
				markConversationFailed(conversation, err)
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
				markConversationFailed(conversation, err)
				return TurnResponse{}, err
			}
			assistantMessage := map[string]any{"role": "assistant", "content": result.Reply}
			if err := conversation.Session.RecordMessage(assistantMessage); err != nil {
				writeTurnError(turnErrorWriter, err)
				markConversationFailed(conversation, err)
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

	functions := service.functionsForTurn(request.PeerKey, conversation.Session.ID())
	app := agent.NewWithOptions(service.client, service.model, agent.Options{
		SystemPrompt:    systemPrompt,
		LogWriter:       logWriter,
		ToolDefinitions: service.toolDefinitions,
		Functions:       functions,
		Planner:         service.planner,
		Recorder:        conversation.Recorder(),
		Stream:          options.Stream,
		WorkspaceRoot:   service.workspaceRoot,
		ProgressWriter:  progressWriter,
		TokenSink:       options.TokenSink,
		Context:         service.context,
	})

	maxTurns := service.maxTurns
	if options.MaxIterations > 0 {
		maxTurns = options.MaxIterations
	}
	result, _, err := app.RunConversationTurn(ctx, conversation.Messages, maxTurns)
	if err != nil {
		writeTurnError(turnErrorWriter, err)
		markConversationFailed(conversation, err)
		return TurnResponse{}, err
	}
	if err := conversation.MarkCompleted(); err != nil {
		writeTurnError(turnErrorWriter, err)
		return TurnResponse{}, err
	}
	service.appendMemory(effectiveText, result)
	writeTurnReply(logWriter, result, options.Stream)

	return TurnResponse{SessionID: conversation.Session.ID(), Reply: result, Streamed: options.Stream}, nil
}

func (service *Service) handleSkillCommand(ctx context.Context, message string, recorder agent.MessageRecorder, logWriter io.Writer, progressWriter io.Writer, systemPrompt string) (string, bool, error) {
	message = strings.TrimSpace(message)
	token, rest := splitFirstToken(message)
	if token == "" {
		return "", false, nil
	}

	var skillID string
	var args string
	if token == "/skill" {
		skillToken, skillArgs := splitFirstToken(rest)
		if skillToken == "" {
			return "", true, fmt.Errorf("skill name is required after /skill")
		}
		resolved, _, err := service.resolveSkillToken(skillToken)
		if err != nil {
			return "", true, err
		}
		skillID = resolved
		args = skillArgs
	} else {
		if strings.HasPrefix(token, "/") {
			return "", false, nil
		}
		resolved, handled, err := service.resolveSkillToken(token)
		if err != nil || !handled {
			return "", handled, err
		}
		skillID = resolved
		args = rest
	}

	app := agent.NewWithOptions(service.client, service.model, agent.Options{
		SystemPrompt:    systemPrompt,
		LogWriter:       logWriter,
		ToolDefinitions: service.toolDefinitions,
		Functions:       service.functions,
		Planner:         service.planner,
		Recorder:        recorder,
		WorkspaceRoot:   service.workspaceRoot,
		ProgressWriter:  progressWriter,
		Context:         service.context,
	})
	result, err := app.RunSkill(ctx, skillID, args, service.maxTurns)
	return result, true, err
}

func (service *Service) resolveSkillToken(token string) (string, bool, error) {
	ws := &workspace.Workspace{Root: service.workspaceRoot}
	skill, handled, err := ws.ResolveSkill(token)
	if err != nil || !handled {
		return "", handled, err
	}
	return skill.ID, true, nil
}

func splitFirstToken(message string) (string, string) {
	message = strings.TrimSpace(message)
	if message == "" {
		return "", ""
	}
	for index, r := range message {
		if index > 0 && strings.ContainsRune(" \t\n\r", r) {
			return message[:index], strings.TrimSpace(message[index:])
		}
	}
	return message, ""
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

// markConversationFailed marks the session as failed and logs when even that
// bookkeeping write fails, since the session status would otherwise silently
// stay "running".
func markConversationFailed(conversation *appruntime.Conversation, err error) {
	if markErr := conversation.MarkFailed(err); markErr != nil {
		log.Printf("session mark-failed failed: %v", markErr)
	}
}

func (service *Service) appendMemory(task, result string) {
	if service == nil || service.memoryAppend == nil {
		return
	}
	if err := service.memoryAppend(task, result); err != nil {
		log.Printf("memory append failed: %v", err)
	}
}

func (service *Service) turnLogWriter(sessionWriter io.Writer, outputWriter io.Writer) io.Writer {
	if outputWriter != nil {
		return io.MultiWriter(outputWriter, sessionWriter)
	}
	return sessionWriter
}

func (service *Service) turnErrorWriter(sessionWriter io.Writer, outputWriter io.Writer) io.Writer {
	return service.turnLogWriter(sessionWriter, outputWriter)
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
