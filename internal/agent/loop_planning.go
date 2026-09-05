package agent

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (a *Agent) RunPlanned(ctx context.Context, task string, maxIterations int) (string, error) {
	messages := a.prompt.Messages()
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

// buildRequestMessages returns the message payload to send to the model and,
// when pruning or summarization compacted the history, a non-nil bounded working
// set the loop should adopt in place of its full in-memory history. This working
// set can be persisted separately from the complete raw transcript. The
// synthetic summary lives only in the working set and context checkpoint; it is
// never recorded to messages.jsonl.
