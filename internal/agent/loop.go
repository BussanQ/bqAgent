package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"bqagent/internal/tools"
)

const (
	DefaultModel         = "MiniMax-M2.5"
	DefaultSystemPrompt  = "You are a helpful assistant. Be concise."
	DefaultMaxIterations = 20
)

type MessageRecorder interface {
	RecordMessage(message map[string]any) error
}

type Options struct {
	SystemPrompt    string
	LogWriter       io.Writer
	ToolDefinitions []tools.Definition
	Functions       map[string]tools.Function
	Planner         *Planner
	Recorder        MessageRecorder
	Stream          bool
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
	stream          bool
}

func New(client ChatCompletionClient, model string, logWriter io.Writer) *Agent {
	return NewWithOptions(client, model, Options{LogWriter: logWriter})
}

func NewWithOptions(client ChatCompletionClient, model string, options Options) *Agent {
	if model == "" {
		model = DefaultModel
	}

	client = instrumentChatCompletionClient(client, options.LogWriter)

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

	return &Agent{
		client:          client,
		model:           model,
		logWriter:       options.LogWriter,
		systemPrompt:    systemPrompt,
		toolDefinitions: definitions,
		functions:       functions,
		planner:         clonePlannerWithClient(options.Planner, client),
		recorder:        options.Recorder,
		stream:          options.Stream,
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
	result, _, err := a.runConversation(ctx, sanitizeCompletedToolHistory(duplicateMessages(messages)), maxIterations, a.planner != nil)
	return result, err
}

func (a *Agent) RunConversationTurn(ctx context.Context, messages []map[string]any, maxIterations int) (string, []map[string]any, error) {
	return a.runConversation(ctx, sanitizeCompletedToolHistory(duplicateMessages(messages)), maxIterations, a.planner != nil)
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
	actualIterations := 0
	defer func() {
		logTurnTiming(a.logWriter, actualIterations, allowPlan, time.Since(startedAt), err)
	}()

	for iteration := 0; iteration < maxIterations; iteration++ {
		actualIterations = iteration + 1
		requestMessages := sanitizeCompletedToolHistory(duplicateMessages(messages))
		var (
			message    AssistantMessage
			requestErr error
		)
		if a.stream {
			message, requestErr = a.client.CreateChatCompletionStream(ctx, a.model, requestMessages, definitions, func(chunk string) {
				if a.logWriter != nil {
					_, _ = io.WriteString(a.logWriter, chunk)
				}
			})
		} else {
			message, requestErr = a.client.CreateChatCompletion(ctx, a.model, requestMessages, definitions)
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

		for _, toolCall := range message.ToolCalls {
			parsedArguments, err := parseArguments(toolCall.Function.Arguments)
			if err != nil {
				toolMessage, recordErr := a.appendToolMessage(updatedMessages, toolCall.ID, fmt.Sprintf("Error: Invalid JSON arguments for tool %q: %v", toolCall.Function.Name, err))
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
				toolMessage, recordErr := a.appendToolMessage(updatedMessages, toolCall.ID, fmt.Sprintf("Error: Tool arguments for %q must decode to a JSON object", toolCall.Function.Name))
				updatedMessages = toolMessage
				if recordErr != nil {
					return "", updatedMessages, recordErr
				}
				messages = updatedMessages
				continue
			}

			if toolCall.Function.Name == "plan" && allowPlan && a.planner != nil {
				result, updatedMessages, planErr := a.executePlanTool(ctx, updatedMessages, toolCall, arguments, maxIterations)
				messages = updatedMessages
				if planErr != nil {
					return "", updatedMessages, planErr
				}
				if result != "" {
					return result, updatedMessages, nil
				}
				continue
			}

			toolStartedAt := time.Now()
			toolResult := ""
			var toolErr error
			function, ok := a.functions[toolCall.Function.Name]
			if !ok {
				toolErr = fmt.Errorf("unknown tool '%s'", toolCall.Function.Name)
				toolResult = fmt.Sprintf("Error: Unknown tool '%s'", toolCall.Function.Name)
			} else {
				toolResult, toolErr = function(arguments)
				if toolErr != nil {
					toolResult = fmt.Sprintf("Error: %v", toolErr)
				}
			}
			logToolTiming(a.logWriter, toolCall.Function.Name, time.Since(toolStartedAt), toolErr)

			updatedMessages, recordErr := a.appendToolMessage(updatedMessages, toolCall.ID, toolResult)
			messages = updatedMessages
			if recordErr != nil {
				return "", updatedMessages, recordErr
			}
		}
	}

	return fmt.Sprintf("Agent stopped: reached maximum of %d iterations without completing.", maxIterations), updatedMessages, nil
}

func (a *Agent) executePlanTool(ctx context.Context, messages []map[string]any, toolCall ToolCall, arguments map[string]any, maxIterations int) (string, []map[string]any, error) {
	task, err := requireStringArgument(arguments, "task")
	if err != nil {
		updatedMessages, recordErr := a.appendToolMessage(messages, toolCall.ID, fmt.Sprintf("Error: %v", err))
		if recordErr != nil {
			return "", updatedMessages, recordErr
		}
		return "", updatedMessages, nil
	}

	a.logf("[Plan] Breaking down: %s\n", task)
	steps, err := a.planner.Generate(ctx, task)
	if err != nil {
		updatedMessages, recordErr := a.appendToolMessage(messages, toolCall.ID, fmt.Sprintf("Error: plan generation failed: %v", err))
		if recordErr != nil {
			return "", updatedMessages, recordErr
		}
		return "", updatedMessages, nil
	}
	if len(steps) == 0 {
		updatedMessages, recordErr := a.appendToolMessage(messages, toolCall.ID, "Error: planner returned no steps for this task")
		if recordErr != nil {
			return "", updatedMessages, recordErr
		}
		return "", updatedMessages, nil
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

func requireStringArgument(args map[string]any, key string) (string, error) {
	value, ok := args[key]
	if !ok {
		return "", fmt.Errorf("missing required argument %q", key)
	}

	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("argument %q must be a string", key)
	}
	return text, nil
}

func (a *Agent) toolDefinitionsForRun(allowPlan bool) []tools.Definition {
	if allowPlan || a.planner == nil {
		return cloneDefinitions(a.toolDefinitions)
	}

	filtered := make([]tools.Definition, 0, len(a.toolDefinitions))
	for _, definition := range a.toolDefinitions {
		if definition.Function.Name == "plan" {
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
