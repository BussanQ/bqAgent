package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strings"
	"time"

	"bqagent/internal/agent"
	"bqagent/internal/extagent"
	"bqagent/internal/logging"
	appmemory "bqagent/internal/memory"
	appruntime "bqagent/internal/runtime"
	"bqagent/internal/session"
	"bqagent/internal/subagent"
	"bqagent/internal/tools"
	apptrace "bqagent/internal/trace"
	"bqagent/internal/workspace"
)

type ServiceOptions struct {
	WorkspaceRoot       string
	Client              agent.ChatCompletionClient
	APIType             agent.APIType
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
	Subagents           *subagent.Manager
	MemoryStore         *appmemory.Store
}

type Service struct {
	store               *session.Store
	workspaceRoot       string
	client              agent.ChatCompletionClient
	apiType             agent.APIType
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
	traceStore          *apptrace.Store
	subagents           *subagent.Manager
	memoryStore         *appmemory.Store
	activeTurns         *activeTurnRegistry
}

type TurnRequest struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
	// TurnID identifies one in-flight conversation turn. Any caller/channel can
	// provide it and later cancel the whole turn through Service.StopTurn.
	TurnID string `json:"turn_id,omitempty"`
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
	RunID     string `json:"run_id,omitempty"`
	Streamed  bool   `json:"-"`
}

type TurnOptions struct {
	OutputWriter   io.Writer
	ProgressWriter io.Writer
	TokenSink      io.Writer
	Stream         bool
	MaxIterations  int
	Stage          agent.StageConfig
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
		apiType:             agent.NormalizeAPIType(string(options.APIType)),
		model:               agent.EffectiveModel(options.Model),
		systemPrompt:        options.SystemPrompt,
		systemPromptBuilder: options.SystemPromptBuilder,
		planner:             options.Planner,
		toolDefinitions:     append(append([]tools.Definition{}, options.ToolDefinitions...), subagentToolDefinitions(options.Subagents != nil)...),
		functions:           cloneFunctions(options.Functions),
		maxTurns:            maxTurns,
		locker:              NewKeyedLocker(),
		externalBroker:      options.ExternalBroker,
		memoryAppend:        options.MemoryAppend,
		context:             options.Context,
		processGroupStops:   newProcessGroupStopRegistry(),
		environmentCommands: newEnvironmentCommandGuard(0),
		traceStore:          apptrace.NewStore(options.WorkspaceRoot),
		subagents:           options.Subagents,
		memoryStore:         options.MemoryStore,
		activeTurns:         newActiveTurnRegistry(),
	}
}

func (service *Service) HandleTurn(ctx context.Context, request TurnRequest) (TurnResponse, error) {
	return service.HandleTurnWithOptions(ctx, request, TurnOptions{})
}

// StopTurn cancels the model request and every context-aware tool running for
// the identified turn. Channels can opt in by assigning TurnRequest.TurnID.
func (service *Service) StopTurn(turnID string) bool {
	if service == nil || service.activeTurns == nil {
		return false
	}
	return service.activeTurns.Stop(turnID)
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
	for name, function := range service.subagentFunctions(sessionID) {
		functions[name] = function
	}
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

func (service *Service) HandleTurnWithOptions(ctx context.Context, request TurnRequest, options TurnOptions) (response TurnResponse, err error) {
	message := strings.TrimSpace(request.Message)
	if isStopCommand(message) {
		reply := service.stopProcessGroupReply(request.PeerKey, request.SessionID)
		return TurnResponse{SessionID: strings.TrimSpace(request.SessionID), Reply: reply}, nil
	}
	if message == "" && len(request.Images) == 0 {
		return TurnResponse{}, fmt.Errorf("message is required")
	}
	turnID := strings.TrimSpace(request.TurnID)
	if turnID != "" {
		if !validTurnID(turnID) {
			return TurnResponse{}, fmt.Errorf("invalid turn_id")
		}
		turnCtx, cancel := context.WithCancel(ctx)
		unregister, registered := service.activeTurns.Register(turnID, cancel)
		if !registered {
			cancel()
			return TurnResponse{}, fmt.Errorf("turn %q is already active", turnID)
		}
		defer func() {
			unregister()
			cancel()
		}()
		ctx = turnCtx
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
	systemPrompt, err := service.currentSystemPrompt(effectiveText)
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

	if feedbackReply, handled, feedbackErr := service.handleFeedbackCommand(message, conversation.Session); handled {
		if feedbackErr != nil {
			writeTurnError(turnErrorWriter, feedbackErr)
			markConversationFailed(conversation, feedbackErr)
			return TurnResponse{}, feedbackErr
		}
		userMessage := map[string]any{"role": "user", "content": message}
		assistantMessage := map[string]any{"role": "assistant", "content": feedbackReply}
		if recordErr := conversation.Session.RecordMessages(userMessage, assistantMessage); recordErr != nil {
			return TurnResponse{}, recordErr
		}
		conversation.Messages = append(conversation.Messages, userMessage, assistantMessage)
		if completeErr := service.completeConversation(conversation); completeErr != nil {
			return TurnResponse{}, completeErr
		}
		return TurnResponse{SessionID: conversation.Session.ID(), Reply: feedbackReply}, nil
	}

	runRecorder, traceErr := service.traceStore.Create(conversation.Session.ID(), apptrace.NewID("turn"), "", "agent", service.model, systemPrompt)
	if traceErr != nil {
		fmt.Fprintf(turnErrorWriter, "trace create failed: %v\n", traceErr)
	}
	runID := ""
	if runRecorder != nil {
		runID = runRecorder.RunID()
		if traceLog, openErr := runRecorder.OpenOutputFile(); openErr == nil {
			defer traceLog.Close()
			logWriter = io.MultiWriter(logWriter, traceLog)
			turnErrorWriter = io.MultiWriter(turnErrorWriter, traceLog)
		}
		if setErr := conversation.Session.SetLastRunID(runID); setErr != nil {
			fmt.Fprintf(turnErrorWriter, "trace session link failed: %v\n", setErr)
		}
		defer func() {
			if finishErr := runRecorder.Finish(response.Reply, err); finishErr != nil {
				fmt.Fprintf(turnErrorWriter, "trace finish failed: %v\n", finishErr)
			}
			if response.RunID == "" {
				response.RunID = runID
			}
		}()
		ctx = apptrace.WithRunID(ctx, runID)
	}

	if err := conversation.AddUserMessageWithImages(message, request.Images); err != nil {
		writeTurnError(turnErrorWriter, err)
		markConversationFailed(conversation, err)
		return TurnResponse{}, err
	}
	if reply, handled, memoryErr := service.handleMemoryCommand(message, runID); handled {
		if memoryErr != nil {
			markConversationFailed(conversation, memoryErr)
			return TurnResponse{}, memoryErr
		}
		assistantMessage := map[string]any{"role": "assistant", "content": reply}
		if recordErr := conversation.Session.RecordMessage(assistantMessage); recordErr != nil {
			return TurnResponse{}, recordErr
		}
		conversation.Messages = append(conversation.Messages, assistantMessage)
		if completeErr := service.completeConversation(conversation); completeErr != nil {
			return TurnResponse{}, completeErr
		}
		return TurnResponse{SessionID: conversation.Session.ID(), Reply: reply, RunID: runID}, nil
	}

	if reply, handled, commandErr := service.handleAgentCommand(ctx, message, conversation.Session.ID(), runRecorder); handled {
		if commandErr != nil {
			writeTurnError(turnErrorWriter, commandErr)
			markConversationFailed(conversation, commandErr)
			return TurnResponse{}, commandErr
		}
		assistantMessage := map[string]any{"role": "assistant", "content": reply}
		if recordErr := conversation.Session.RecordMessage(assistantMessage); recordErr != nil {
			return TurnResponse{}, recordErr
		}
		conversation.Messages = append(conversation.Messages, assistantMessage)
		if completeErr := service.completeConversation(conversation); completeErr != nil {
			return TurnResponse{}, completeErr
		}
		return TurnResponse{SessionID: conversation.Session.ID(), Reply: reply, RunID: runID}, nil
	}

	if reply, handled, skillErr := service.handleSkillCommand(ctx, message, conversation.Recorder(), logWriter, progressWriter, systemPrompt, runRecorder); handled {
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
		if err := service.completeConversation(conversation); err != nil {
			writeTurnError(turnErrorWriter, err)
			return TurnResponse{}, err
		}
		service.appendMemory(message, reply)
		writeTurnReply(logWriter, reply, false)
		return TurnResponse{SessionID: conversation.Session.ID(), Reply: reply, RunID: runID}, nil
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
			if err := service.completeConversation(conversation); err != nil {
				writeTurnError(turnErrorWriter, err)
				return TurnResponse{}, err
			}
			writeTurnReply(logWriter, reply, false)
			return TurnResponse{SessionID: conversation.Session.ID(), Reply: reply, RunID: runID}, nil
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
			if err := service.completeConversation(conversation); err != nil {
				writeTurnError(turnErrorWriter, err)
				return TurnResponse{}, err
			}
			service.appendMemory(message, result.Reply)
			writeTurnReply(logWriter, result.Reply, false)
			if runRecorder != nil {
				_ = runRecorder.Event("external_agent", map[string]any{"agent": routedAgent, "transport": result.State.Transport, "external_session_id": result.State.ExternalSessionID})
			}
			return TurnResponse{SessionID: conversation.Session.ID(), Reply: result.Reply, RunID: runID}, nil
		}
	}

	functions := service.functionsForTurn(request.PeerKey, conversation.Session.ID())
	app := agent.NewWithOptions(service.client, service.model, agent.Options{
		SystemPrompt:    systemPrompt,
		APIType:         service.apiType,
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
		Stage:           options.Stage,
		Trace:           runRecorder,
	})

	maxTurns := service.maxTurns
	if options.MaxIterations > 0 {
		maxTurns = options.MaxIterations
	}
	result, updatedMessages, err := app.RunConversationTurn(ctx, conversation.Messages, maxTurns)
	if err != nil {
		conversation.Messages = agent.BoundWorkingMessages(updatedMessages, service.context)
		// Best effort: even a failed model/tool request may already have pruned a
		// legacy oversized transcript into a safe working context for the retry.
		_ = conversation.SaveWorkingContext()
		writeTurnError(turnErrorWriter, err)
		markConversationFailed(conversation, err)
		return TurnResponse{}, err
	}
	conversation.Messages = updatedMessages
	if err := service.completeConversation(conversation); err != nil {
		writeTurnError(turnErrorWriter, err)
		return TurnResponse{}, err
	}
	service.appendMemory(effectiveText, result)
	writeTurnReply(logWriter, result, options.Stream)

	return TurnResponse{SessionID: conversation.Session.ID(), Reply: result, RunID: runID, Streamed: options.Stream}, nil
}

func (service *Service) completeConversation(conversation *appruntime.Conversation) error {
	conversation.Messages = agent.BoundWorkingMessages(conversation.Messages, service.context)
	if err := conversation.SaveWorkingContext(); err != nil {
		return err
	}
	return conversation.MarkCompleted()
}

func (service *Service) handleFeedbackCommand(message string, savedSession *session.Session) (string, bool, error) {
	fields := strings.Fields(strings.TrimSpace(message))
	if len(fields) == 0 || strings.ToLower(fields[0]) != "/feedback" {
		return "", false, nil
	}
	if savedSession == nil {
		return "", true, fmt.Errorf("feedback requires a session or explicit run id")
	}
	runID := savedSession.Meta().LastRunID
	index := 1
	if len(fields) > 2 && fields[1] != "up" && fields[1] != "down" {
		runID = fields[1]
		index = 2
	}
	if runID == "" || len(fields) <= index {
		return "", true, fmt.Errorf("usage: /feedback [run-id] up|down [comment]")
	}
	rating := strings.ToLower(fields[index])
	comment := ""
	if len(fields) > index+1 {
		comment = strings.Join(fields[index+1:], " ")
	}
	feedback, err := service.traceStore.AddFeedback(runID, rating, comment, "command")
	if err != nil {
		return "", true, err
	}
	return fmt.Sprintf("feedback recorded for %s: %s", feedback.RunID, feedback.Rating), true, nil
}

func (service *Service) handleAgentCommand(ctx context.Context, message, sessionID string, recorder *apptrace.Recorder) (string, bool, error) {
	fields := strings.Fields(strings.TrimSpace(message))
	if len(fields) == 0 || strings.ToLower(fields[0]) != "/agent" {
		return "", false, nil
	}
	if service.subagents == nil {
		return "", true, fmt.Errorf("subagent manager is unavailable")
	}
	if len(fields) < 2 {
		return "", true, fmt.Errorf("usage: /agent spawn|list|status|wait|interrupt|cancel|resume|result|collect|apply|cleanup")
	}
	action := strings.ToLower(fields[1])
	switch action {
	case "spawn":
		if len(fields) < 4 {
			return "", true, fmt.Errorf("usage: /agent spawn <claude|codex|cursor|opencode> [--timeout 30m] [--retries 1] [--include-dirty] -- <task>")
		}
		agentName := extagent.AgentName(strings.ToLower(fields[2]))
		timeout, retries, includeDirty := 30*time.Minute, 1, false
		var promptParts []string
		for i := 3; i < len(fields); i++ {
			switch fields[i] {
			case "--":
				promptParts = append(promptParts, fields[i+1:]...)
				i = len(fields)
			case "--include-dirty":
				includeDirty = true
			case "--timeout":
				if i+1 >= len(fields) {
					return "", true, fmt.Errorf("--timeout requires a value")
				}
				parsed, err := subagent.ParseDuration(fields[i+1], timeout)
				if err != nil {
					return "", true, err
				}
				timeout = parsed
				i++
			case "--retries":
				if i+1 >= len(fields) {
					return "", true, fmt.Errorf("--retries requires a value")
				}
				parsed, err := subagent.ParseInt(fields[i+1], retries)
				if err != nil {
					return "", true, err
				}
				retries = parsed
				i++
			default:
				promptParts = append(promptParts, fields[i])
			}
		}
		turnID, parentRunID := "", ""
		if recorder != nil {
			turnID, parentRunID = recorder.TurnID(), recorder.RunID()
		}
		task, err := service.subagents.Spawn(subagent.SpawnOptions{ParentSessionID: sessionID, ParentTurnID: turnID, ParentRunID: parentRunID, Agent: agentName, Prompt: strings.Join(promptParts, " "), Timeout: timeout, Retries: retries, IncludeDirty: includeDirty})
		if err != nil {
			return "", true, err
		}
		return fmt.Sprintf("subagent queued: %s (%s)", task.ID, task.Agent), true, nil
	case "list":
		var status subagent.Status
		if len(fields) >= 4 && fields[2] == "--status" {
			status = subagent.Status(fields[3])
		}
		tasks, err := service.subagents.List(status)
		if err != nil {
			return "", true, err
		}
		content, _ := json.MarshalIndent(tasks, "", "  ")
		return string(content), true, nil
	case "status", "result":
		if len(fields) < 3 {
			return "", true, fmt.Errorf("task id is required")
		}
		task, err := service.subagents.Status(fields[2])
		if err != nil {
			return "", true, err
		}
		if action == "result" {
			return fmt.Sprintf("%s [%s]\n%s\npatch: %s", task.ID, task.Status, task.Result, filepath.Join(service.workspaceRoot, ".agent", "subagents", task.ID, "diff.patch")), true, nil
		}
		content, _ := json.MarshalIndent(task, "", "  ")
		return string(content), true, nil
	case "wait":
		if len(fields) < 3 {
			return "", true, fmt.Errorf("wait requires a task id or all")
		}
		wait := 30 * time.Second
		if len(fields) >= 5 && fields[3] == "--timeout" {
			parsed, err := time.ParseDuration(fields[4])
			if err != nil {
				return "", true, err
			}
			wait = parsed
		}
		waitCtx, cancel := context.WithTimeout(ctx, wait)
		defer cancel()
		if fields[2] == "all" {
			tasks, listErr := service.subagents.List("")
			if listErr != nil {
				return "", true, listErr
			}
			completed := make([]subagent.Task, 0, len(tasks))
			for _, item := range tasks {
				if item.ParentSessionID != sessionID {
					continue
				}
				task, waitErr := service.subagents.Wait(waitCtx, item.ID)
				if task != nil {
					completed = append(completed, *task)
				}
				if waitErr != nil && !errors.Is(waitErr, context.DeadlineExceeded) {
					return "", true, waitErr
				}
				if waitCtx.Err() != nil {
					break
				}
			}
			content, _ := json.MarshalIndent(completed, "", "  ")
			return string(content), true, nil
		}
		task, err := service.subagents.Wait(waitCtx, fields[2])
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return "", true, err
		}
		content, _ := json.MarshalIndent(task, "", "  ")
		return string(content), true, nil
	case "interrupt", "cancel", "apply", "cleanup":
		if len(fields) < 3 {
			return "", true, fmt.Errorf("task id is required")
		}
		var err error
		switch action {
		case "interrupt":
			err = service.subagents.Interrupt(fields[2])
		case "cancel":
			err = service.subagents.Cancel(fields[2])
		case "apply":
			err = service.subagents.Apply(fields[2])
		case "cleanup":
			err = service.subagents.Cleanup(fields[2])
		}
		if err != nil {
			return "", true, err
		}
		return action + " completed for " + fields[2], true, nil
	case "resume":
		if len(fields) < 3 {
			return "", true, fmt.Errorf("task id is required")
		}
		followUp := ""
		if len(fields) > 3 {
			start := 3
			if fields[3] == "--" {
				start = 4
			}
			if start < len(fields) {
				followUp = strings.Join(fields[start:], " ")
			}
		}
		task, err := service.subagents.Resume(fields[2], followUp)
		if err != nil {
			return "", true, err
		}
		return "subagent resumed: " + task.ID, true, nil
	case "collect":
		if len(fields) < 3 {
			return "", true, fmt.Errorf("at least one task id is required")
		}
		var blocks []string
		for _, id := range fields[2:] {
			task, err := service.subagents.Status(id)
			if err != nil {
				return "", true, err
			}
			blocks = append(blocks, fmt.Sprintf("## %s [%s]\n%s", task.ID, task.Status, task.Result))
		}
		return strings.Join(blocks, "\n\n"), true, nil
	default:
		return "", true, fmt.Errorf("unknown /agent action %q", action)
	}
}

func (service *Service) handleMemoryCommand(message, runID string) (string, bool, error) {
	fields := strings.Fields(strings.TrimSpace(message))
	if len(fields) == 0 || strings.ToLower(fields[0]) != "/memory" {
		return "", false, nil
	}
	if service.memoryStore == nil {
		return "", true, fmt.Errorf("memory store is unavailable")
	}
	if len(fields) < 2 {
		return "", true, fmt.Errorf("usage: /memory list|search|confirm|compact")
	}
	encode := func(value any) (string, error) {
		content, err := json.MarshalIndent(value, "", "  ")
		return string(content), err
	}
	switch strings.ToLower(fields[1]) {
	case "list":
		entries, err := service.memoryStore.ListAll()
		if err != nil {
			return "", true, err
		}
		content, err := encode(entries)
		return content, true, err
	case "search":
		if len(fields) < 3 {
			return "", true, fmt.Errorf("search query is required")
		}
		results, err := service.memoryStore.Search(strings.Join(fields[2:], " "), nil, 12)
		if err != nil {
			return "", true, err
		}
		content, err := encode(results)
		return content, true, err
	case "confirm":
		if len(fields) < 3 {
			return "", true, fmt.Errorf("memory id is required")
		}
		entry, err := service.memoryStore.Confirm(fields[2], runID)
		if err != nil {
			return "", true, err
		}
		content, err := encode(entry)
		return content, true, err
	case "compact":
		report, err := service.memoryStore.Compact()
		if err != nil {
			return "", true, err
		}
		content, err := encode(report)
		return content, true, err
	default:
		return "", true, fmt.Errorf("unknown /memory action %q", fields[1])
	}
}

func (service *Service) handleSkillCommand(ctx context.Context, message string, recorder agent.MessageRecorder, logWriter io.Writer, progressWriter io.Writer, systemPrompt string, runRecorder *apptrace.Recorder) (string, bool, error) {
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
		APIType:         service.apiType,
		LogWriter:       logWriter,
		ToolDefinitions: service.toolDefinitions,
		Functions:       service.functions,
		Planner:         service.planner,
		Recorder:        recorder,
		WorkspaceRoot:   service.workspaceRoot,
		ProgressWriter:  progressWriter,
		Context:         service.context,
		Trace:           runRecorder,
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

func (service *Service) currentSystemPrompt(query string) (string, error) {
	if service == nil {
		return "", nil
	}
	prompt := service.systemPrompt
	if service.systemPromptBuilder != nil {
		built, err := service.systemPromptBuilder()
		if err != nil {
			return "", err
		}
		prompt = built
	}
	if service.memoryStore != nil {
		results, err := service.memoryStore.Search(query, nil, 12)
		entries, listErr := service.memoryStore.Active()
		if err == nil && listErr == nil {
			var memory strings.Builder
			memory.WriteString("\n\n# Relevant structured memory\n")
			seen := map[string]bool{}
			for _, entry := range entries {
				if entry.Kind != appmemory.KindDecision && entry.Kind != appmemory.KindUserPreference {
					continue
				}
				line := fmt.Sprintf("- [%s/%s] %s\n", entry.ID, entry.Kind, entry.Content)
				if memory.Len()+len(line) > 6000 {
					break
				}
				memory.WriteString(line)
				seen[entry.ID] = true
			}
			for _, result := range results {
				if seen[result.Entry.ID] {
					continue
				}
				line := fmt.Sprintf("- [%s/%s] %s\n", result.Entry.ID, result.Entry.Kind, result.Entry.Content)
				if memory.Len()+len(line) > 6000 {
					break
				}
				memory.WriteString(line)
			}
			prompt += memory.String()
		}
	}
	return agent.AppendModelIdentitySystemPrompt(prompt, service.model, service.apiType), nil
}

type RuntimeLLMInfo struct {
	APIType agent.APIType `json:"api_type"`
	Model   string        `json:"model"`
}

func (service *Service) RuntimeLLMInfo() RuntimeLLMInfo {
	if service == nil {
		return RuntimeLLMInfo{}
	}
	return RuntimeLLMInfo{APIType: service.apiType, Model: service.model}
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
