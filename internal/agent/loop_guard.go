package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"bqagent/internal/tools"
)

type loopGuard struct {
	config              StageConfig
	callCounts          map[string]int
	failureCounts       map[string]int
	pathFailures        map[string]int
	lastTodoProgress    string
	hasLastTodoProgress bool
	todoNoProgressCount int
	lastEmptySearch     string
	emptySearchCount    int
}

type loopGuardAction struct {
	recoveryPrompt   string
	checkpointReason string
	todoCheckpoint   bool
}

func (action loopGuardAction) merge(next loopGuardAction) loopGuardAction {
	if action.checkpointReason == "" || (action.todoCheckpoint && next.checkpointReason != "" && !next.todoCheckpoint) {
		action.checkpointReason = next.checkpointReason
		action.todoCheckpoint = next.todoCheckpoint
	}
	if action.recoveryPrompt == "" {
		action.recoveryPrompt = next.recoveryPrompt
	}
	return action
}

func newLoopGuard(config StageConfig) *loopGuard {
	return &loopGuard{config: config, callCounts: map[string]int{}, failureCounts: map[string]int{}, pathFailures: map[string]int{}}
}

func (guard *loopGuard) observeTool(call ToolCall, result string) loopGuardAction {
	if guard == nil {
		return loopGuardAction{}
	}
	failed := strings.HasPrefix(strings.TrimSpace(result), "Error:")
	if progress, ok := todoProgressSignature(call); ok {
		if guard.hasLastTodoProgress && guard.lastTodoProgress == progress {
			guard.todoNoProgressCount++
			if guard.todoNoProgressCount == 1 {
				return loopGuardAction{recoveryPrompt: todoNoProgressRecoveryPrompt}
			}
			return loopGuardAction{checkpointReason: "loop protection: repeated todo_write without task progress after recovery", todoCheckpoint: true}
		}
		guard.lastTodoProgress = progress
		guard.hasLastTodoProgress = true
		guard.todoNoProgressCount = 0
	} else if emptySearch := emptySearchSignature(call, result); emptySearch != "" {
		if guard.lastEmptySearch == emptySearch {
			guard.emptySearchCount++
			if guard.emptySearchCount == 2 {
				return loopGuardAction{recoveryPrompt: emptySearchRecoveryPrompt}
			}
			if guard.emptySearchCount >= 3 {
				return loopGuardAction{checkpointReason: fmt.Sprintf("loop protection: repeated empty search in %s after recovery", call.Function.Name)}
			}
		} else {
			guard.lastEmptySearch = emptySearch
			guard.emptySearchCount = 1
		}
	} else if toolResultMadeProgress(call, result) {
		guard.lastTodoProgress = ""
		guard.hasLastTodoProgress = false
		guard.todoNoProgressCount = 0
		guard.lastEmptySearch = ""
		guard.emptySearchCount = 0
	}
	if !guard.config.LoopProtection {
		return loopGuardAction{}
	}
	signature := call.Function.Name + "\x00" + strings.TrimSpace(call.Function.Arguments)
	guard.callCounts[signature]++
	if failed {
		guard.failureCounts[signature]++
		if guard.failureCounts[signature] >= guard.config.RepeatedFailureLimit {
			return loopGuardAction{checkpointReason: fmt.Sprintf("loop protection: repeated failing tool call %s", call.Function.Name)}
		}
		if pathSignature := failedPathSignature(call); pathSignature != "" {
			guard.pathFailures[pathSignature]++
			if guard.pathFailures[pathSignature] >= guard.config.RepeatedFailureLimit {
				return loopGuardAction{checkpointReason: fmt.Sprintf("loop protection: repeated failing path in %s", call.Function.Name)}
			}
		}
	}
	if guard.callCounts[signature] >= guard.config.DuplicateCallLimit {
		return loopGuardAction{checkpointReason: fmt.Sprintf("loop protection: repeated tool call %s", call.Function.Name)}
	}
	return loopGuardAction{}
}

func todoProgressSignature(call ToolCall) (string, bool) {
	if call.Function.Name != "todo_write" {
		return "", false
	}
	fallback := strings.TrimSpace(call.Function.Arguments)
	parsed, err := parseArguments(call.Function.Arguments)
	if err != nil {
		return fallback, true
	}
	arguments, ok := parsed.(map[string]any)
	if !ok {
		return fallback, true
	}
	raw, ok := arguments["todos"]
	if !ok {
		return fallback, true
	}
	var encoded []byte
	if text, isString := raw.(string); isString {
		encoded = []byte(strings.TrimSpace(text))
	} else {
		encoded, err = json.Marshal(raw)
		if err != nil {
			return fallback, true
		}
	}
	var items []tools.TodoItem
	if err := json.Unmarshal(encoded, &items); err != nil {
		return fallback, true
	}
	type todoProgress struct {
		Content string `json:"content"`
		Status  string `json:"status"`
	}
	progress := make([]todoProgress, 0, len(items))
	for _, item := range items {
		progress = append(progress, todoProgress{Content: strings.TrimSpace(item.Content), Status: item.Status})
	}
	encoded, err = json.Marshal(progress)
	if err != nil {
		return fallback, true
	}
	return string(encoded), true
}

func toolResultMadeProgress(call ToolCall, result string) bool {
	trimmed := strings.TrimSpace(result)
	if trimmed == "" || strings.HasPrefix(trimmed, "Error:") {
		return false
	}
	switch call.Function.Name {
	case "glob":
		return trimmed != "No files matched."
	case "grep":
		return trimmed != "No matches found."
	default:
		return true
	}
}

func emptySearchSignature(call ToolCall, result string) string {
	trimmed := strings.TrimSpace(result)
	switch call.Function.Name {
	case "glob":
		if trimmed == "No files matched." {
			return call.Function.Name + "\x00" + strings.TrimSpace(call.Function.Arguments)
		}
	case "grep":
		if trimmed == "No matches found." {
			return call.Function.Name + "\x00" + strings.TrimSpace(call.Function.Arguments)
		}
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
