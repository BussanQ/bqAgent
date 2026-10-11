package main

import (
	"fmt"
	"strings"

	"bqagent/internal/agent"
)

func (backend *tuiBackend) currentEffort() agent.ReasoningEffort {
	backend.effortMu.RLock()
	defer backend.effortMu.RUnlock()
	return backend.effort
}

// Like the WebUI selector, this is a UI preference passed on each turn, not a
// change to provider defaults or external CLI/ACP agent configuration.
func (backend *tuiBackend) handleEffortCommand(message string) (string, bool, error) {
	fields := strings.Fields(message)
	if len(fields) == 0 || !strings.EqualFold(fields[0], "/effort") {
		return "", false, nil
	}
	if len(fields) > 2 {
		return "", true, fmt.Errorf("用法：/effort [auto|low|medium|high|max]")
	}
	if len(fields) == 2 {
		effort, err := agent.ParseReasoningEffort(fields[1])
		if err != nil {
			return "", true, fmt.Errorf("用法：/effort [auto|low|medium|high|max]：%w", err)
		}
		backend.effortMu.Lock()
		backend.effort = effort
		backend.effortMu.Unlock()
	}
	value := string(backend.currentEffort())
	if value == "" {
		value = "auto（模型默认）"
	}
	return "当前推理努力程度：" + value + "\n使用 /effort auto|low|medium|high|max 切换，作用于当前 TUI 后续模型 API 请求。", true, nil
}
