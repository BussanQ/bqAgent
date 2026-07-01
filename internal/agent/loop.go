package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"bqagent/internal/tools"
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

type Options struct {
	SystemPrompt    string
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
}

func New(client ChatCompletionClient, model string, logWriter io.Writer) *Agent {
	return NewWithOptions(client, model, Options{LogWriter: logWriter})
}

func NewWithOptions(client ChatCompletionClient, model string, options Options) *Agent {
	if model == "" {
		model = DefaultModel
	}

	logWriter := synchronizeLogWriter(options.LogWriter)
	progressWriter := synchronizeLogWriter(options.ProgressWriter)
	tokenSink := synchronizeLogWriter(options.TokenSink)
	client = instrumentChatCompletionClient(client, logWriter, progressWriter)

	systemPrompt := strings.TrimSpace(options.SystemPrompt)
	if systemPrompt == "" {
		systemPrompt = DefaultSystemPrompt
	}

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
	}
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
	result, _, err := a.runPlannedConversation(ctx, duplicateMessages(messages), task, maxIterations)
	return result, err
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
	startedAt := time.Now()
	updatedMessages = messages
	if maxIterations <= 0 {
		maxIterations = DefaultMaxIterations
	}

	definitions := a.toolDefinitionsForRun(allowPlan)
	guard := newNoProgressGuard()
	actualIterations := 0
	defer func() {
		logTurnTiming(a.logWriter, actualIterations, allowPlan, time.Since(startedAt), err)
	}()

	for iteration := 0; iteration < maxIterations; iteration++ {
		actualIterations = iteration + 1
		requestMessages, compacted := a.buildRequestMessages(ctx, messages)
		if compacted != nil {
			messages = compacted
			updatedMessages = compacted
		}
		var (
			message    AssistantMessage
			requestErr error
		)
		requestMessages = guard.appendFinalizationReminder(requestMessages)
		requestDefinitions := guard.requestDefinitions(definitions)
		if a.stream {
			message, requestErr = a.client.CreateChatCompletionStream(ctx, a.model, requestMessages, requestDefinitions, func(chunk string) {
				sink := a.tokenSink
				if sink == nil {
					sink = a.logWriter
				}
				if sink != nil {
					_, _ = io.WriteString(sink, chunk)
				}
			})
		} else {
			message, requestErr = a.client.CreateChatCompletion(ctx, a.model, requestMessages, requestDefinitions)
		}
		if requestErr != nil {
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

		if a.stream && len(message.ToolCalls) == 0 {
			// content already streamed via onChunk; skip log line
		} else {
			a.logf("[Agent] %s\n", message.DisplayContent())
		}
		if len(message.ToolCalls) == 0 {
			return message.FinalContent(), updatedMessages, nil
		}
		if guard.finalizing {
			return guard.stoppedMessage(), updatedMessages, nil
		}

		// plan and run_skill recurse and mutate the working history in place, so a
		// turn containing either runs sequentially. A turn of only regular tools
		// runs them concurrently and appends results in the original order.
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

				decision := guard.before(toolCall)
				toolResult := decision.Content
				if !decision.Skipped {
					_, toolResult = a.runRegularToolCall(ctx, toolCall)
					guard.after(toolCall, decision, toolResult)
				}
				updatedToolMessages, recordErr := a.appendToolMessage(updatedMessages, toolCall.ID, toolResult)
				updatedMessages = updatedToolMessages
				messages = updatedMessages
				if recordErr != nil {
					return "", updatedMessages, recordErr
				}
			}
		} else {
			results := make([]string, len(message.ToolCalls))
			decisions := make([]toolExecutionDecision, len(message.ToolCalls))
			semaphore := make(chan struct{}, maxParallelTools)
			var waitGroup sync.WaitGroup
			for index, toolCall := range message.ToolCalls {
				decision := guard.before(toolCall)
				decisions[index] = decision
				if decision.Skipped {
					results[index] = decision.Content
					continue
				}
				waitGroup.Add(1)
				go func(index int, toolCall ToolCall) {
					defer waitGroup.Done()
					semaphore <- struct{}{}
					defer func() { <-semaphore }()
					_, content := a.runRegularToolCall(ctx, toolCall)
					results[index] = content
				}(index, toolCall)
			}
			waitGroup.Wait()
			for index, toolCall := range message.ToolCalls {
				guard.after(toolCall, decisions[index], results[index])
			}

			for index, toolCall := range message.ToolCalls {
				updatedToolMessages, recordErr := a.appendToolMessage(updatedMessages, toolCall.ID, results[index])
				updatedMessages = updatedToolMessages
				messages = updatedMessages
				if recordErr != nil {
					return "", updatedMessages, recordErr
				}
			}
		}
	}

	return fmt.Sprintf("Agent stopped: reached maximum of %d iterations without completing.", maxIterations), updatedMessages, nil
}

func (a *Agent) executePlanTool(ctx context.Context, messages []map[string]any, toolCall ToolCall, arguments map[string]any, maxIterations int) (string, []map[string]any, error) {
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

func (a *Agent) executeSkillTool(ctx context.Context, messages []map[string]any, toolCall ToolCall, arguments map[string]any, maxIterations int) ([]map[string]any, error) {
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
	}
	result, _, err := child.runConversation(ctx, []map[string]any{{"role": "system", "content": a.systemPrompt}, {"role": "user", "content": skillTask}}, maxIterations, false)
	if err != nil {
		return messages, err
	}

	messages[len(messages)-1]["content"] = result
	return messages, nil
}

// buildRequestMessages returns the message payload to send to the model and,
// when summarization compacted the history, a non-nil compacted working set the
// loop should adopt in place of its full in-memory history. compacted is nil for
// the disabled / under-budget / pruned paths, leaving the working set untouched
// exactly as before — only the (expensive) summarize path is adopted so the loop
// continues on the compacted context instead of re-summarizing from scratch each
// turn. The synthetic summary message lives only in this returned set (and in
// context_checkpoint.json); it is never recorded to the raw transcript.
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
	prunedTokens := estimateMessagesTokens(pruned)
	if !a.contextConfig.SummarizationEnabled || !shouldSummarize(estimatedTokens, a.contextConfig) {
		a.logContextBudget(len(messages), len(sanitized), len(pruned), prunedTokens, true, false)
		return pruned, nil
	}

	summarized, ok := a.summarizeMessages(ctx, sanitized)
	if !ok {
		a.logContextBudget(len(messages), len(sanitized), len(pruned), prunedTokens, true, false)
		return pruned, nil
	}
	summaryTokens := estimateMessagesTokens(summarized)
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
	if a.checkpointSaver != nil {
		if err := a.checkpointSaver.SaveCheckpointSummary(summary, tail, a.systemPrompt); err != nil {
			a.logf("[Context] checkpoint save failed: %v\n", err)
		}
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
			}
			continue
		}
		if role == "assistant" && len(extractToolCallsFromMessageMap(message)) > 0 {
			if i == pendingAssistantIndex {
				sanitized = append(sanitized, message)
			}
			continue
		}
		sanitized = append(sanitized, message)
	}
	return sanitized
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
// sequentially because it recurses and mutates the working history in place.
func (a *Agent) hasSpecialToolCalls(toolCalls []ToolCall, allowPlan bool) bool {
	for _, toolCall := range toolCalls {
		switch toolCall.Function.Name {
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
	if toolCall.Function.Name == "todo_write" && toolErr == nil {
		a.writeProgress(toolResult)
	}
	return toolCall.ID, toolResult
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
