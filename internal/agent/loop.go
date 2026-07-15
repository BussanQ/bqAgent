package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"bqagent/internal/tools"
	apptrace "bqagent/internal/trace"
	"bqagent/internal/workspace"
)

const (
	DefaultModel        = "MiniMax-M2.5"
	DefaultSystemPrompt = "You are a helpful assistant. Be concise."
	// DefaultMaxIterations is the single canonical loop cap shared by every mode.
	// With auto-compaction the loop continues on a budget-bounded context, so this
	// is a runaway safety valve, not a task limit. Override with AGENT_MAX_ITERATIONS.
	DefaultMaxIterations                = 1000
	DefaultContextMaxInputTokens        = 24000
	DefaultContextResponseReserveTokens = 4000
	DefaultContextKeepLastTurns         = 6
	EarlierConversationSummaryPrefix    = "Summary of earlier conversation:\n"
	// maxParallelTools caps how many independent tool calls in one assistant turn
	// run concurrently.
	maxParallelTools = 8
)

type MessageRecorder interface {
	RecordMessage(message map[string]any) error
}

type ContextCheckpointRecorder interface {
	SaveCheckpointSummary(summary string, tailMessages []map[string]any, systemPrompt string) error
}

type ContextConfig struct {
	Enabled               bool
	MaxInputTokens        int
	TargetInputTokens     int
	ResponseReserveTokens int
	KeepLastTurns         int
	SummarizationEnabled  bool
	SummaryTriggerTokens  int
	SummaryModel          string
}

// StageConfig bounds one interactive exploration stage without changing the
// higher global runaway limit used by CLI runs. When a budget or loop guard is
// reached, the agent produces and persists a checkpoint summary so the next
// user turn can continue from the same session.
type StageConfig struct {
	MaxIterations        int
	Timeout              time.Duration
	LoopProtection       bool
	ImmediateProgress    bool
	EmitProgress         bool
	DuplicateCallLimit   int
	RepeatedFailureLimit int
	EmptyAssistantLimit  int
}

type Options struct {
	SystemPrompt    string
	APIType         APIType
	LogWriter       io.Writer
	ToolDefinitions []tools.Definition
	Functions       map[string]tools.Function
	Planner         *Planner
	Recorder        MessageRecorder
	Stream          bool
	WorkspaceRoot   string
	ProgressWriter  io.Writer
	TokenSink       io.Writer
	Context         ContextConfig
	Stage           StageConfig
	Trace           *apptrace.Recorder
}

type Agent struct {
	client          ChatCompletionClient
	model           string
	logWriter       io.Writer
	systemPrompt    string
	toolDefinitions []tools.Definition
	functions       map[string]tools.Function
	planner         *Planner
	recorder        MessageRecorder
	checkpointSaver ContextCheckpointRecorder
	stream          bool
	workspaceRoot   string
	progressWriter  io.Writer
	tokenSink       io.Writer
	contextConfig   ContextConfig
	stageConfig     StageConfig
	trace           *apptrace.Recorder
}

func New(client ChatCompletionClient, model string, logWriter io.Writer) *Agent {
	return NewWithOptions(client, model, Options{LogWriter: logWriter})
}

func NewWithOptions(client ChatCompletionClient, model string, options Options) *Agent {
	model = EffectiveModel(model)

	logWriter := synchronizeLogWriter(options.LogWriter)
	progressWriter := synchronizeLogWriter(options.ProgressWriter)
	tokenSink := synchronizeLogWriter(options.TokenSink)
	client = instrumentChatCompletionClient(client, logWriter, progressWriter)

	systemPrompt := AppendModelIdentitySystemPrompt(options.SystemPrompt, model, options.APIType)

	definitions := options.ToolDefinitions
	if definitions == nil {
		definitions = tools.Definitions()
	} else {
		definitions = cloneDefinitions(definitions)
	}

	functions := options.Functions
	if functions == nil {
		functions = tools.Registry()
	} else {
		functions = cloneFunctionMap(functions)
	}

	contextConfig := options.Context
	contextConfig = normalizeContextConfig(contextConfig)

	var checkpointSaver ContextCheckpointRecorder
	if saver, ok := options.Recorder.(ContextCheckpointRecorder); ok {
		checkpointSaver = saver
	}

	return &Agent{
		client:          client,
		model:           model,
		logWriter:       logWriter,
		progressWriter:  progressWriter,
		systemPrompt:    systemPrompt,
		toolDefinitions: definitions,
		functions:       functions,
		planner:         clonePlannerWithClient(options.Planner, client),
		recorder:        options.Recorder,
		checkpointSaver: checkpointSaver,
		stream:          options.Stream,
		workspaceRoot:   options.WorkspaceRoot,
		tokenSink:       tokenSink,
		contextConfig:   contextConfig,
		stageConfig:     normalizeStageConfig(options.Stage),
		trace:           options.Trace,
	}
}

func normalizeStageConfig(config StageConfig) StageConfig {
	if config.DuplicateCallLimit <= 0 {
		config.DuplicateCallLimit = 4
	}
	if config.RepeatedFailureLimit <= 0 {
		config.RepeatedFailureLimit = 3
	}
	if config.EmptyAssistantLimit <= 0 {
		config.EmptyAssistantLimit = 8
	}
	return config
}

func (a *Agent) Run(ctx context.Context, userMessage string, maxIterations int) (string, error) {
	messages := []map[string]any{
		{"role": "system", "content": a.systemPrompt},
		{"role": "user", "content": userMessage},
	}
	if err := a.recordMessages(messages...); err != nil {
		return "", err
	}
	return a.RunConversation(ctx, messages, maxIterations)
}

func (a *Agent) RunConversation(ctx context.Context, messages []map[string]any, maxIterations int) (string, error) {
	result, _, err := a.runConversation(ctx, duplicateMessages(messages), maxIterations, a.planner != nil)
	return result, err
}

func (a *Agent) RunConversationTurn(ctx context.Context, messages []map[string]any, maxIterations int) (string, []map[string]any, error) {
	return a.runConversation(ctx, duplicateMessages(messages), maxIterations, a.planner != nil)
}

func DefaultContextConfig() ContextConfig {
	return ContextConfig{
		Enabled:               true,
		MaxInputTokens:        DefaultContextMaxInputTokens,
		ResponseReserveTokens: DefaultContextResponseReserveTokens,
		TargetInputTokens:     DefaultContextMaxInputTokens - DefaultContextResponseReserveTokens,
		KeepLastTurns:         DefaultContextKeepLastTurns,
		SummarizationEnabled:  true,
		SummaryTriggerTokens:  DefaultContextMaxInputTokens - DefaultContextResponseReserveTokens,
	}
}

// BoundWorkingMessages creates a request-safe working snapshot without calling
// the model. Server paths use it for turns handled by external agents or command
// shortcuts that do not pass through runConversation's context manager.
func BoundWorkingMessages(messages []map[string]any, config ContextConfig) []map[string]any {
	config = normalizeContextConfig(config)
	working := sanitizeCompletedToolHistory(duplicateMessages(messages))
	if !config.Enabled || estimateMessagesTokens(working) <= config.TargetInputTokens {
		return working
	}
	working = pruneMessagesToBudget(working, config)
	return hardPruneMessagesToBudget(working, config.TargetInputTokens)
}

func normalizeContextConfig(config ContextConfig) ContextConfig {
	if config.MaxInputTokens <= 0 {
		config.MaxInputTokens = DefaultContextMaxInputTokens
	}
	if config.ResponseReserveTokens < 0 {
		config.ResponseReserveTokens = 0
	}
	if config.ResponseReserveTokens >= config.MaxInputTokens {
		config.ResponseReserveTokens = config.MaxInputTokens / 4
	}
	if config.TargetInputTokens <= 0 || config.TargetInputTokens >= config.MaxInputTokens {
		config.TargetInputTokens = config.MaxInputTokens - config.ResponseReserveTokens
	}
	if config.TargetInputTokens <= 0 {
		config.TargetInputTokens = config.MaxInputTokens
	}
	if config.KeepLastTurns < 0 {
		config.KeepLastTurns = DefaultContextKeepLastTurns
	}
	if config.SummaryTriggerTokens <= 0 {
		config.SummaryTriggerTokens = config.TargetInputTokens
	}
	return config
}

func (a *Agent) RunSkill(ctx context.Context, skillID, args string, maxIterations int) (string, error) {
	messages, err := a.executeSkillTool(ctx, nil, ToolCall{ID: "skill-direct-1", Function: FunctionCall{Name: "run_skill"}}, map[string]any{"skill": skillID, "args": args}, maxIterations)
	if err != nil {
		return "", err
	}
	if len(messages) == 0 {
		return "", nil
	}
	content, _ := messages[len(messages)-1]["content"].(string)
	return content, nil
}

func (a *Agent) RunPlanned(ctx context.Context, task string, maxIterations int) (string, error) {
	messages := []map[string]any{{"role": "system", "content": a.systemPrompt}}
	if err := a.recordMessages(messages...); err != nil {
		return "", err
	}
	return a.RunPlannedConversation(ctx, messages, task, maxIterations)
}

func (a *Agent) RunPlannedConversation(ctx context.Context, messages []map[string]any, task string, maxIterations int) (string, error) {
	result, _, err := a.RunPlannedConversationTurn(ctx, messages, task, maxIterations)
	return result, err
}

func (a *Agent) RunPlannedConversationTurn(ctx context.Context, messages []map[string]any, task string, maxIterations int) (string, []map[string]any, error) {
	return a.runPlannedConversation(ctx, duplicateMessages(messages), task, maxIterations)
}

func (a *Agent) runPlannedConversation(ctx context.Context, messages []map[string]any, task string, maxIterations int) (string, []map[string]any, error) {
	if a.planner == nil {
		return "", messages, fmt.Errorf("planner is not configured")
	}

	a.logf("[Plan] Breaking down: %s\n", task)
	steps, err := a.planner.Generate(ctx, task)
	if err != nil {
		return "", messages, err
	}
	if len(steps) == 0 {
		return "", messages, fmt.Errorf("planner returned no steps")
	}

	a.logf("[Plan] Created %d steps\n", len(steps))
	results := make([]string, 0, len(steps))
	for index, step := range steps {
		a.logf("[Plan] %d. %s\n", index+1, step)
		userMessage := map[string]any{"role": "user", "content": step}
		messages = append(messages, userMessage)
		if err := a.recordMessages(userMessage); err != nil {
			return "", messages, err
		}

		stepResult, updatedMessages, err := a.runConversation(ctx, messages, maxIterations, false)
		if err != nil {
			return "", updatedMessages, err
		}
		messages = updatedMessages
		results = append(results, stepResult)
	}

	return strings.Join(results, "\n"), messages, nil
}

func (a *Agent) runConversation(ctx context.Context, messages []map[string]any, maxIterations int, allowPlan bool) (result string, updatedMessages []map[string]any, err error) {
	messages = ensureSystemPromptMessage(messages, a.systemPrompt)
	startedAt := time.Now()
	updatedMessages = messages
	if maxIterations <= 0 {
		maxIterations = DefaultMaxIterations
	}

	definitions := a.toolDefinitionsForRun(allowPlan)
	actualIterations := 0
	explorationCtx := ctx
	cancelExploration := func() {}
	if a.stageConfig.Timeout > 0 {
		explorationCtx, cancelExploration = context.WithTimeout(ctx, a.stageConfig.Timeout)
	}
	defer cancelExploration()
	loopGuard := newLoopGuard(a.stageConfig)
	defer func() {
		logTurnTiming(a.logWriter, actualIterations, allowPlan, time.Since(startedAt), err)
	}()

	for iteration := 0; iteration < maxIterations; iteration++ {
		if reason := a.stageBoundaryReason(iteration, explorationCtx); reason != "" {
			return a.finishStageCheckpoint(ctx, updatedMessages, reason, actualIterations)
		}
		actualIterations = iteration + 1
		if a.stageConfig.ImmediateProgress {
			a.writeStageProgress(fmt.Sprintf("Starting analysis iteration %d", actualIterations))
		}
		requestMessages, compacted := a.buildRequestMessages(explorationCtx, messages)
		if compacted != nil {
			messages = compacted
			updatedMessages = compacted
		}
		var (
			message    AssistantMessage
			requestErr error
		)
		modelStartedAt := time.Now()
		if a.stream {
			message, requestErr = a.client.CreateChatCompletionStream(explorationCtx, a.model, requestMessages, definitions, func(chunk string) {
				sink := a.tokenSink
				if sink == nil {
					sink = a.logWriter
				}
				if sink != nil {
					_, _ = io.WriteString(sink, chunk)
				}
			})
		} else {
			message, requestErr = a.client.CreateChatCompletion(explorationCtx, a.model, requestMessages, definitions)
		}
		if a.trace != nil {
			usage := message.Usage
			if usage.TotalTokens == 0 {
				usage.PromptTokens = estimateMessagesTokens(requestMessages)
				usage.CompletionTokens = maxInt(1, len(message.DisplayContent())/4)
				usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
				usage.Estimated = true
			}
			a.trace.ModelCall(apptrace.HashJSON(requestMessages), apptrace.TokenUsage{
				PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
				TotalTokens: usage.TotalTokens, Estimated: usage.Estimated,
			}, time.Since(modelStartedAt), requestErr)
		}
		if requestErr != nil {
			if errors.Is(requestErr, context.DeadlineExceeded) && ctx.Err() == nil && a.stageConfig.Timeout > 0 {
				return a.finishStageCheckpoint(ctx, updatedMessages, "stage time budget reached", actualIterations)
			}
			err = requestErr
			return "", updatedMessages, err
		}
		message.normalizeInlineToolCalls()

		requestMessage := message.RequestMessage()
		updatedMessages = append(updatedMessages, requestMessage)
		if err := a.recordMessages(requestMessage); err != nil {
			return "", updatedMessages, err
		}
		messages = updatedMessages
		if reason := loopGuard.observeAssistant(message); reason != "" {
			return a.finishStageCheckpoint(ctx, updatedMessages, reason, actualIterations)
		}

		if a.stream && len(message.ToolCalls) == 0 {
			// content already streamed via onChunk; skip log line
		} else {
			a.logf("[Agent] %s\n", message.DisplayContent())
		}
		if len(message.ToolCalls) == 0 {
			return message.FinalContent(), updatedMessages, nil
		}
		if !a.stageConfig.ImmediateProgress {
			a.writeStageProgress(fmt.Sprintf("Analyzing tool requests in iteration %d", actualIterations))
		}

		// Some tools recurse or mutate state that later tool calls may depend on, so
		// a turn containing them runs sequentially. A turn of only independent tools
		// runs concurrently and appends results in the original order.
		if a.hasSpecialToolCalls(message.ToolCalls, allowPlan) {
			for _, toolCall := range message.ToolCalls {
				parsedArguments, err := parseArguments(toolCall.Function.Arguments)
				if err != nil {
					toolMessage, recordErr := a.appendToolError(updatedMessages, toolCall.ID, "Invalid JSON arguments for tool %q: %v", toolCall.Function.Name, err)
					updatedMessages = toolMessage
					if recordErr != nil {
						return "", updatedMessages, recordErr
					}
					messages = updatedMessages
					continue
				}
				a.logf("[Tool] %s(%v)\n", toolCall.Function.Name, parsedArguments)

				arguments, ok := parsedArguments.(map[string]any)
				if !ok {
					toolMessage, recordErr := a.appendToolError(updatedMessages, toolCall.ID, "Tool arguments for %q must decode to a JSON object", toolCall.Function.Name)
					updatedMessages = toolMessage
					if recordErr != nil {
						return "", updatedMessages, recordErr
					}
					messages = updatedMessages
					continue
				}

				if toolCall.Function.Name == "plan" && allowPlan && a.planner != nil {
					result, updatedPlanMessages, planErr := a.executePlanTool(ctx, updatedMessages, toolCall, arguments, maxIterations)
					updatedMessages = updatedPlanMessages
					messages = updatedMessages
					if planErr != nil {
						return "", updatedMessages, planErr
					}
					if result != "" {
						return result, updatedMessages, nil
					}
					continue
				}

				if toolCall.Function.Name == "run_skill" {
					updatedSkillMessages, skillErr := a.executeSkillTool(ctx, updatedMessages, toolCall, arguments, maxIterations)
					updatedMessages = updatedSkillMessages
					messages = updatedMessages
					if skillErr != nil {
						return "", updatedMessages, skillErr
					}
					continue
				}

				_, toolResult := a.runRegularToolCall(explorationCtx, toolCall)
				updatedToolMessages, recordErr := a.appendToolMessage(updatedMessages, toolCall.ID, toolResult)
				updatedMessages = updatedToolMessages
				messages = updatedMessages
				if recordErr != nil {
					return "", updatedMessages, recordErr
				}
				if reason := loopGuard.observeTool(toolCall, toolResult); reason != "" {
					return a.finishStageCheckpoint(ctx, updatedMessages, reason, actualIterations)
				}
			}
		} else {
			results := make([]string, len(message.ToolCalls))
			semaphore := make(chan struct{}, maxParallelTools)
			var waitGroup sync.WaitGroup
			for index, toolCall := range message.ToolCalls {
				waitGroup.Add(1)
				go func(index int, toolCall ToolCall) {
					defer waitGroup.Done()
					semaphore <- struct{}{}
					defer func() { <-semaphore }()
					_, content := a.runRegularToolCall(explorationCtx, toolCall)
					results[index] = content
				}(index, toolCall)
			}
			waitGroup.Wait()

			loopReason := ""
			for index, toolCall := range message.ToolCalls {
				updatedToolMessages, recordErr := a.appendToolMessage(updatedMessages, toolCall.ID, results[index])
				updatedMessages = updatedToolMessages
				messages = updatedMessages
				if recordErr != nil {
					return "", updatedMessages, recordErr
				}
				if reason := loopGuard.observeTool(toolCall, results[index]); reason != "" && loopReason == "" {
					loopReason = reason
				}
			}
			if loopReason != "" {
				return a.finishStageCheckpoint(ctx, updatedMessages, loopReason, actualIterations)
			}
		}
		a.writeStageProgress(fmt.Sprintf("Completed analysis iteration %d", actualIterations))
	}

	if a.stageConfig.MaxIterations > 0 || a.stageConfig.Timeout > 0 || a.stageConfig.LoopProtection {
		return a.finishStageCheckpoint(ctx, updatedMessages, fmt.Sprintf("maximum turn iterations reached (%d)", maxIterations), actualIterations)
	}
	return fmt.Sprintf("Agent stopped: reached maximum of %d iterations without completing.", maxIterations), updatedMessages, nil
}

func ensureSystemPromptMessage(messages []map[string]any, systemPrompt string) []map[string]any {
	systemMessage := map[string]any{"role": "system", "content": systemPrompt}
	if len(messages) == 0 {
		return []map[string]any{systemMessage}
	}
	role, _ := messages[0]["role"].(string)
	if role == "system" {
		messages[0] = systemMessage
		return messages
	}
	return append([]map[string]any{systemMessage}, messages...)
}

func (a *Agent) stageBoundaryReason(iteration int, explorationCtx context.Context) string {
	if a.stageConfig.MaxIterations > 0 && iteration >= a.stageConfig.MaxIterations {
		return fmt.Sprintf("stage iteration budget reached (%d)", a.stageConfig.MaxIterations)
	}
	if explorationCtx.Err() != nil && a.stageConfig.Timeout > 0 {
		return "stage time budget reached"
	}
	return ""
}

func (a *Agent) finishStageCheckpoint(ctx context.Context, messages []map[string]any, reason string, iterations int) (string, []map[string]any, error) {
	a.writeStageProgress(fmt.Sprintf("Preparing stage summary after %d iterations", iterations))
	request, compacted := a.buildRequestMessages(ctx, messages)
	if compacted != nil {
		messages = compacted
	}
	request = append(request, map[string]any{
		"role":    "user",
		"content": fmt.Sprintf("The current interactive analysis stage must stop now because %s. Based only on the work and tool results above, provide a concise checkpoint with exactly these sections: 已发现, 未完成, 建议下一步. State that the user can reply ‘继续’ to resume from this session. Do not call tools.", reason),
	})
	message, summaryErr := a.client.CreateChatCompletion(ctx, a.model, request, nil)
	summary := strings.TrimSpace(message.FinalContent())
	if summaryErr != nil || summary == "" {
		summary = fmt.Sprintf("阶段已暂停（%s）。\n\n已发现\n- 已完成 %d 轮探索，相关工具结果已保留在当前会话中。\n\n未完成\n- 仍需基于现有结果继续分析。\n\n建议下一步\n- 回复“继续”，我会沿用当前 session 和已保存的上下文继续。", reason, iterations)
	}
	checkpoint := map[string]any{"role": "assistant", "content": summary}
	messages = append(messages, checkpoint)
	if recordErr := a.recordMessages(checkpoint); recordErr != nil {
		return "", messages, recordErr
	}
	a.writeStageProgress("Stage summary completed; waiting for confirmation to continue")
	return summary, messages, nil
}

type loopGuard struct {
	config          StageConfig
	callCounts      map[string]int
	failureCounts   map[string]int
	pathFailures    map[string]int
	emptyAssistants int
}

func newLoopGuard(config StageConfig) *loopGuard {
	return &loopGuard{config: config, callCounts: map[string]int{}, failureCounts: map[string]int{}, pathFailures: map[string]int{}}
}

func (guard *loopGuard) observeAssistant(message AssistantMessage) string {
	if guard == nil || !guard.config.LoopProtection {
		return ""
	}
	if strings.TrimSpace(message.FinalContent()) == "" && len(message.ToolCalls) > 0 {
		guard.emptyAssistants++
	} else {
		guard.emptyAssistants = 0
	}
	if guard.emptyAssistants >= guard.config.EmptyAssistantLimit {
		return fmt.Sprintf("loop protection: %d consecutive empty assistant tool rounds", guard.emptyAssistants)
	}
	return ""
}

func (guard *loopGuard) observeTool(call ToolCall, result string) string {
	if guard == nil || !guard.config.LoopProtection {
		return ""
	}
	signature := call.Function.Name + "\x00" + strings.TrimSpace(call.Function.Arguments)
	guard.callCounts[signature]++
	if strings.HasPrefix(strings.TrimSpace(result), "Error:") {
		guard.failureCounts[signature]++
		if guard.failureCounts[signature] >= guard.config.RepeatedFailureLimit {
			return fmt.Sprintf("loop protection: repeated failing tool call %s", call.Function.Name)
		}
		if pathSignature := failedPathSignature(call); pathSignature != "" {
			guard.pathFailures[pathSignature]++
			if guard.pathFailures[pathSignature] >= guard.config.RepeatedFailureLimit {
				return fmt.Sprintf("loop protection: repeated failing path in %s", call.Function.Name)
			}
		}
	}
	if guard.callCounts[signature] >= guard.config.DuplicateCallLimit {
		return fmt.Sprintf("loop protection: repeated tool call %s", call.Function.Name)
	}
	return ""
}

func failedPathSignature(call ToolCall) string {
	parsed, err := parseArguments(call.Function.Arguments)
	if err != nil {
		return ""
	}
	arguments, ok := parsed.(map[string]any)
	if !ok {
		return ""
	}
	path, _ := arguments["path"].(string)
	path = strings.ToLower(filepath.ToSlash(strings.TrimSpace(path)))
	if path == "" {
		return ""
	}
	return call.Function.Name + "\x00" + path
}

func (a *Agent) executePlanTool(ctx context.Context, messages []map[string]any, toolCall ToolCall, arguments map[string]any, maxIterations int) (result string, updated []map[string]any, err error) {
	startedAt := time.Now()
	defer func() {
		if a.trace != nil {
			a.trace.ToolCall("plan", arguments, result, time.Since(startedAt), err)
		}
	}()
	task, err := requireStringArgument("plan", arguments, "task")
	if err != nil {
		updatedMessages, recordErr := a.appendToolError(messages, toolCall.ID, "%v", err)
		return "", updatedMessages, recordErr
	}

	a.logf("[Plan] Breaking down: %s\n", task)
	steps, err := a.planner.Generate(ctx, task)
	if err != nil {
		updatedMessages, recordErr := a.appendToolError(messages, toolCall.ID, "plan generation failed: %v", err)
		return "", updatedMessages, recordErr
	}
	if len(steps) == 0 {
		updatedMessages, recordErr := a.appendToolError(messages, toolCall.ID, "planner returned no steps for this task")
		return "", updatedMessages, recordErr
	}

	a.logf("[Plan] Created %d steps\n", len(steps))
	toolMessage := map[string]any{
		"role":         "tool",
		"tool_call_id": toolCall.ID,
		"content":      fmt.Sprintf("Plan created with %d steps. Executing now...", len(steps)),
	}
	messages = append(messages, toolMessage)
	if err := a.recordMessages(toolMessage); err != nil {
		return "", messages, err
	}

	results := make([]string, 0, len(steps))
	for index, step := range steps {
		a.logf("[Plan] %d. %s\n", index+1, step)
		userMessage := map[string]any{"role": "user", "content": step}
		messages = append(messages, userMessage)
		if err := a.recordMessages(userMessage); err != nil {
			return "", messages, err
		}

		stepResult, updatedMessages, err := a.runConversation(ctx, messages, maxIterations, false)
		if err != nil {
			return "", updatedMessages, err
		}
		messages = updatedMessages
		results = append(results, stepResult)
	}

	return strings.Join(results, "\n"), messages, nil
}

func (a *Agent) executeSkillTool(ctx context.Context, messages []map[string]any, toolCall ToolCall, arguments map[string]any, maxIterations int) (updated []map[string]any, err error) {
	startedAt := time.Now()
	defer func() {
		if a.trace != nil {
			result := ""
			if len(updated) > 0 {
				result, _ = updated[len(updated)-1]["content"].(string)
			}
			a.trace.ToolCall("run_skill", arguments, result, time.Since(startedAt), err)
		}
	}()
	skillID, err := requireStringArgument("run_skill", arguments, "skill")
	if err != nil {
		return a.appendToolError(messages, toolCall.ID, "%v", err)
	}
	if strings.TrimSpace(a.workspaceRoot) == "" {
		return a.appendToolError(messages, toolCall.ID, "workspace root is not configured for run_skill")
	}

	ws := &workspace.Workspace{Root: a.workspaceRoot}
	skill, err := ws.LoadSkill(skillID)
	if err != nil {
		return a.appendToolError(messages, toolCall.ID, "%v", err)
	}

	argsText := ""
	if rawArgs, ok := arguments["args"]; ok {
		text, ok := rawArgs.(string)
		if !ok {
			return a.appendToolError(messages, toolCall.ID, "tool %q argument %q must be a string", "run_skill", "args")
		}
		argsText = strings.TrimSpace(text)
	}

	toolMessage := map[string]any{
		"role":         "tool",
		"tool_call_id": toolCall.ID,
		"content":      fmt.Sprintf("Running skill %q...", skill.ID),
	}
	messages = append(messages, toolMessage)
	if err := a.recordMessages(toolMessage); err != nil {
		return messages, err
	}

	skillTask := buildSkillTask(skill, argsText)
	child := &Agent{
		client:          a.client,
		model:           a.model,
		logWriter:       a.logWriter,
		progressWriter:  a.progressWriter,
		systemPrompt:    a.systemPrompt,
		toolDefinitions: a.toolDefinitionsForSkillRun(),
		functions:       cloneFunctionMap(a.functions),
		planner:         nil,
		recorder:        a.recorder,
		stream:          false,
		workspaceRoot:   a.workspaceRoot,
		contextConfig:   a.contextConfig,
		trace:           a.trace,
	}
	skillResult, _, err := child.runConversation(ctx, []map[string]any{{"role": "system", "content": a.systemPrompt}, {"role": "user", "content": skillTask}}, maxIterations, false)
	if err != nil {
		return messages, err
	}

	messages[len(messages)-1]["content"] = skillResult
	return messages, nil
}

// buildRequestMessages returns the message payload to send to the model and,
// when pruning or summarization compacted the history, a non-nil bounded working
// set the loop should adopt in place of its full in-memory history. This working
// set can be persisted separately from the complete raw transcript. The
// synthetic summary lives only in the working set and context checkpoint; it is
// never recorded to messages.jsonl.
func (a *Agent) buildRequestMessages(ctx context.Context, messages []map[string]any) (request []map[string]any, compacted []map[string]any) {
	sanitized := sanitizeCompletedToolHistory(duplicateMessages(messages))
	if !a.contextConfig.Enabled {
		return sanitized, nil
	}

	estimatedTokens := estimateMessagesTokens(sanitized)
	if estimatedTokens <= a.contextConfig.TargetInputTokens {
		a.logContextBudget(len(messages), len(sanitized), len(sanitized), estimatedTokens, false, false)
		return sanitized, nil
	}

	pruned := pruneMessagesToBudget(sanitized, a.contextConfig)
	pruned = hardPruneMessagesToBudget(pruned, a.contextConfig.TargetInputTokens)
	prunedTokens := estimateMessagesTokens(pruned)
	if !a.contextConfig.SummarizationEnabled || !shouldSummarize(estimatedTokens, a.contextConfig) {
		a.logContextBudget(len(messages), len(sanitized), len(pruned), prunedTokens, true, false)
		return pruned, pruned
	}

	summarized, ok := a.summarizeMessages(ctx, sanitized)
	if !ok {
		a.logContextBudget(len(messages), len(sanitized), len(pruned), prunedTokens, true, false)
		return pruned, pruned
	}
	summaryTokens := estimateMessagesTokens(summarized)
	if summaryTokens > a.contextConfig.TargetInputTokens {
		summarized = hardPruneMessagesToBudget(summarized, a.contextConfig.TargetInputTokens)
		summaryTokens = estimateMessagesTokens(summarized)
	}
	a.logContextBudget(len(messages), len(sanitized), len(summarized), summaryTokens, true, true)
	return summarized, summarized
}

func pruneMessagesToBudget(messages []map[string]any, config ContextConfig) []map[string]any {
	if len(messages) <= 1 {
		return messages
	}

	systemEnd := 0
	if role, _ := messages[0]["role"].(string); role == "system" {
		systemEnd = 1
	}
	if systemEnd >= len(messages) {
		return messages
	}

	start := safeTailStart(messages, config.KeepLastTurns)
	if start < systemEnd {
		start = systemEnd
	}

	pruned := append([]map[string]any{}, messages[:systemEnd]...)
	pruned = append(pruned, messages[start:]...)
	return pruned
}

// hardPruneMessagesToBudget is the final request-size guard. Turn-based pruning
// can still exceed the token target when one recent turn contains many or very
// large tool results. This keeps a contiguous, valid tail and truncates the
// newest group only when that group alone would exceed the remaining budget.
func hardPruneMessagesToBudget(messages []map[string]any, targetTokens int) []map[string]any {
	if targetTokens <= 0 || len(messages) == 0 || estimateMessagesTokens(messages) <= targetTokens {
		return messages
	}

	protectedEnd := 0
	result := make([]map[string]any, 0, len(messages))
	remaining := targetTokens
	latestUserIndex := -1
	for index := len(messages) - 1; index >= 0; index-- {
		if role, _ := messages[index]["role"].(string); role == "user" {
			latestUserIndex = index
			break
		}
	}
	var reservedUser []map[string]any
	reservedUserTokens := 0
	if latestUserIndex >= 0 {
		userBudget := targetTokens * 3 / 4
		if userBudget < 1 {
			userBudget = 1
		}
		reservedUser = truncateMessageGroupToBudget(messages[latestUserIndex:latestUserIndex+1], userBudget)
		reservedUserTokens = estimateMessagesTokens(reservedUser)
	}
	if role, _ := messages[0]["role"].(string); role == "system" {
		result = append(result, messages[0])
		remaining -= estimateMessagesTokens(messages[:1])
		protectedEnd = 1
	}
	if protectedEnd < len(messages) {
		if content, _ := messages[protectedEnd]["content"].(string); strings.HasPrefix(content, EarlierConversationSummaryPrefix) {
			summaryBudget := remaining - reservedUserTokens
			if summaryBudget > 0 {
				clippedSummary := truncateMessageGroupToBudget(messages[protectedEnd:protectedEnd+1], summaryBudget)
				result = append(result, clippedSummary...)
				remaining -= estimateMessagesTokens(clippedSummary)
			}
			protectedEnd++
		}
	}
	if remaining <= 0 || protectedEnd >= len(messages) {
		return result
	}

	groups := messageTailGroups(messages, protectedEnd)
	selected := make([]messageGroup, 0, len(groups))
	latestUserGroup := -1
	for index, group := range groups {
		if group.start == latestUserIndex {
			latestUserGroup = index
			break
		}
	}
	for index := len(groups) - 1; index >= 0; index-- {
		group := groups[index]
		groupMessages := group.messages
		if index == latestUserGroup && len(reservedUser) > 0 {
			groupMessages = reservedUser
		}
		available := remaining
		if latestUserGroup >= 0 && index > latestUserGroup {
			available -= reservedUserTokens
		}
		if available <= 0 {
			continue
		}
		cost := estimateMessagesTokens(groupMessages)
		if cost > available {
			if index > latestUserGroup || (latestUserGroup < 0 && len(selected) == 0) {
				clipped := truncateMessageGroupToBudget(groupMessages, available)
				if len(clipped) > 0 {
					selected = append(selected, messageGroup{start: group.start, messages: clipped})
					remaining -= estimateMessagesTokens(clipped)
				}
				continue
			}
			break
		}
		selected = append(selected, messageGroup{start: group.start, messages: groupMessages})
		remaining -= cost
	}
	for index := len(selected) - 1; index >= 0; index-- {
		result = append(result, selected[index].messages...)
	}
	return result
}

type messageGroup struct {
	start    int
	messages []map[string]any
}

func messageTailGroups(messages []map[string]any, start int) []messageGroup {
	groups := make([]messageGroup, 0, len(messages)-start)
	for index := start; index < len(messages); {
		end := index + 1
		role, _ := messages[index]["role"].(string)
		if role == "assistant" && len(extractToolCallsFromMessageMap(messages[index])) > 0 {
			for end < len(messages) {
				nextRole, _ := messages[end]["role"].(string)
				if nextRole != "tool" {
					break
				}
				end++
			}
		}
		groups = append(groups, messageGroup{start: index, messages: messages[index:end]})
		index = end
	}
	return groups
}

func truncateMessageGroupToBudget(group []map[string]any, budgetTokens int) []map[string]any {
	cloned := duplicateMessages(group)
	if budgetTokens <= 0 {
		return nil
	}
	for estimateMessagesTokens(cloned) > budgetTokens {
		largestIndex := -1
		largestContent := ""
		for index, message := range cloned {
			content, _ := message["content"].(string)
			if len(content) > len(largestContent) {
				largestIndex = index
				largestContent = content
			}
		}
		if largestIndex < 0 || len(largestContent) <= 16 {
			break
		}
		maxChars := len(largestContent) / 2
		if maxChars < 16 {
			maxChars = 16
		}
		cloned[largestIndex]["content"] = truncateTextMiddle(largestContent, maxChars)
	}
	return cloned
}

func truncateTextMiddle(text string, maxChars int) string {
	if maxChars <= 0 || len(text) <= maxChars {
		return text
	}
	marker := "\n... [content truncated to fit context budget] ...\n"
	if maxChars <= len(marker)+2 {
		return text[:maxChars]
	}
	available := maxChars - len(marker)
	head := available * 2 / 3
	tail := available - head
	return text[:head] + marker + text[len(text)-tail:]
}

func shouldSummarize(estimatedTokens int, config ContextConfig) bool {
	return config.SummarizationEnabled && estimatedTokens > config.SummaryTriggerTokens
}

func (a *Agent) summarizeMessages(ctx context.Context, messages []map[string]any) ([]map[string]any, bool) {
	prefix, tail, ok := splitMessagesForSummary(messages, a.contextConfig.KeepLastTurns)
	if !ok {
		return nil, false
	}
	summary, err := a.generateSummary(ctx, prefix)
	if err != nil || strings.TrimSpace(summary) == "" {
		return nil, false
	}

	summarized := make([]map[string]any, 0, len(tail)+2)
	if len(prefix) > 0 {
		if role, _ := prefix[0]["role"].(string); role == "system" {
			summarized = append(summarized, prefix[0])
		}
	}
	summarized = append(summarized, map[string]any{
		"role":    "assistant",
		"content": EarlierConversationSummaryPrefix + summary,
	})
	summarized = append(summarized, tail...)
	summarized = hardPruneMessagesToBudget(summarized, a.contextConfig.TargetInputTokens)
	if a.checkpointSaver != nil {
		checkpointTail := summarized
		if len(checkpointTail) > 0 {
			if role, _ := checkpointTail[0]["role"].(string); role == "system" {
				checkpointTail = checkpointTail[1:]
			}
		}
		if len(checkpointTail) > 0 {
			if content, _ := checkpointTail[0]["content"].(string); strings.HasPrefix(content, EarlierConversationSummaryPrefix) {
				checkpointTail = checkpointTail[1:]
			}
		}
		if err := a.checkpointSaver.SaveCheckpointSummary(summary, checkpointTail, a.systemPrompt); err != nil {
			a.logf("[Context] checkpoint save failed: %v\n", err)
		}
	}
	return summarized, true
}

func splitMessagesForSummary(messages []map[string]any, keepLastTurns int) ([]map[string]any, []map[string]any, bool) {
	if len(messages) <= 2 {
		return nil, nil, false
	}
	start := safeTailStart(messages, keepLastTurns)
	systemEnd := 0
	if role, _ := messages[0]["role"].(string); role == "system" {
		systemEnd = 1
	}
	if start <= systemEnd || start >= len(messages) {
		return nil, nil, false
	}
	return messages[:start], messages[start:], true
}

func (a *Agent) generateSummary(ctx context.Context, messages []map[string]any) (string, error) {
	client, ok := a.client.(chatCompletionOptionsClient)
	if !ok {
		return "", fmt.Errorf("chat completion options are not supported")
	}

	model := strings.TrimSpace(a.contextConfig.SummaryModel)
	if model == "" {
		model = a.model
	}
	messages = hardPruneMessagesToBudget(messages, a.contextConfig.TargetInputTokens)
	promptMessages := []map[string]any{
		{"role": "system", "content": "Summarize the earlier conversation for future continuation. Preserve goals, constraints, decisions, unresolved questions, and important factual context. Be concise."},
		{"role": "user", "content": buildSummaryInput(messages)},
	}
	response, err := client.CreateChatCompletionWithOptions(ctx, model, promptMessages, nil, ChatCompletionOptions{})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(response.FinalContent()), nil
}

func buildSummaryInput(messages []map[string]any) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		role, _ := message["role"].(string)
		content, _ := message["content"].(string)
		content = strings.TrimSpace(content)
		if role == "" || content == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %s", role, content))
	}
	return strings.Join(parts, "\n")
}

func safeTailStart(messages []map[string]any, keepLastTurns int) int {
	turns := 0
	for i := len(messages) - 1; i >= 0; i-- {
		role, _ := messages[i]["role"].(string)
		if role == "user" {
			turns++
			if turns >= keepLastTurns {
				return i
			}
		}
	}
	return 0
}

func estimateMessagesTokens(messages []map[string]any) int {
	totalChars := 0
	for _, message := range messages {
		for _, key := range []string{"role", "content", "tool_call_id"} {
			if text, ok := message[key].(string); ok {
				totalChars += len(text)
			}
		}
		if toolCalls, ok := message["tool_calls"]; ok && toolCalls != nil {
			encoded, err := json.Marshal(toolCalls)
			if err == nil {
				totalChars += len(encoded)
			}
		}
	}
	if totalChars == 0 {
		return 0
	}
	return (totalChars + 3) / 4
}

func (a *Agent) logContextBudget(rawCount, sanitizedCount, requestCount, estimatedTokens int, pruned bool, summarized bool) {
	if a.logWriter == nil {
		return
	}
	fmt.Fprintf(a.logWriter, "[Context] raw_messages=%d sanitized_messages=%d request_messages=%d estimated_tokens=%d pruned=%t summarized=%t target_tokens=%d\n", rawCount, sanitizedCount, requestCount, estimatedTokens, pruned, summarized, a.contextConfig.TargetInputTokens)
}

func buildSkillTask(skill workspace.Skill, args string) string {
	parts := []string{
		fmt.Sprintf("Execute workspace skill %q.", skill.ID),
		fmt.Sprintf("Skill title: %s", skill.Title),
		"Follow the skill instructions below and complete the requested task using the available tools.",
		"Skill definition:",
		skill.Body,
	}
	if strings.TrimSpace(args) != "" {
		parts = append(parts, "Skill arguments:", args)
	}
	return strings.Join(parts, "\n\n")
}

func parseArguments(raw string) (any, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}

	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

func requireStringArgument(toolName string, args map[string]any, key string) (string, error) {
	value, ok := args[key]
	if !ok {
		return "", fmt.Errorf("tool %q missing required argument %q", toolName, key)
	}

	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("tool %q argument %q must be a string", toolName, key)
	}
	return text, nil
}

func (a *Agent) toolDefinitionsForRun(allowPlan bool) []tools.Definition {
	filtered := make([]tools.Definition, 0, len(a.toolDefinitions))
	for _, definition := range a.toolDefinitions {
		if definition.Function.Name == "plan" && (!allowPlan || a.planner == nil) {
			continue
		}
		filtered = append(filtered, definition)
	}
	return filtered
}

func (a *Agent) toolDefinitionsForSkillRun() []tools.Definition {
	filtered := make([]tools.Definition, 0, len(a.toolDefinitions))
	for _, definition := range a.toolDefinitions {
		if definition.Function.Name == "plan" || definition.Function.Name == "run_skill" {
			continue
		}
		filtered = append(filtered, definition)
	}
	return filtered
}

func (a *Agent) recordMessages(messages ...map[string]any) error {
	if a.recorder == nil {
		return nil
	}
	for _, message := range messages {
		if err := a.recorder.RecordMessage(message); err != nil {
			return err
		}
	}
	return nil
}

func cloneDefinitions(definitions []tools.Definition) []tools.Definition {
	cloned := make([]tools.Definition, len(definitions))
	copy(cloned, definitions)
	return cloned
}

func cloneFunctionMap(functions map[string]tools.Function) map[string]tools.Function {
	cloned := make(map[string]tools.Function, len(functions))
	for name, function := range functions {
		cloned[name] = function
	}
	return cloned
}

func clonePlannerWithClient(planner *Planner, client ChatCompletionClient) *Planner {
	if planner == nil {
		return nil
	}
	return &Planner{
		client: client,
		model:  planner.model,
	}
}

func duplicateMessages(messages []map[string]any) []map[string]any {
	cloned := make([]map[string]any, len(messages))
	for index, message := range messages {
		copyMessage := make(map[string]any, len(message))
		for key, value := range message {
			copyMessage[key] = value
		}
		cloned[index] = copyMessage
	}
	return cloned
}

func sanitizeCompletedToolHistory(messages []map[string]any) []map[string]any {
	if len(messages) == 0 {
		return messages
	}

	pendingAssistantIndex := -1
	pendingToolStart := len(messages)
	for i := len(messages) - 1; i >= 0; i-- {
		role, _ := messages[i]["role"].(string)
		if role == "tool" {
			pendingToolStart = i
			continue
		}
		if role == "assistant" && len(extractToolCallsFromMessageMap(messages[i])) > 0 && pendingToolStart == i+1 {
			pendingAssistantIndex = i
		}
		break
	}

	sanitized := make([]map[string]any, 0, len(messages))
	for i := 0; i < len(messages); i++ {
		message := messages[i]
		role, _ := message["role"].(string)
		if role == "tool" {
			if pendingAssistantIndex >= 0 && i >= pendingToolStart {
				sanitized = append(sanitized, message)
			} else {
				content, _ := message["content"].(string)
				sanitized = append(sanitized, map[string]any{"role": "assistant", "content": "Completed tool result:\n" + content})
			}
			continue
		}
		if role == "assistant" && len(extractToolCallsFromMessageMap(message)) > 0 {
			if i == pendingAssistantIndex {
				sanitized = append(sanitized, message)
				continue
			}
			end := i + 1
			for end < len(messages) {
				nextRole, _ := messages[end]["role"].(string)
				if nextRole != "tool" {
					break
				}
				end++
			}
			sanitized = append(sanitized, summarizeCompletedToolBatch(message, messages[i+1:end]))
			i = end - 1
			continue
		}
		sanitized = append(sanitized, message)
	}
	return sanitized
}

func summarizeCompletedToolBatch(assistant map[string]any, results []map[string]any) map[string]any {
	parts := []string{"Completed tool activity (retain this evidence for later reasoning):"}
	if content, _ := assistant["content"].(string); strings.TrimSpace(content) != "" {
		parts = append(parts, "Assistant note: "+strings.TrimSpace(content))
	}
	if calls, err := json.Marshal(assistant["tool_calls"]); err == nil {
		parts = append(parts, "Calls: "+string(calls))
	}
	for _, result := range results {
		id, _ := result["tool_call_id"].(string)
		content, _ := result["content"].(string)
		parts = append(parts, fmt.Sprintf("Result %s:\n%s", id, content))
	}
	return map[string]any{"role": "assistant", "content": strings.Join(parts, "\n")}
}

func extractToolCallsFromMessageMap(message map[string]any) []any {
	raw, ok := message["tool_calls"]
	if !ok || raw == nil {
		return nil
	}
	calls, ok := raw.([]any)
	if ok {
		return calls
	}
	if typed, ok := raw.([]ToolCall); ok && len(typed) > 0 {
		calls := make([]any, len(typed))
		for i, call := range typed {
			calls[i] = call
		}
		return calls
	}
	return nil
}

func (a *Agent) logf(format string, arguments ...any) {
	if a.logWriter == nil {
		return
	}
	fmt.Fprintf(a.logWriter, format, arguments...)
}

func (a *Agent) appendToolError(messages []map[string]any, toolCallID, format string, arguments ...any) ([]map[string]any, error) {
	return a.appendToolMessage(messages, toolCallID, "Error: "+fmt.Sprintf(format, arguments...))
}

// hasSpecialToolCalls reports whether the batch contains a tool that must run
// sequentially because it recurses or mutates state later calls may depend on.
func (a *Agent) hasSpecialToolCalls(toolCalls []ToolCall, allowPlan bool) bool {
	for _, toolCall := range toolCalls {
		switch toolCall.Function.Name {
		case "write_file", "edit_file":
			return true
		case "run_skill":
			return true
		case "plan":
			if allowPlan && a.planner != nil {
				return true
			}
		}
	}
	return false
}

// runRegularToolCall parses, dispatches, and times one non-special tool call,
// returning its tool_call_id and the result content (errors are rendered as
// "Error: ..." content, matching appendToolError). It is safe to call from
// multiple goroutines; the log/progress writers are synchronized.
func (a *Agent) runRegularToolCall(ctx context.Context, toolCall ToolCall) (string, string) {
	parsedArguments, err := parseArguments(toolCall.Function.Arguments)
	if err != nil {
		return toolCall.ID, fmt.Sprintf("Error: Invalid JSON arguments for tool %q: %v", toolCall.Function.Name, err)
	}
	a.logf("[Tool] %s(%v)\n", toolCall.Function.Name, parsedArguments)

	arguments, ok := parsedArguments.(map[string]any)
	if !ok {
		return toolCall.ID, fmt.Sprintf("Error: Tool arguments for %q must decode to a JSON object", toolCall.Function.Name)
	}
	if path, _ := arguments["path"].(string); strings.TrimSpace(path) != "" {
		a.writeStageProgress(fmt.Sprintf("Running %s on %s", toolCall.Function.Name, path))
	} else {
		a.writeStageProgress(fmt.Sprintf("Running tool %s", toolCall.Function.Name))
	}

	toolStartedAt := time.Now()
	toolResult := ""
	var toolErr error
	function, ok := a.functions[toolCall.Function.Name]
	if !ok {
		toolErr = fmt.Errorf("unknown tool '%s'", toolCall.Function.Name)
		toolResult = fmt.Sprintf("Error: Unknown tool '%s'", toolCall.Function.Name)
	} else {
		toolResult, toolErr = function(ctx, arguments)
		if toolErr != nil {
			toolResult = formatToolError(toolErr, toolResult)
		}
	}
	logToolTiming(a.logWriter, toolCall.Function.Name, time.Since(toolStartedAt), toolErr)
	status := "completed"
	if toolErr != nil {
		status = "failed"
	}
	a.writeStageProgress(fmt.Sprintf("Tool %s %s", toolCall.Function.Name, status))
	if toolCall.Function.Name == "todo_write" && toolErr == nil {
		a.writeProgress(toolResult)
	}
	if a.trace != nil {
		a.trace.ToolCall(toolCall.Function.Name, arguments, toolResult, time.Since(toolStartedAt), toolErr)
		if toolErr == nil && (toolCall.Function.Name == "write_file" || toolCall.Function.Name == "edit_file") {
			if path, _ := arguments["path"].(string); strings.TrimSpace(path) != "" {
				if !filepath.IsAbs(path) {
					path = filepath.Join(a.workspaceRoot, path)
				}
				a.trace.AddArtifact(path, "file")
			}
		}
	}
	return toolCall.ID, toolResult
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

const maxToolErrorOutputChars = 12 * 1024

func formatToolError(err error, output string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return fmt.Sprintf("Error: %v", err)
	}
	if len(output) > maxToolErrorOutputChars {
		output = output[len(output)-maxToolErrorOutputChars:]
		output = "... [truncated]\n" + output
	}
	return fmt.Sprintf("Error: %v\n\nOutput before failure:\n%s", err, output)
}

// writeProgress surfaces a message to the progress writer (chat/channel/webui),
// if one is configured.
func (a *Agent) writeProgress(text string) {
	if a.progressWriter == nil {
		return
	}
	if strings.TrimSpace(text) == "" {
		return
	}
	_, _ = io.WriteString(a.progressWriter, text+"\n")
}

func (a *Agent) writeStageProgress(text string) {
	if !a.stageConfig.EmitProgress {
		return
	}
	if a.stageConfig.MaxIterations <= 0 && a.stageConfig.Timeout <= 0 && !a.stageConfig.LoopProtection {
		return
	}
	a.writeProgress(text)
}

func (a *Agent) appendToolMessage(messages []map[string]any, toolCallID, content string) ([]map[string]any, error) {
	toolMessage := map[string]any{
		"role":         "tool",
		"tool_call_id": toolCallID,
		"content":      content,
	}
	messages = append(messages, toolMessage)
	if err := a.recordMessages(toolMessage); err != nil {
		return messages, err
	}
	return messages, nil
}
