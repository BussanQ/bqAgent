package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

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
		planner:         options.Planner,
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
	result, _, err := a.runConversation(ctx, duplicateMessages(messages), maxIterations, a.planner != nil)
	return result, err
}

func (a *Agent) RunConversationTurn(ctx context.Context, messages []map[string]any, maxIterations int) (string, []map[string]any, error) {
	return a.runConversation(ctx, duplicateMessages(messages), maxIterations, a.planner != nil)
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

func (a *Agent) runConversation(ctx context.Context, messages []map[string]any, maxIterations int, allowPlan bool) (string, []map[string]any, error) {
	if maxIterations <= 0 {
		maxIterations = DefaultMaxIterations
	}

	definitions := a.toolDefinitionsForRun(allowPlan)
	for iteration := 0; iteration < maxIterations; iteration++ {
		var (
			message AssistantMessage
			err     error
		)
		if a.stream {
			message, err = a.client.CreateChatCompletionStream(ctx, a.model, messages, definitions, func(chunk string) {
				if a.logWriter != nil {
					_, _ = io.WriteString(a.logWriter, chunk)
				}
			})
		} else {
			message, err = a.client.CreateChatCompletion(ctx, a.model, messages, definitions)
		}
		if err != nil {
			return "", messages, err
		}

		requestMessage := message.RequestMessage()
		messages = append(messages, requestMessage)
		if err := a.recordMessages(requestMessage); err != nil {
			return "", messages, err
		}

		if a.stream && len(message.ToolCalls) == 0 {
			// content already streamed via onChunk; skip log line
		} else {
			a.logf("[Agent] %s\n", message.DisplayContent())
		}
		if len(message.ToolCalls) == 0 {
			return message.FinalContent(), messages, nil
		}

		for _, toolCall := range message.ToolCalls {
			parsedArguments, err := parseArguments(toolCall.Function.Arguments)
			if err != nil {
				toolMessage, recordErr := a.appendToolMessage(messages, toolCall.ID, fmt.Sprintf("Error: Invalid JSON arguments for tool %q: %v", toolCall.Function.Name, err))
				messages = toolMessage
				if recordErr != nil {
					return "", messages, recordErr
				}
				continue
			}
			a.logf("[Tool] %s(%v)\n", toolCall.Function.Name, parsedArguments)

			arguments, ok := parsedArguments.(map[string]any)
			if !ok {
				toolMessage, recordErr := a.appendToolMessage(messages, toolCall.ID, fmt.Sprintf("Error: Tool arguments for %q must decode to a JSON object", toolCall.Function.Name))
				messages = toolMessage
				if recordErr != nil {
					return "", messages, recordErr
				}
				continue
			}

			if toolCall.Function.Name == "plan" && allowPlan && a.planner != nil {
				result, updatedMessages, planErr := a.executePlanTool(ctx, messages, toolCall, arguments, maxIterations)
				messages = updatedMessages
				if planErr != nil {
					return "", messages, planErr
				}
				if result != "" {
					return result, messages, nil
				}
				continue
			}

			result := ""
			function, ok := a.functions[toolCall.Function.Name]
			if !ok {
				result = fmt.Sprintf("Error: Unknown tool '%s'", toolCall.Function.Name)
			} else {
				result, err = function(arguments)
				if err != nil {
					result = fmt.Sprintf("Error: %v", err)
				}
			}

			updatedMessages, recordErr := a.appendToolMessage(messages, toolCall.ID, result)
			messages = updatedMessages
			if recordErr != nil {
				return "", messages, recordErr
			}
		}
	}

	return fmt.Sprintf("Agent stopped: reached maximum of %d iterations without completing.", maxIterations), messages, nil
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
