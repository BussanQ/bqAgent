package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bqagent/internal/tools"
	apptrace "bqagent/internal/trace"
)

const (
	// DefaultMaxIterations is the single canonical loop cap shared by every mode.
	// With auto-compaction the loop continues on a budget-bounded context, so this
	// is a runaway safety valve, not a task limit. Override with AGENT_MAX_ITERATIONS.
	DefaultMaxIterations                = 1000
	DefaultContextCompressionTokens     = 128000
	DefaultContextResponseReserveTokens = 4000
	DefaultContextMaxInputTokens        = DefaultContextCompressionTokens + DefaultContextResponseReserveTokens
	DefaultContextKeepLastTurns         = 6
	DefaultExactCountTriggerPercent     = 80
	EarlierConversationSummaryPrefix    = "Summary of earlier conversation:\n"
	// maxParallelTools caps how many independent tool calls in one assistant turn
	// run concurrently.
	maxParallelTools                    = 8
	maxTruncatedToolRecoveries          = 3
	maxEmptyFinalRecoveries             = 2
	truncatedToolBatchError             = "Error: Tool batch was not executed because the model output reached its token limit. No side effects occurred. Resend the complete tool-call batch with complete JSON arguments."
	truncatedToolRecoveryStoppedMessage = "Tool-call recovery stopped after repeated output-token truncation. Increase the model output-token limit or request a smaller tool-call batch; no tools from the truncated batches were executed."
	emptyFinalRecoveryPrompt            = "The previous assistant response contained no usable final content. Continue the task from the existing conversation and completed tool results. If additional work or verification is needed, call the required tools now. Otherwise provide a concise final answer stating what was completed and any verification limitations. Do not return an empty response."
	todoNoProgressRecoveryPrompt        = "The todo list did not change. Planning and rewriting todos are not task progress. Continue this same turn now with a substantive tool such as read_file, glob, grep, execute_bash, or the tool directly required by the task. If a previous search returned no matches, change its arguments or use a different exploration tool instead of repeating it. Do not call todo_write again until task content or status actually changes."
	emptySearchRecoveryPrompt           = "The same search returned no results again. Repeating it cannot advance the task. Continue this same turn with different search arguments or another exploration tool such as read_file or execute_bash."
)

var errEmptyFinalResponse = errors.New("model returned an empty final response")

type MessageRecorder interface {
	RecordMessage(message map[string]any) error
}

type ToolEvent struct {
	Kind       string         `json:"-"`
	Seq        uint64         `json:"seq"`
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Status     string         `json:"status"`
	Arguments  map[string]any `json:"arguments,omitempty"`
	Preview    string         `json:"preview,omitempty"`
	DurationMS int64          `json:"duration_ms,omitempty"`
	Truncated  bool           `json:"truncated"`
}

type ToolEventSink interface {
	EmitToolEvent(ToolEvent)
}

type ContextCheckpointRecorder interface {
	SaveCheckpointSummary(summary string, tailMessages []map[string]any, systemPrompt string) error
}

type PromptContextCheckpointRecorder interface {
	SaveCheckpointSummaryWithPrompt(summary string, tailMessages []map[string]any, systemPrompt, stableHash string, promptMessageCount int) error
}

type ContextConfig struct {
	Enabled                  bool
	MaxInputTokens           int
	TargetInputTokens        int
	ResponseReserveTokens    int
	KeepLastTurns            int
	ExactCountTriggerPercent int
	SummarizationEnabled     bool
	SummaryTriggerTokens     int
	SummaryModel             string
}

type promptUsageBaseline struct {
	messageHashes    []string
	requestShapeHash string
	promptTokens     int
}

type requestTokenMeasurement struct {
	tokens int
	source string
	exact  bool
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
}

type Options struct {
	SystemPrompt    string
	Prompt          PromptSnapshot
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
	ToolEventSink   ToolEventSink
	Context         ContextConfig
	Stage           StageConfig
	ReasoningEffort ReasoningEffort
	Trace           *apptrace.Recorder
}

type Agent struct {
	client          ChatCompletionClient
	model           string
	apiType         APIType
	logWriter       io.Writer
	systemPrompt    string
	prompt          PromptSnapshot
	toolDefinitions []tools.Definition
	functions       map[string]tools.Function
	planner         *Planner
	recorder        MessageRecorder
	checkpointSaver ContextCheckpointRecorder
	stream          bool
	workspaceRoot   string
	progressWriter  io.Writer
	tokenSink       io.Writer
	toolEventSink   ToolEventSink
	toolEventSeq    atomic.Uint64
	contextConfig   ContextConfig
	contextUsageMu  sync.Mutex
	promptUsage     promptUsageBaseline
	stageConfig     StageConfig
	reasoningEffort ReasoningEffort
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
	client = instrumentGenerationMetrics(client)

	prompt := options.Prompt
	if strings.TrimSpace(prompt.Stable) == "" {
		prompt.Stable = options.SystemPrompt
	}
	prompt = NewPromptSnapshot(prompt.Stable, prompt.SessionContext, model, options.APIType)
	systemPrompt := prompt.Combined()

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
		apiType:         NormalizeAPIType(string(options.APIType)),
		logWriter:       logWriter,
		progressWriter:  progressWriter,
		systemPrompt:    systemPrompt,
		prompt:          prompt,
		toolDefinitions: definitions,
		functions:       functions,
		planner:         clonePlannerWithClient(options.Planner, client),
		recorder:        options.Recorder,
		checkpointSaver: checkpointSaver,
		stream:          options.Stream,
		workspaceRoot:   options.WorkspaceRoot,
		tokenSink:       tokenSink,
		toolEventSink:   options.ToolEventSink,
		contextConfig:   contextConfig,
		stageConfig:     normalizeStageConfig(options.Stage),
		reasoningEffort: options.ReasoningEffort,
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
	return config
}

func (a *Agent) Run(ctx context.Context, userMessage string, maxIterations int) (string, error) {
	messages := append(a.prompt.Messages(), map[string]any{"role": "user", "content": userMessage})
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

func (a *Agent) RunConversationTurnWithMetrics(ctx context.Context, messages []map[string]any, maxIterations int) (string, []map[string]any, TurnGenerationMetrics, error) {
	metricsCtx, collector := withTurnGenerationCollector(ctx)
	result, updatedMessages, err := a.runConversation(metricsCtx, duplicateMessages(messages), maxIterations, a.planner != nil)
	if err != nil {
		return result, updatedMessages, TurnGenerationMetrics{}, err
	}
	return result, updatedMessages, collector.metricsFor(result), nil
}

func (a *Agent) runConversation(ctx context.Context, messages []map[string]any, maxIterations int, allowPlan bool) (result string, updatedMessages []map[string]any, err error) {
	messages = ensurePromptSnapshotMessages(messages, a.prompt)
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
	truncatedToolRecoveries := 0
	emptyFinalRecoveries := 0
	defer func() {
		logTurnTiming(a.logWriter, actualIterations, allowPlan, time.Since(startedAt), err)
	}()

	for iteration := 0; iteration < maxIterations; iteration++ {
		// Parent cancellation always wins over a stage boundary. A checkpoint is
		// meaningful only when the stage's own budget expired, not when the caller
		// stopped the whole turn or the server is shutting down.
		if parentErr := ctx.Err(); parentErr != nil {
			return "", updatedMessages, parentErr
		}
		if reason := a.stageBoundaryReason(iteration, explorationCtx); reason != "" {
			return a.finishStageCheckpoint(ctx, updatedMessages, reason, actualIterations)
		}
		actualIterations = iteration + 1
		if a.stageConfig.ImmediateProgress {
			a.writeStageProgress(selectModelProgressMessage(modelProgressMessages))
		}
		completionOptions := ChatCompletionOptions{ReasoningEffort: a.reasoningEffort}
		completionOptions.PromptCache = buildPromptCacheOptions(a.apiType, a.model, a.prompt, definitions, completionOptions)
		requestMessages, compacted := a.buildRequestMessages(explorationCtx, messages, definitions, completionOptions)
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
			onChunk := func(chunk string) {
				sink := a.tokenSink
				if sink == nil {
					sink = a.logWriter
				}
				if sink != nil {
					_, _ = io.WriteString(sink, chunk)
				}
			}
			if client, ok := a.client.(chatCompletionStreamOptionsClient); ok {
				message, requestErr = client.CreateChatCompletionStreamWithOptions(explorationCtx, a.model, requestMessages, definitions, completionOptions, onChunk)
			} else {
				message, requestErr = a.client.CreateChatCompletionStream(explorationCtx, a.model, requestMessages, definitions, onChunk)
			}
		} else if client, ok := a.client.(chatCompletionOptionsClient); ok {
			message, requestErr = client.CreateChatCompletionWithOptions(explorationCtx, a.model, requestMessages, definitions, completionOptions)
		} else {
			message, requestErr = a.client.CreateChatCompletion(explorationCtx, a.model, requestMessages, definitions)
		}
		a.rememberServerPromptUsage(requestMessages, definitions, completionOptions, message.Usage.PromptTokens)
		if a.trace != nil {
			usage := message.Usage
			if usage.TotalTokens == 0 {
				usage.PromptTokens = estimateMessagesTokens(requestMessages)
				usage.CompletionTokens = maxInt(1, len(message.DisplayContent())/4)
				usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
				usage.Estimated = true
			}
			requestMetadata := message.Request
			if requestErr != nil {
				requestMetadata = ProviderRequestMetadataFromError(requestErr)
			}
			cacheMode := requestMetadata.CacheMode
			if cacheMode == "" {
				cacheMode = completionOptions.PromptCache.Mode
			}
			traceRecoveries := make([]apptrace.ModelRecovery, 0, len(requestMetadata.Recoveries))
			for _, recovery := range requestMetadata.Recoveries {
				traceRecoveries = append(traceRecoveries, apptrace.ModelRecovery{
					Kind: recovery.Kind, Attempt: recovery.Attempt, Category: string(recovery.Category),
					StatusCode: recovery.StatusCode, DelayMS: recovery.Delay.Milliseconds(),
				})
			}
			a.trace.ModelCallWithMetadata(apptrace.HashJSON(requestMessages), apptrace.TokenUsage{
				PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
				CachedPromptTokens: usage.CachedPromptTokens, CacheWritePromptTokens: usage.CacheWritePromptTokens,
				TotalTokens: usage.TotalTokens, Estimated: usage.Estimated,
			}, time.Since(modelStartedAt), requestErr, apptrace.ModelCallMetadata{
				RetryCount: requestMetadata.RetryCount, ReasoningDowngraded: requestMetadata.ReasoningDowngraded,
				ReasoningDowngradeSource: requestMetadata.ReasoningDowngradeSource, Recoveries: traceRecoveries,
				StablePromptHash: a.prompt.StableHash, ToolSetHash: apptrace.HashJSON(definitions),
				CacheMode: cacheMode, CacheDowngradeReason: requestMetadata.CacheDowngradeReason,
			})
		}
		if requestErr != nil {
			if errors.Is(requestErr, context.DeadlineExceeded) && ctx.Err() == nil && a.stageConfig.Timeout > 0 {
				return a.finishStageCheckpoint(ctx, updatedMessages, "stage time budget reached", actualIterations)
			}
			err = requestErr
			return "", updatedMessages, err
		}
		message.normalizeInlineToolCalls()
		truncatedToolBatch := message.Completion.OutputTruncated && len(message.ToolCalls) > 0
		if truncatedToolBatch {
			message.ToolCalls = replayableToolCalls(message.ToolCalls)
		}

		requestMessage := message.RequestMessage()
		updatedMessages = append(updatedMessages, requestMessage)
		if err := a.recordMessages(requestMessage); err != nil {
			return "", updatedMessages, err
		}
		messages = updatedMessages

		if a.stream && len(message.ToolCalls) == 0 {
			// content already streamed via onChunk; skip log line
		} else {
			a.logf("[Agent] %s\n", message.DisplayContent())
		}
		if truncatedToolBatch {
			truncatedToolRecoveries++
			var recoveryErr error
			updatedMessages, recoveryErr = a.appendTruncatedToolRecovery(updatedMessages, message.ToolCalls, truncatedToolRecoveries)
			messages = updatedMessages
			if recoveryErr != nil {
				return "", updatedMessages, recoveryErr
			}
			if truncatedToolRecoveries > maxTruncatedToolRecoveries {
				return truncatedToolRecoveryStoppedMessage, updatedMessages, nil
			}
			continue
		}
		truncatedToolRecoveries = 0
		if len(message.ToolCalls) == 0 {
			finalContent := message.FinalContent()
			if message.Content != nil && strings.TrimSpace(finalContent) != "" {
				return finalContent, updatedMessages, nil
			}

			emptyFinalRecoveries++
			if emptyFinalRecoveries > maxEmptyFinalRecoveries {
				return "", updatedMessages, fmt.Errorf("%w after %d consecutive attempts", errEmptyFinalResponse, emptyFinalRecoveries)
			}
			a.writeStageProgress(fmt.Sprintf("Retrying empty final response (%d/%d)", emptyFinalRecoveries, maxEmptyFinalRecoveries))
			updatedMessages, err = a.appendEmptyFinalRecovery(updatedMessages)
			messages = updatedMessages
			if err != nil {
				return "", updatedMessages, err
			}
			continue
		}
		emptyFinalRecoveries = 0
		if !a.stageConfig.ImmediateProgress {
			a.writeStageProgress(fmt.Sprintf("Analyzing tool requests in iteration %d", actualIterations))
		}

		// Some tools recurse or mutate state that later tool calls may depend on, so
		// a turn containing them runs sequentially. A turn of only independent tools
		// runs concurrently and appends results in the original order.
		guardAction := loopGuardAction{}
		if a.hasSpecialToolCalls(message.ToolCalls, allowPlan) {
			for _, toolCall := range message.ToolCalls {
				parsedArguments, err := parseArguments(toolCall.Function.Arguments)
				if err != nil {
					result := fmt.Sprintf("Error: Invalid JSON arguments for tool %q: %v", toolCall.Function.Name, err)
					a.emitToolResult(toolCall, result, 0, true)
					toolMessage, recordErr := a.appendToolMessage(updatedMessages, toolCall.ID, result)
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
					result := fmt.Sprintf("Error: Tool arguments for %q must decode to a JSON object", toolCall.Function.Name)
					a.emitToolResult(toolCall, result, 0, true)
					toolMessage, recordErr := a.appendToolMessage(updatedMessages, toolCall.ID, result)
					updatedMessages = toolMessage
					if recordErr != nil {
						return "", updatedMessages, recordErr
					}
					messages = updatedMessages
					continue
				}

				if toolCall.Function.Name == "plan" && allowPlan && a.planner != nil {
					planStartedAt := time.Now()
					a.emitToolEvent(ToolEvent{Kind: "tool_start", ID: toolCall.ID, Name: toolCall.Function.Name, Status: "running", Arguments: boundedToolEventArguments(arguments)})
					result, updatedPlanMessages, planErr := a.executePlanTool(ctx, updatedMessages, toolCall, arguments, maxIterations)
					preview := result
					if preview == "" && len(updatedPlanMessages) > 0 {
						preview, _ = updatedPlanMessages[len(updatedPlanMessages)-1]["content"].(string)
					}
					a.emitToolResult(toolCall, preview, time.Since(planStartedAt), planErr != nil || strings.HasPrefix(strings.TrimSpace(preview), "Error:"))
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

				_, toolResult := a.runRegularToolCall(explorationCtx, toolCall)
				action := loopGuard.observeTool(toolCall, toolResult)
				if action.recoveryPrompt != "" {
					toolResult = toolResult + "\n\nRecovery instruction: " + action.recoveryPrompt
				}
				updatedToolMessages, recordErr := a.appendToolMessage(updatedMessages, toolCall.ID, toolResult)
				updatedMessages = updatedToolMessages
				messages = updatedMessages
				if recordErr != nil {
					return "", updatedMessages, recordErr
				}
				if action.checkpointReason != "" && !action.todoCheckpoint {
					return a.finishStageCheckpoint(ctx, updatedMessages, action.checkpointReason, actualIterations)
				}
				guardAction = guardAction.merge(action)
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

			for index, toolCall := range message.ToolCalls {
				action := loopGuard.observeTool(toolCall, results[index])
				if action.recoveryPrompt != "" {
					results[index] = results[index] + "\n\nRecovery instruction: " + action.recoveryPrompt
				}
				updatedToolMessages, recordErr := a.appendToolMessage(updatedMessages, toolCall.ID, results[index])
				updatedMessages = updatedToolMessages
				messages = updatedMessages
				if recordErr != nil {
					return "", updatedMessages, recordErr
				}
				guardAction = guardAction.merge(action)
			}
		}
		if guardAction.checkpointReason != "" && (!guardAction.todoCheckpoint || loopGuard.todoNoProgressCount > 1) {
			return a.finishStageCheckpoint(ctx, updatedMessages, guardAction.checkpointReason, actualIterations)
		}
		if guardAction.recoveryPrompt != "" && loopGuard.todoNoProgressCount > 0 {
			a.writeStageProgress("Recovering from a no-progress tool loop")
		}
		a.writeStageProgress(fmt.Sprintf("Completed analysis iteration %d", actualIterations))
	}

	if a.stageConfig.MaxIterations > 0 || a.stageConfig.Timeout > 0 || a.stageConfig.LoopProtection {
		return a.finishStageCheckpoint(ctx, updatedMessages, fmt.Sprintf("maximum turn iterations reached (%d)", maxIterations), actualIterations)
	}
	return fmt.Sprintf("Agent stopped: reached maximum of %d iterations without completing.", maxIterations), updatedMessages, nil
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

func (a *Agent) logf(format string, arguments ...any) {
	if a.logWriter == nil {
		return
	}
	fmt.Fprintf(a.logWriter, format, arguments...)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

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
