package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"bqagent/internal/tools"
)

type Planner struct {
	client ChatCompletionClient
	model  string
}

type chatCompletionOptionsClient interface {
	CreateChatCompletionWithOptions(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions) (AssistantMessage, error)
}

func NewPlanner(client ChatCompletionClient, model string) *Planner {
	if model == "" {
		model = DefaultModel
	}
	return &Planner{client: client, model: model}
}

func (p *Planner) Generate(ctx context.Context, task string) ([]string, error) {
	messages := []map[string]any{
		{"role": "system", "content": "Break task into 3-5 steps. Return JSON with a 'steps' array of strings."},
		{"role": "user", "content": task},
	}

	var (
		message AssistantMessage
		err     error
	)
	if client, ok := p.client.(chatCompletionOptionsClient); ok {
		message, err = client.CreateChatCompletionWithOptions(ctx, p.model, messages, nil, ChatCompletionOptions{
			ResponseFormat: map[string]any{"type": "json_object"},
		})
	} else {
		message, err = p.client.CreateChatCompletion(ctx, p.model, messages, nil)
	}
	if err != nil {
		return nil, err
	}

	steps, err := parseSteps(message.FinalContent())
	if err != nil {
		return nil, err
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("planner returned no steps")
	}
	return steps, nil
}

func parseSteps(raw string) ([]string, error) {
	var direct struct {
		Steps []string `json:"steps"`
	}
	if err := json.Unmarshal([]byte(raw), &direct); err == nil && len(direct.Steps) > 0 {
		return cleanSteps(direct.Steps), nil
	}

	var generic map[string]any
	if err := json.Unmarshal([]byte(raw), &generic); err != nil {
		return nil, err
	}

	entries, ok := generic["steps"].([]any)
	if !ok {
		return nil, fmt.Errorf("planner response missing steps array")
	}

	steps := make([]string, 0, len(entries))
	for _, entry := range entries {
		text, ok := entry.(string)
		if !ok {
			return nil, fmt.Errorf("planner steps must be strings")
		}
		steps = append(steps, text)
	}
	return cleanSteps(steps), nil
}

func cleanSteps(steps []string) []string {
	cleaned := make([]string, 0, len(steps))
	for _, step := range steps {
		step = strings.TrimSpace(step)
		if step == "" {
			continue
		}
		cleaned = append(cleaned, step)
	}
	return cleaned
}
