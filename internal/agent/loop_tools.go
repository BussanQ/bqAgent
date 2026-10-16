package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"bqagent/internal/tools"
	apptrace "bqagent/internal/trace"
)

func replayableToolCalls(toolCalls []ToolCall) []ToolCall {
	filtered := make([]ToolCall, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		if strings.TrimSpace(toolCall.ID) == "" || strings.TrimSpace(toolCall.Function.Name) == "" || strings.TrimSpace(toolCall.Function.Arguments) == "" {
			continue
		}
		var arguments map[string]any
		if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &arguments); err != nil || arguments == nil {
			continue
		}
		filtered = append(filtered, toolCall)
	}
	return filtered
}

func (a *Agent) appendEmptyFinalRecovery(messages []map[string]any) ([]map[string]any, error) {
	recoveryMessage := map[string]any{
		"role":    "user",
		"content": emptyFinalRecoveryPrompt,
	}
	updated := append(messages, recoveryMessage)
	if err := a.recordMessages(recoveryMessage); err != nil {
		return updated, err
	}
	return updated, nil
}

func (a *Agent) appendTruncatedToolRecovery(messages []map[string]any, toolCalls []ToolCall, attempt int) ([]map[string]any, error) {
	updated := messages
	for _, toolCall := range toolCalls {
		a.emitToolResult(toolCall, truncatedToolBatchError, 0, true)
		var err error
		updated, err = a.appendToolMessage(updated, toolCall.ID, truncatedToolBatchError)
		if err != nil {
			return updated, err
		}
	}
	if attempt > maxTruncatedToolRecoveries {
		finalMessage := map[string]any{"role": "assistant", "content": truncatedToolRecoveryStoppedMessage}
		updated = append(updated, finalMessage)
		if err := a.recordMessages(finalMessage); err != nil {
			return updated, err
		}
		return updated, nil
	}
	recoveryMessage := map[string]any{
		"role":    "user",
		"content": "The previous assistant response reached its output-token limit while forming tool calls. No tools from that batch were executed. Reissue every required tool call in one complete response with complete JSON object arguments. Do not assume any side effects occurred.",
	}
	updated = append(updated, recoveryMessage)
	if err := a.recordMessages(recoveryMessage); err != nil {
		return updated, err
	}
	return updated, nil
}

func parseArguments(raw string) (any, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("tool arguments are empty")
	}

	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		if count := concatenatedArgumentObjects(raw); count > 1 {
			return nil, fmt.Errorf("%w; arguments contain %d concatenated JSON objects. This tool call was not executed. Reissue each intended operation as a separate tool call with a unique call ID and exactly one JSON object in arguments. Do not concatenate objects or discard any intended operation", err, count)
		}
		return nil, err
	}
	return parsed, nil
}

// Count only a complete sequence of objects. Never silently accept a valid
// prefix, combine duplicate keys, or choose one command from malformed input.
func concatenatedArgumentObjects(raw string) int {
	decoder := json.NewDecoder(strings.NewReader(raw))
	count := 0
	for {
		var object map[string]json.RawMessage
		err := decoder.Decode(&object)
		if err == io.EOF {
			return count
		}
		if err != nil || object == nil {
			return 0
		}
		count++
	}
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

func (a *Agent) toolDefinitionsForRun() []tools.Definition {
	filtered := make([]tools.Definition, 0, len(a.toolDefinitions))
	for _, definition := range a.toolDefinitions {
		if definition.Function.Name == "web_search" && a.webSearchAvailable != nil && !a.webSearchAvailable() {
			continue
		}
		filtered = append(filtered, definition)
	}
	return filtered
}

func (a *Agent) appendToolError(messages []map[string]any, toolCallID, format string, arguments ...any) ([]map[string]any, error) {
	return a.appendToolMessage(messages, toolCallID, "Error: "+fmt.Sprintf(format, arguments...))
}

// hasSpecialToolCalls reports whether the batch contains a tool that must run
// sequentially because it recurses or mutates state later calls may depend on.
func (a *Agent) hasSpecialToolCalls(toolCalls []ToolCall) bool {
	for _, toolCall := range toolCalls {
		// A possibly side-effecting shell command must not overlap other calls
		// in its batch: they could observe half-applied changes, and the
		// verification ledger relies on a clear order.
		if toolCall.Function.Name == "execute_bash" && len(toolCalls) > 1 {
			var arguments struct {
				Command string `json:"command"`
			}
			if json.Unmarshal([]byte(toolCall.Function.Arguments), &arguments) != nil || classifyBash(arguments.Command) == bashMutating {
				return true
			}
		}
		switch toolCall.Function.Name {
		case "write_file", "edit_file", "task_update", "task_scope":
			return true
		case "consult_group_agent":
			return true
		}
	}
	return false
}

// runRegularToolCall parses, dispatches, and times one non-special tool call,
// returning its tool_call_id and the result content (errors are rendered as
// "Error: ..." content, matching appendToolError). It is safe to call from
// multiple goroutines; the log/progress writers are synchronized.
const maxToolEventPreviewRunes = 4 * 1024

func (a *Agent) emitToolEvent(event ToolEvent) {
	if a == nil || a.toolEventSink == nil {
		return
	}
	event.Seq = a.toolEventSeq.Add(1)
	a.toolEventSink.EmitToolEvent(event)
}

func (a *Agent) emitToolResult(toolCall ToolCall, result string, duration time.Duration, failed bool) {
	preview, truncated := boundedToolEventPreview(result)
	status := "succeeded"
	if failed {
		status = "failed"
	}
	a.emitToolEvent(ToolEvent{
		Kind: "tool_result", ID: toolCall.ID, Name: toolCall.Function.Name,
		Status: status, Preview: preview, DurationMS: duration.Milliseconds(), Truncated: truncated,
	})
}

func boundedToolEventPreview(value string) (string, bool) {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= maxToolEventPreviewRunes {
		return string(runes), false
	}
	return string(runes[:maxToolEventPreviewRunes]) + "\n... [preview truncated]", true
}

func boundedToolEventArguments(arguments map[string]any) map[string]any {
	redacted := apptrace.RedactMap(arguments)
	bounded, _ := boundToolEventValue(redacted, 0).(map[string]any)
	return bounded
}

func boundToolEventValue(value any, depth int) any {
	if depth >= 5 {
		return "[depth limit]"
	}
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any)
		count := 0
		for key, item := range typed {
			if count >= 64 {
				result["..."] = "[entry limit]"
				break
			}
			result[key] = boundToolEventValue(item, depth+1)
			count++
		}
		return result
	case []any:
		limit := len(typed)
		if limit > 32 {
			limit = 32
		}
		result := make([]any, 0, limit+1)
		for _, item := range typed[:limit] {
			result = append(result, boundToolEventValue(item, depth+1))
		}
		if len(typed) > limit {
			result = append(result, "[item limit]")
		}
		return result
	case string:
		preview, _ := boundedToolEventPreview(typed)
		if len([]rune(preview)) > 1024 {
			return string([]rune(preview)[:1024]) + "..."
		}
		return preview
	default:
		return value
	}
}

func (a *Agent) runRegularToolCall(ctx context.Context, toolCall ToolCall) (string, string) {
	toolStartedAt := time.Now()
	parsedArguments, err := parseArguments(toolCall.Function.Arguments)
	if err != nil {
		result := fmt.Sprintf("Error: Invalid JSON arguments for tool %q: %v", toolCall.Function.Name, err)
		a.emitToolEvent(ToolEvent{Kind: "tool_start", ID: toolCall.ID, Name: toolCall.Function.Name, Status: "running"})
		a.emitToolResult(toolCall, result, time.Since(toolStartedAt), true)
		return toolCall.ID, result
	}
	a.logf("[Tool] %s(%v)\n", toolCall.Function.Name, parsedArguments)

	arguments, ok := parsedArguments.(map[string]any)
	if !ok {
		result := fmt.Sprintf("Error: Tool arguments for %q must decode to a JSON object", toolCall.Function.Name)
		a.emitToolEvent(ToolEvent{Kind: "tool_start", ID: toolCall.ID, Name: toolCall.Function.Name, Status: "running"})
		a.emitToolResult(toolCall, result, time.Since(toolStartedAt), true)
		return toolCall.ID, result
	}
	a.emitToolEvent(ToolEvent{
		Kind: "tool_start", ID: toolCall.ID, Name: toolCall.Function.Name,
		Status: "running", Arguments: boundedToolEventArguments(arguments),
	})
	if path, _ := arguments["path"].(string); strings.TrimSpace(path) != "" {
		a.writeStageProgress(fmt.Sprintf("Running %s on %s", toolCall.Function.Name, path))
	} else {
		a.writeStageProgress(fmt.Sprintf("Running tool %s", toolCall.Function.Name))
	}

	toolResult := ""
	var toolErr error
	function, ok := a.functions[toolCall.Function.Name]
	if !ok {
		toolErr = fmt.Errorf("unknown tool '%s'", toolCall.Function.Name)
		toolResult = fmt.Sprintf("Error: Unknown tool '%s'", toolCall.Function.Name)
	} else {
		run := turnRun(ctx)
		var entry *execEntry
		if run != nil {
			entry = run.ledger.begin(toolCall.ID, toolCall.Function.Name, arguments)
		}
		toolResult, toolErr = function(ctx, arguments)
		if toolErr != nil {
			toolResult = formatToolError(toolErr, toolResult)
		}
		if run != nil {
			succeeded := toolErr == nil && !strings.HasPrefix(strings.TrimSpace(toolResult), "Error:")
			run.ledger.finish(entry, succeeded)
		}
	}
	duration := time.Since(toolStartedAt)
	logToolTiming(a.logWriter, toolCall.Function.Name, duration, toolErr)
	status := "completed"
	if toolErr != nil {
		status = "failed"
	}
	a.writeStageProgress(fmt.Sprintf("Tool %s %s", toolCall.Function.Name, status))
	a.emitToolResult(toolCall, toolResult, duration, toolErr != nil)
	if a.trace != nil {
		a.trace.ToolCall(toolCall.Function.Name, arguments, toolResult, duration, toolErr)
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
