package server

import (
	"fmt"
	"strings"

	"bqagent/internal/agent"
	"bqagent/internal/tools"
)

type ChatMode string

const (
	ChatModeRun ChatMode = "run"
	ChatModeAsk ChatMode = "ask"
)

const askModeSystemPrompt = `# Ask mode

The current conversation is in Ask mode. Provide read-only question answering and analysis only.
You may inspect workspace files and retrieve information with the available read-only tools. Do not modify, create, rename, or delete files; do not execute shell commands; do not install anything; do not change memory; and do not start or control external coding agents or subagents.
If the user asks you to make a change or execute a command, explain that Ask mode is read-only and ask them to switch to Run mode with /run before repeating the request.`

var askModeReadOnlyTools = map[string]struct{}{
	"read_file":  {},
	"grep":       {},
	"glob":       {},
	"web_search": {},
	"web_fetch":  {},
}

func parseChatMode(value string) (ChatMode, error) {
	switch ChatMode(strings.ToLower(strings.TrimSpace(value))) {
	case "", ChatModeRun, "agent":
		return ChatModeRun, nil
	case ChatModeAsk:
		return ChatModeAsk, nil
	default:
		return "", fmt.Errorf("mode must be run or ask")
	}
}

func storedChatMode(value string) ChatMode {
	mode, err := parseChatMode(value)
	if err != nil {
		return ChatModeRun
	}
	return mode
}

func persistedChatMode(mode ChatMode) string {
	if mode == ChatModeAsk {
		return string(mode)
	}
	return ""
}

func promptForChatMode(prompt agent.PromptSnapshot, mode ChatMode) agent.PromptSnapshot {
	if mode != ChatModeAsk {
		return prompt
	}
	stable := strings.TrimSpace(prompt.Stable)
	if stable != "" {
		stable += "\n\n"
	}
	stable += askModeSystemPrompt
	return agent.NewFrozenPromptSnapshot(stable, prompt.SessionContext)
}

func toolsetForChatMode(mode ChatMode, definitions []tools.Definition, functions map[string]tools.Function) ([]tools.Definition, map[string]tools.Function) {
	if mode != ChatModeAsk {
		return definitions, functions
	}
	filteredDefinitions := make([]tools.Definition, 0, len(askModeReadOnlyTools))
	for _, definition := range definitions {
		if _, ok := askModeReadOnlyTools[definition.Function.Name]; ok {
			filteredDefinitions = append(filteredDefinitions, definition)
		}
	}
	filteredFunctions := make(map[string]tools.Function, len(askModeReadOnlyTools))
	for name, function := range functions {
		if _, ok := askModeReadOnlyTools[name]; ok {
			filteredFunctions[name] = function
		}
	}
	return filteredDefinitions, filteredFunctions
}

func chatModeCommand(message string) (ChatMode, bool) {
	command, _ := splitFirstToken(message)
	switch strings.ToLower(command) {
	case "/ask":
		return ChatModeAsk, true
	case "/run":
		return ChatModeRun, true
	default:
		return "", false
	}
}

func chatModeCommandReply(mode ChatMode) string {
	if mode == ChatModeAsk {
		return "已切换到 Ask 模式：只读问答，不会修改文件或执行命令。"
	}
	return "已切换到 Run 模式：已恢复完整工具能力。"
}

func askModeBlockedCommand(message string) bool {
	command, rest := splitFirstToken(message)
	switch strings.ToLower(command) {
	case "/agent", "/claude", "/codex", "/cursor", "/opencode", "/default":
		return true
	case "/memory":
		action, _ := splitFirstToken(rest)
		return !strings.EqualFold(action, "list") && !strings.EqualFold(action, "search")
	default:
		return false
	}
}

func askModeBlockedCommandReply() string {
	return "当前是 Ask 模式，只能进行只读问答，不能修改文件、执行命令或启动外部 Agent。请先输入 /run 切换到 Run 模式。"
}
