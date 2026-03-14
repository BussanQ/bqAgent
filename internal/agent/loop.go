package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"nanoagent/internal/tools"
)

const (
	DefaultModel         = "MiniMax-M2.5"
	DefaultSystemPrompt  = "You are a helpful assistant. Be concise."
	DefaultMaxIterations = 5
)

type Agent struct {
	client          ChatCompletionClient
	model           string
	logWriter       io.Writer
	toolDefinitions []tools.Definition
	functions       map[string]tools.Function
}

func New(client ChatCompletionClient, model string, logWriter io.Writer) *Agent {
	if model == "" {
		model = DefaultModel
	}
	return &Agent{
		client:          client,
		model:           model,
		logWriter:       logWriter,
		toolDefinitions: tools.Definitions(),
		functions:       tools.Registry(),
	}
}

func (a *Agent) Run(ctx context.Context, userMessage string, maxIterations int) (string, error) {
	if maxIterations <= 0 {
		maxIterations = DefaultMaxIterations
	}

	messages := []map[string]any{
		{"role": "system", "content": DefaultSystemPrompt},
		{"role": "user", "content": userMessage},
	}

	for iteration := 0; iteration < maxIterations; iteration++ {
		message, err := a.client.CreateChatCompletion(ctx, a.model, messages, a.toolDefinitions)
		if err != nil {
			return "", err
		}

		messages = append(messages, message.RequestMessage())
		a.logf("[Agent] %s\n", message.DisplayContent())
		if len(message.ToolCalls) == 0 {
			return message.FinalContent(), nil
		}

		for _, toolCall := range message.ToolCalls {
			parsedArguments, err := parseArguments(toolCall.Function.Arguments)
			if err != nil {
				return "", err
			}
			a.logf("[Tool] %s(%v)\n", toolCall.Function.Name, parsedArguments)

			result := ""
			function, ok := a.functions[toolCall.Function.Name]
			if !ok {
				result = fmt.Sprintf("Error: Unknown tool '%s'", toolCall.Function.Name)
			} else {
				arguments, ok := parsedArguments.(map[string]any)
				if !ok {
					return "", fmt.Errorf("tool arguments for %s must decode to a JSON object", toolCall.Function.Name)
				}
				result, err = function(arguments)
				if err != nil {
					return "", err
				}
			}

			messages = append(messages, map[string]any{
				"role":         "tool",
				"tool_call_id": toolCall.ID,
				"content":      result,
			})
		}
	}

	return "Max iterations reached", nil
}

func parseArguments(raw string) (any, error) {
	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

func (a *Agent) logf(format string, arguments ...any) {
	if a.logWriter == nil {
		return
	}
	fmt.Fprintf(a.logWriter, format, arguments...)
}
