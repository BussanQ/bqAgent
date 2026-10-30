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

	"bqagent/internal/taskstate"
	"bqagent/internal/tools"
	apptrace "bqagent/internal/trace"
	"bqagent/internal/turn"
)

const (
	// DefaultMaxIterations is the single canonical loop cap shared by every mode.
	// With auto-compaction the loop continues on a budget-bounded context, so this
	// is a runaway safety valve, not a task limit. Override with AGENT_MAX_ITERATIONS.
	DefaultMaxIterations                = 1000
	DefaultContextCompressionPercent    = 85
	DefaultContextCompressionTokens     = DefaultContextMaxInputTokens * DefaultContextCompressionPercent / 100
	DefaultContextTargetInputTokens     = 18000 // 9% of the default total budget.
	DefaultContextResponseReserveTokens = 4000
	DefaultContextMaxInputTokens        = 200000
	DefaultContextKeepLastTurns         = 6
	DefaultExactCountTriggerPercent     = 100 // Count exactly when approaching the compression trigger.
	EarlierConversationSummaryPrefix    = "Summary of earlier conversation:\n"
	// maxParallelTools caps how many independent tool calls in one assistant turn
	// run concurrently.
	maxParallelTools                    = 8
	maxTruncatedToolRecoveries          = 3
	maxEmptyFinalRecoveries             = 2
	truncatedToolBatchError             = "Error: Tool batch was not executed because the model output reached its token limit. No side effects occurred. Resend the complete tool-call batch with complete JSON arguments."
	truncatedToolRecoveryStoppedMessage = "Tool-call recovery stopped after repeated output-token truncation. Increase the model output-token limit or request a smaller tool-call batch; no tools from the truncated batches were executed."
	emptyFinalRecoveryPrompt            = "The previous assistant response contained no usable final content. Continue the task from the existing conversation and completed tool results. If additional work or verification is needed, call the required tools now. Otherwise provide a concise final answer stating what was completed and any verification limitations. Do not return an empty response."
	emptySearchRecoveryPrompt           = "The same search returned no results again. Repeating it cannot advance the task. Continue this same turn with different search arguments or another exploration tool such as read_file or bash."
)

var errEmptyFinalResponse = errors.New("model returned an empty final response")

type MessageRecorder interface {
	RecordMessage(message map[string]any) error
}

type ToolEvent struct {
	apptrace.ToolExecution
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
	Enabled                   bool
	MaxInputTokens            int
	CompressionTriggerTokens  int // Optional absolute override; zero derives from the total budget.
	CompressionTriggerPercent int
	TargetInputTokens         int
	ResponseReserveTokens     int
	KeepLastTurns             int
	ExactCountTriggerPercent  int
	SummarizationEnabled      bool
	SummaryTriggerTokens      int
	SummaryModel              string
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
	// EnableNativeWebSearch allows adding model-backed search to an explicitly
	// supplied toolset. Default toolsets enable it automatically.
	EnableNativeWebSearch bool

	TurnID          string
	MessageSink     turn.Sink
	TaskRelation    turn.Relation
	TaskStore       taskstate.Store
	SystemPrompt    string
	Prompt          PromptSnapshot
	APIType         APIType
	LogWriter       io.Writer
	ToolDefinitions []tools.Definition
	Functions       map[string]tools.Function
	Recorder        MessageRecorder
	Stream          bool
	WorkspaceRoot   string
	ProgressWriter  io.Writer
	TokenSink       io.Writer
	ToolEventSink   ToolEventSink
	Context         ContextConfig
	Stage           StageConfig
	// InvestigationRoundLimit sets the first convergence budget; subsequent
	// budgets halve toward a tool-free decision. Zero defaults; negative disables.
	InvestigationRoundLimit int
	BashJobs                tools.BashJobOptions
	ReasoningEffort         ReasoningEffort
	ServiceTier             string
	Trace                   *apptrace.Recorder
}

type Agent struct {
	webSearchAvailable func() bool

	turnID                  string
	messageSink             turn.Sink
	taskRelation            turn.Relation
	taskStore               taskstate.Store
	task                    *taskstate.State
	client                  ChatCompletionClient
	model                   string
	apiType                 APIType
	logWriter               io.Writer
	systemPrompt            string
	prompt                  PromptSnapshot
	toolDefinitions         []tools.Definition
	functions               map[string]tools.Function
	recorder                MessageRecorder
	checkpointSaver         ContextCheckpointRecorder
	stream                  bool
	workspaceRoot           string
	progressWriter          io.Writer
	tokenSink               io.Writer
	toolEventSink           ToolEventSink
	toolEventSeq            atomic.Uint64
	contextConfig           ContextConfig
	contextUsageMu          sync.Mutex
	promptUsage             promptUsageBaseline
	stageConfig             StageConfig
	investigationRoundLimit int
	bashJobOptions          tools.BashJobOptions
	reasoningEffort         ReasoningEffort
	serviceTier             string
	trace                   *apptrace.Recorder
}

func New(client ChatCompletionClient, model string, logWriter io.Writer) *Agent {
	return NewWithOptions(client, model, Options{LogWriter: logWriter})
}

func NewWithOptions(client ChatCompletionClient, model string, options Options) *Agent {
	model = EffectiveModel(model)
	searchClient, _ := client.(nativeWebSearcher)

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

	app := &Agent{
		turnID: options.TurnID, messageSink: options.MessageSink, taskRelation: options.TaskRelation,
		taskStore:               options.TaskStore,
		client:                  client,
		model:                   model,
		apiType:                 NormalizeAPIType(string(options.APIType)),
		logWriter:               logWriter,
		progressWriter:          progressWriter,
		systemPrompt:            systemPrompt,
		prompt:                  prompt,
		toolDefinitions:         definitions,
		functions:               functions,
		recorder:                options.Recorder,
		checkpointSaver:         checkpointSaver,
		stream:                  options.Stream,
		workspaceRoot:           options.WorkspaceRoot,
		tokenSink:               tokenSink,
		toolEventSink:           options.ToolEventSink,
		contextConfig:           contextConfig,
		stageConfig:             normalizeStageConfig(options.Stage),
		investigationRoundLimit: investigationRoundLimit(options.InvestigationRoundLimit),
		bashJobOptions:          options.BashJobs,
		reasoningEffort:         options.ReasoningEffort,
		serviceTier:             options.ServiceTier,
		trace:                   options.Trace,
	}
	if options.EnableNativeWebSearch || (options.ToolDefinitions == nil && options.Functions == nil) {
		app.installNativeWebSearch(searchClient)
	}
	app.installTaskTool()
	return app
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
	result, err := a.RunTurn(ctx, messages, maxIterations)
	return result.Reply, err
}

func (a *Agent) RunConversationTurn(ctx context.Context, messages []map[string]any, maxIterations int) (string, []map[string]any, error) {
	result, err := a.RunTurn(ctx, messages, maxIterations)
	return result.Reply, result.Messages, err
}

func (a *Agent) RunConversationTurnWithMetrics(ctx context.Context, messages []map[string]any, maxIterations int) (string, []map[string]any, TurnGenerationMetrics, error) {
	result, err := a.RunTurn(ctx, messages, maxIterations)
	return result.Reply, result.Messages, result.Generation, err
}

func (a *Agent) runConversation(ctx context.Context, messages []map[string]any, maxIterations int) (result string, updatedMessages []map[string]any, err error) {
	ctx, continuations := continuationContext(ctx)
	inputs := turn.Inputs(ctx)
	if inputs != nil {
		defer inputs.Close()
	}
	if a.taskStore != nil {
		a.task, err = a.taskStore.LoadTaskState()
		if err != nil {
			a.logf("[Task] could not load progress: %v; continuing without a saved task\n", err)
			a.task, err = nil, nil
		}
		visibleTask := a.task
		if visibleTask != nil && (visibleTask.Status == "completed" || visibleTask.Status == "cancelled") {
			visibleTask = nil
		}
		turnRun(ctx).emitTask(visibleTask)
	}
	messages = ensurePromptSnapshotMessages(messages, a.prompt)
	startedAt := time.Now()
	updatedMessages = messages
	if maxIterations <= 0 {
		maxIterations = DefaultMaxIterations
	}

	actualIterations := 0
	explorationCtx := ctx
	cancelExploration := func() {}
	if a.stageConfig.Timeout > 0 {
		explorationCtx, cancelExploration = context.WithTimeout(ctx, a.stageConfig.Timeout)
	}
	defer cancelExploration()
	loopGuard := newLoopGuard(a.stageConfig)
	loopGuard.investigation.limit = a.investigationRoundLimit
	truncatedToolRecoveries := 0
	emptyFinalRecoveries := 0
	defer func() {
		logTurnTiming(a.logWriter, actualIterations, time.Since(startedAt), err)
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
		if inputs != nil {
			pending := inputs.TakeSteering()
			if len(pending) > 0 {
				loopGuard.investigation.reset()
			}
			messages, err = a.appendTurnInputs(ctx, messages, pending, false)
			updatedMessages = messages
			if err != nil {
				return "", updatedMessages, err
			}
		}
		messages, err = a.deliverFinishedBashJobs(ctx, messages)
		updatedMessages = messages
		if err != nil {
			return "", updatedMessages, err
		}
		actualIterations = iteration + 1
		if a.stageConfig.ImmediateProgress {
			a.writeStageProgress(selectModelProgressMessage(modelProgressMessages))
		}
		decisionOnly := loopGuard.investigation.decisionPending
		definitions := loopGuard.filterDefinitions(a.toolDefinitionsForRun())
		completionOptions := ChatCompletionOptions{ReasoningEffort: a.reasoningEffort, ServiceTier: a.serviceTier, DisableTools: decisionOnly}
		if decisionOnly {
			definitions = nil
			a.writeProgress("调查多次未收敛，已暂停工具调用，要求模型先给出决策。")
		}
		completionOptions.PromptCache = buildPromptCacheOptions(a.apiType, a.model, a.prompt, definitions, completionOptions)
		var decisionTail []map[string]any
		if decisionOnly {
			prompt := investigationDecisionPrompt
			if loopGuard.investigation.exhausted {
				prompt += "\n" + investigationStopPrompt
			}
			decisionTail = []map[string]any{{"role": "developer", "content": prompt}}
		}
		requestMessages, _, budgetErr := a.buildRequestMessagesWithTail(explorationCtx, messages, definitions, completionOptions, decisionTail)
		if budgetErr != nil {
			return "", updatedMessages, budgetErr
		}
		if recordErr := a.recordRequestTaskContext(messages, requestMessages); recordErr != nil {
			return "", updatedMessages, recordErr
		}
		messages = requestMessages
		updatedMessages = requestMessages
		var (
			message    AssistantMessage
			requestErr error
		)
		modelStartedAt := time.Now()
		state := turnRun(ctx)
		itemID := state.newItem()
		var streamedText strings.Builder
		if a.stream {
			onChunk := func(chunk string) {
				streamedText.WriteString(chunk)
				if decisionOnly {
					return // Display the parsed report rather than the control response.
				}
				state.emit(itemID, "message.updated", "draft", streamedText.String())
				if a.messageSink != nil {
					return
				}
				sink := a.tokenSink
				if sink == nil {
					sink = a.logWriter
				}
				if sink != nil {
					_, _ = io.WriteString(sink, chunk)
				}
			}
			streamCtx := withStreamRetryObserver(explorationCtx, func() {
				if streamedText.Len() == 0 {
					return // Nothing visible to retract.
				}
				streamedText.Reset()
				state.emit(itemID, "message.updated", "draft", "")
				if a.messageSink == nil {
					sink := a.tokenSink
					if sink == nil {
						sink = a.logWriter
					}
					if sink != nil {
						_, _ = io.WriteString(sink, "\n\n[上游请求中断，正在重试；以下为重新生成的回复]\n\n")
					}
				}
			})
			if client, ok := a.client.(chatCompletionStreamOptionsClient); ok {
				message, requestErr = client.CreateChatCompletionStreamWithOptions(streamCtx, a.model, requestMessages, definitions, completionOptions, onChunk)
			} else {
				message, requestErr = a.client.CreateChatCompletionStream(streamCtx, a.model, requestMessages, definitions, onChunk)
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
		if requestErr == nil {
			requestErr = ctx.Err()
		}
		if requestErr == nil && strings.EqualFold(message.Completion.StopReason, "incomplete") && !message.Completion.OutputTruncated {
			requestErr = fmt.Errorf("model response incomplete: %s", firstNonBlank(message.Completion.IncompleteReason, "unspecified reason"))
		}
		if requestErr != nil {
			partial := firstNonBlank(streamedText.String(), contentText(message.Content))
			if strings.TrimSpace(partial) != "" {
				// Only preserve visible text, never incomplete or unexecuted tool calls.
				text := "[未完成的回复：生成过程中断，以下内容仅为已收到的部分，不代表任务已完成。]\n\n" + partial
				if decisionOnly {
					text = "[调查决策生成中断，任务尚未完成。已收到的决策草稿：]\n\n" + partial
				}
				draft := map[string]any{"role": "assistant", "content": text, "phase": "commentary"}
				updatedMessages = append(updatedMessages, draft)
				if saveErr := a.recordMessages(draft); saveErr != nil {
					return "", updatedMessages, errors.Join(requestErr, fmt.Errorf("save interrupted reply: %w", saveErr))
				}
				state.emit(itemID, "message.updated", "interrupted", text)
			}
			if errors.Is(requestErr, context.DeadlineExceeded) && ctx.Err() == nil && a.stageConfig.Timeout > 0 {
				return a.finishStageCheckpoint(ctx, updatedMessages, "stage time budget reached", actualIterations)
			}
			return "", updatedMessages, requestErr
		}
		message.normalizeInlineToolCalls()
		if decisionOnly {
			if inputs != nil {
				if pending := inputs.TakeSteering(); len(pending) > 0 {
					loopGuard.investigation.reset()
					updatedMessages, err = a.appendTurnInputs(ctx, updatedMessages, pending, false)
					messages = updatedMessages
					if err != nil {
						return "", updatedMessages, err
					}
					continue
				}
			}
			decision, decisionErr := parseInvestigationDecision(contentText(message.Content))
			invalid := decisionErr != nil || len(message.ToolCalls) > 0 || message.Completion.OutputTruncated
			text := decision.Report
			if invalid {
				text = "调查多次未收敛，模型未能提供有效决策，本轮已暂停。已有工具结果和修改保存在会话中；任务尚未完成，请根据最后的证据继续最小实现或补充具体阻塞信息。"
			} else if decision.Action == "implement" || decision.Action == "investigate" {
				text = fmt.Sprintf("调查结论：%s\n本阶段交付：%s\n下一步：%s", decision.Facts, decision.Deliverable, decision.NextStep)
				if decision.Blocker != "" {
					text += "\n待解决问题：" + decision.Blocker
				}
			}
			stop := invalid || loopGuard.investigation.exhausted || decision.Action == "blocked"
			if stop {
				setTurnStop(ctx, turn.Blocked, "investigation_stalled")
				if !invalid {
					text = "本轮调查未能继续推进，任务尚未完成。\n\n" + text
				}
			}
			decisionMessage := map[string]any{"role": "assistant", "content": text, "phase": "commentary"}
			updatedMessages = append(updatedMessages, decisionMessage)
			if err := a.recordMessages(decisionMessage); err != nil {
				return "", updatedMessages, err
			}
			state.emit(itemID, "message.completed", "commentary", text)
			if stop || decision.Action == "complete" {
				state.finalItem = itemID
				return text, updatedMessages, nil
			}
			loopGuard.investigation.decisionPending = false
			loopGuard.investigation.decisionUsed = true
			loopGuard.investigation.rounds = 0
			updatedMessages, err = a.appendContinuation(updatedMessages, "Execute the recorded decision now. Tools are available again. Resolve only the named uncertainty or take the smallest authorized implementation step; do not expand scope. The remaining recovery budget is at most three tool rounds without concrete progress.")
			messages = updatedMessages
			if err != nil {
				return "", updatedMessages, err
			}
			continue
		}
		truncatedToolBatch := message.Completion.OutputTruncated && len(message.ToolCalls) > 0
		recoveredToolCalls := false
		if truncatedToolBatch {
			message.ToolCalls = replayableToolCalls(message.ToolCalls)
		} else {
			message.ToolCalls, recoveredToolCalls = recoverConcatenatedToolCalls(message.ToolCalls)
			if recoveredToolCalls {
				a.logf("[Agent] Split concatenated tool arguments into separate tool calls; executing batch sequentially\n")
			}
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
			state.emit(itemID, "message.completed", "commentary", contentText(message.Content))
			truncatedToolRecoveries++
			var recoveryErr error
			updatedMessages, recoveryErr = a.appendTruncatedToolRecovery(updatedMessages, message.ToolCalls, truncatedToolRecoveries)
			messages = updatedMessages
			if recoveryErr != nil {
				return "", updatedMessages, recoveryErr
			}
			if truncatedToolRecoveries > maxTruncatedToolRecoveries {
				setTurnStop(ctx, turn.Blocked, "tool_output_truncated")
				return truncatedToolRecoveryStoppedMessage, updatedMessages, nil
			}
			continue
		}
		truncatedToolRecoveries = 0
		if len(message.ToolCalls) == 0 {
			canContinue := iteration+1 < maxIterations && a.stageBoundaryReason(iteration+1, explorationCtx) == ""
			if inputs != nil && canContinue {
				if pending := inputs.TakeSteering(); len(pending) > 0 {
					state.emit(itemID, "message.completed", "commentary", contentText(message.Content))
					loopGuard.investigation.reset()
					updatedMessages, err = a.appendTurnInputs(ctx, updatedMessages, pending, true)
					messages = updatedMessages
					if err != nil {
						return "", updatedMessages, err
					}
					continue
				}
			}
			if reason := a.continuationReason(ctx, message); reason != "" {
				if loopGuard.investigation.decisionUsed {
					loopGuard.investigation.beginRound()
					if prompt := loopGuard.investigation.finishRound(); prompt != "" {
						reason = prompt
					}
				}
				if strings.TrimSpace(message.FinalContent()) != "" {
					emptyFinalRecoveries = 0
				}
				state.emit(itemID, "message.completed", "commentary", contentText(message.Content))
				reason = continuations.recoveryPrompt(reason)
				a.writeProgress("任务尚未完成，继续执行与验证。")
				updatedMessages, err = a.appendContinuation(updatedMessages, reason)
				messages = updatedMessages
				if err != nil {
					return "", updatedMessages, err
				}
				continue
			}
			finalContent := message.FinalContent()
			if message.Content != nil && strings.TrimSpace(finalContent) != "" {
				if inputs != nil && canContinue && state.canAcceptFollowUp() {
					if pending := inputs.Finish(); len(pending) > 0 {
						state.emit(itemID, "message.completed", "commentary", finalContent)
						loopGuard.investigation.reset()
						updatedMessages, err = a.appendTurnInputs(ctx, updatedMessages, pending, true)
						messages = updatedMessages
						if err != nil {
							return "", updatedMessages, err
						}
						continue
					}
				}
				if state != nil {
					state.finalItem = itemID
				}
				return finalContent, updatedMessages, nil
			}

			state.emit(itemID, "message.completed", "commentary", "")
			emptyFinalRecoveries++
			a.logf("[EmptyFinalResponse] attempt=%d stop_reason=%q incomplete_reason=%q truncated=%t phase=%q response_items=%d output_tokens=%d reasoning_tokens=%d diagnostics=%s\n",
				emptyFinalRecoveries, message.Completion.StopReason, message.Completion.IncompleteReason,
				message.Completion.OutputTruncated, message.Phase, len(message.ResponseItems),
				message.Usage.CompletionTokens, message.Usage.ReasoningTokens, message.ResponseDiagnostics)
			if emptyFinalRecoveries > maxEmptyFinalRecoveries {
				return "", updatedMessages, fmt.Errorf("%w after %d consecutive attempts", errEmptyFinalResponse, emptyFinalRecoveries)
			}
			a.writeStageProgress(fmt.Sprintf("Retrying empty final response (%d/%d)", emptyFinalRecoveries, maxEmptyFinalRecoveries))
			updatedMessages, err = a.appendEmptyFinalRecovery(updatedMessages, continuations.recoveryPrompt(emptyFinalRecoveryPrompt))
			messages = updatedMessages
			if err != nil {
				return "", updatedMessages, err
			}
			continue
		}
		state.emit(itemID, "message.completed", "commentary", contentText(message.Content))
		emptyFinalRecoveries = 0
		if !a.stageConfig.ImmediateProgress {
			a.writeStageProgress(fmt.Sprintf("Analyzing tool requests in iteration %d", actualIterations))
		}

		// Some tools recurse or mutate state that later tool calls may depend on, so
		// a turn containing them runs sequentially. A turn of only independent tools
		// runs concurrently and appends results in the original order.
		guardAction := loopGuardAction{}
		loopGuard.investigation.beginRound()
		var batchImages []tools.ToolImage
		if recoveredToolCalls || a.hasSpecialToolCalls(message.ToolCalls) {
			recoveredSuccesses := map[string]bool{}
			for _, toolCall := range message.ToolCalls {
				parsedArguments, err := parseArguments(toolCall.Function.Arguments)
				if err != nil {
					result := fmt.Sprintf("Error: Invalid JSON arguments for tool %q: %v", toolCall.Function.Name, err)
					guardAction = guardAction.merge(loopGuard.observeTool(toolCall, result))
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

				_, ok := parsedArguments.(map[string]any)
				if !ok {
					result := fmt.Sprintf("Error: Tool arguments for %q must decode to a JSON object", toolCall.Function.Name)
					guardAction = guardAction.merge(loopGuard.observeTool(toolCall, result))
					a.emitToolResult(toolCall, result, 0, true)
					toolMessage, recordErr := a.appendToolMessage(updatedMessages, toolCall.ID, result)
					updatedMessages = toolMessage
					if recordErr != nil {
						return "", updatedMessages, recordErr
					}
					messages = updatedMessages
					continue
				}

				_, toolResult, images := a.runRegularToolCallWithImages(explorationCtx, toolCall)
				batchImages = append(batchImages, images...)
				continuations.observe(toolCall, toolResult)
				var action loopGuardAction
				if recoveredToolCalls {
					action = loopGuard.observeRecoveredTool(toolCall, toolResult, recoveredSuccesses)
				} else {
					action = loopGuard.observeTool(toolCall, toolResult)
				}
				if action.recoveryPrompt != "" {
					toolResult = toolResult + "\n\nRecovery instruction: " + action.recoveryPrompt
				}
				updatedToolMessages, recordErr := a.appendToolMessage(updatedMessages, toolCall.ID, toolResult)
				updatedMessages = updatedToolMessages
				messages = updatedMessages
				if recordErr != nil {
					return "", updatedMessages, recordErr
				}
				if err := ctx.Err(); err != nil {
					// Record cancellation for the remaining calls as well, preserving
					// complete tool-call/result pairs without executing their functions.
					continue
				}
				// Finish a recovered batch before checkpointing so no split call
				// is silently dropped or left without its result in history.
				if action.checkpointReason != "" && !recoveredToolCalls {
					if updatedMessages, recordErr = a.appendToolImages(updatedMessages, batchImages); recordErr != nil {
						return "", updatedMessages, recordErr
					}
					setTurnStop(ctx, turn.Blocked, action.checkpointReason)
					return a.finishStageCheckpoint(ctx, updatedMessages, action.checkpointReason, actualIterations)
				}
				guardAction = guardAction.merge(action)
			}
		} else {
			results := make([]string, len(message.ToolCalls))
			resultImages := make([][]tools.ToolImage, len(message.ToolCalls))
			semaphore := make(chan struct{}, maxParallelTools)
			var waitGroup sync.WaitGroup
			for index, toolCall := range message.ToolCalls {
				waitGroup.Add(1)
				go func(index int, toolCall ToolCall) {
					defer waitGroup.Done()
					semaphore <- struct{}{}
					defer func() { <-semaphore }()
					_, content, images := a.runRegularToolCallWithImages(explorationCtx, toolCall)
					results[index] = content
					resultImages[index] = images
				}(index, toolCall)
			}
			waitGroup.Wait()

			for index, toolCall := range message.ToolCalls {
				continuations.observe(toolCall, results[index])
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
				batchImages = append(batchImages, resultImages[index]...)
			}
		}
		updatedMessages, err = a.appendToolImages(updatedMessages, batchImages)
		messages = updatedMessages
		if err != nil {
			return "", updatedMessages, err
		}
		if err := ctx.Err(); err != nil {
			return "", updatedMessages, err
		}
		if guardAction.checkpointReason != "" {
			setTurnStop(ctx, turn.Blocked, guardAction.checkpointReason)
			return a.finishStageCheckpoint(ctx, updatedMessages, guardAction.checkpointReason, actualIterations)
		}
		if prompt := loopGuard.investigation.finishRound(); prompt != "" {
			updatedMessages, err = a.appendContinuation(updatedMessages, prompt)
			messages = updatedMessages
			if err != nil {
				return "", updatedMessages, err
			}
			a.writeStageProgress("调查已持续多轮，已提示模型收敛并推进下一步。")
		}
		if guardAction.recoveryPrompt != "" && loopGuard.taskUpdateStall >= taskUpdateRecoveryThreshold {
			a.writeStageProgress("Recovering from a no-progress tool loop")
		}
		a.writeStageProgress(fmt.Sprintf("Completed analysis iteration %d", actualIterations))
	}

	if a.stageConfig.MaxIterations > 0 || a.stageConfig.Timeout > 0 || a.stageConfig.LoopProtection {
		return a.finishStageCheckpoint(ctx, updatedMessages, fmt.Sprintf("maximum turn iterations reached (%d)", maxIterations), actualIterations)
	}
	setTurnStop(ctx, turn.BudgetLimited, "max_iterations")
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
