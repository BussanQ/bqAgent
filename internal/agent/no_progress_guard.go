package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"bqagent/internal/tools"
)

const (
	noProgressExactRepeatLimit   = 2
	noProgressFamilyRepeatLimit  = 4
	noProgressFinalizeAfterSkips = 2
)

const noProgressFinalizationReminder = `<system-reminder>
The previous tool calls were repeating without new progress. Do not use more tools. Provide the best final answer now based on the observations already gathered. If evidence is incomplete, state the uncertainty and what the user could do next.
</system-reminder>`

type noProgressGuard struct {
	exact              map[string]*toolRepeatEntry
	families           map[string]*toolRepeatEntry
	mutationGeneration int
	skipCount          int
	finalizeNext       bool
	finalizing         bool
}

type toolRepeatEntry struct {
	count int
}

type toolExecutionDecision struct {
	Fingerprint string
	Family      string
	Skipped     bool
	Content     string
}

func newNoProgressGuard() *noProgressGuard {
	return &noProgressGuard{
		exact:    make(map[string]*toolRepeatEntry),
		families: make(map[string]*toolRepeatEntry),
	}
}

func (guard *noProgressGuard) requestDefinitions(definitions []tools.Definition) []tools.Definition {
	if guard == nil || !guard.finalizeNext {
		return definitions
	}
	guard.finalizing = true
	return nil
}

func (guard *noProgressGuard) appendFinalizationReminder(messages []map[string]any) []map[string]any {
	if guard == nil || !guard.finalizeNext {
		return messages
	}
	reminded := duplicateMessages(messages)
	reminded = append(reminded, map[string]any{"role": "user", "content": noProgressFinalizationReminder})
	return reminded
}

func (guard *noProgressGuard) stoppedMessage() string {
	return "Agent stopped after repeated no-progress tool calls. Use the observations already gathered, or retry with a narrower instruction."
}

func (guard *noProgressGuard) before(toolCall ToolCall) toolExecutionDecision {
	fingerprint, family, ok := guard.fingerprint(toolCall)
	if !ok {
		return toolExecutionDecision{}
	}
	decision := toolExecutionDecision{Fingerprint: fingerprint, Family: family}
	exactEntry := guard.exact[fingerprint]
	if exactEntry != nil && exactEntry.count >= noProgressExactRepeatLimit {
		decision.Skipped = true
		decision.Content = guard.skipMessage(toolCall, fingerprint, family, "This exact tool call has already been run multiple times in this turn without new progress.")
		guard.noteSkipped()
		return decision
	}
	if family != "" {
		familyEntry := guard.families[family]
		if familyEntry != nil && familyEntry.count >= noProgressFamilyRepeatLimit {
			decision.Skipped = true
			decision.Content = guard.skipMessage(toolCall, fingerprint, family, "This family of tool calls has already been run multiple times in the same unchanged state without new progress.")
			guard.noteSkipped()
			return decision
		}
	}
	return decision
}

func (guard *noProgressGuard) after(toolCall ToolCall, decision toolExecutionDecision, content string) {
	if guard == nil || decision.Skipped || decision.Fingerprint == "" {
		return
	}
	entry := guard.exact[decision.Fingerprint]
	if entry == nil {
		entry = &toolRepeatEntry{}
		guard.exact[decision.Fingerprint] = entry
	}
	entry.count++
	if decision.Family != "" {
		familyEntry := guard.families[decision.Family]
		if familyEntry == nil {
			familyEntry = &toolRepeatEntry{}
			guard.families[decision.Family] = familyEntry
		}
		familyEntry.count++
	}
	if isMutationTool(toolCall.Function.Name) && !strings.HasPrefix(strings.TrimSpace(content), "Error:") {
		guard.mutationGeneration++
	}
}

func (guard *noProgressGuard) noteSkipped() {
	guard.skipCount++
	if guard.skipCount >= noProgressFinalizeAfterSkips {
		guard.finalizeNext = true
	}
}

func (guard *noProgressGuard) fingerprint(toolCall ToolCall) (string, string, bool) {
	arguments, err := parseArguments(toolCall.Function.Arguments)
	if err != nil {
		return "", "", false
	}
	args, ok := arguments.(map[string]any)
	if !ok {
		return "", "", false
	}
	name := strings.TrimSpace(toolCall.Function.Name)
	switch name {
	case "read_file":
		path := cleanPath(stringArg(args, "path"))
		if path == "" {
			return "", "", false
		}
		offset := strings.TrimSpace(stringArg(args, "offset"))
		limit := strings.TrimSpace(stringArg(args, "limit"))
		exact := fmt.Sprintf("read_file:path=%s;offset=%s;limit=%s;gen=%d", path, offset, limit, guard.mutationGeneration)
		family := fmt.Sprintf("read_file:path=%s;gen=%d", path, guard.mutationGeneration)
		return exact, family, true
	case "grep":
		pattern := strings.TrimSpace(stringArg(args, "pattern"))
		if pattern == "" {
			return "", "", false
		}
		path := cleanPath(stringArg(args, "path"))
		glob := strings.TrimSpace(stringArg(args, "glob"))
		ignoreCase := strings.TrimSpace(stringArg(args, "ignore_case"))
		maxResults := strings.TrimSpace(stringArg(args, "max_results"))
		exact := fmt.Sprintf("grep:pattern=%s;path=%s;glob=%s;ignore_case=%s;max_results=%s;gen=%d", pattern, path, glob, ignoreCase, maxResults, guard.mutationGeneration)
		family := fmt.Sprintf("grep:pattern=%s;path=%s;glob=%s;ignore_case=%s;gen=%d", pattern, path, glob, ignoreCase, guard.mutationGeneration)
		return exact, family, true
	case "execute_bash":
		command := normalizeToolCommand(stringArg(args, "command"))
		if command == "" {
			return "", "", false
		}
		exact := fmt.Sprintf("execute_bash:cmd=%s;gen=%d", command, guard.mutationGeneration)
		family := commandFamily(command, guard.mutationGeneration)
		return exact, family, true
	default:
		encoded := canonicalJSON(args)
		if encoded == "" {
			return "", "", false
		}
		exact := fmt.Sprintf("%s:args=%s;gen=%d", name, encoded, guard.mutationGeneration)
		return exact, "", true
	}
}

func (guard *noProgressGuard) skipMessage(toolCall ToolCall, fingerprint string, family string, reason string) string {
	parts := []string{
		"Error: Tool call skipped by no-progress guard.",
		"",
		reason,
		"",
		"Tool:",
		strings.TrimSpace(toolCall.Function.Name),
		"",
		"Fingerprint:",
		fingerprint,
	}
	if family != "" {
		parts = append(parts, "", "Family:", family)
	}
	parts = append(parts,
		"",
		"Do not repeat this tool call. Use the previous observation instead. If you already have enough evidence, provide the final answer now. Only call a different tool if it will answer a new, specific question.",
	)
	return strings.Join(parts, "\n")
}

func isMutationTool(name string) bool {
	switch name {
	case "write_file", "edit_file":
		return true
	default:
		return false
	}
}

func cleanPath(path string) string {
	path = strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
	if path == "" {
		return ""
	}
	path = filepath.ToSlash(filepath.Clean(path))
	if path == "." {
		return ""
	}
	return strings.TrimPrefix(path, "./")
}

func stringArg(args map[string]any, key string) string {
	value, ok := args[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprint(typed)
	}
}

func normalizeToolCommand(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	fields[0] = strings.ToLower(fields[0])
	if len(fields) > 1 && isCommandWithLowerSubcommand(fields[0]) {
		fields[1] = strings.ToLower(fields[1])
	}
	return strings.TrimSuffix(strings.Join(fields, " "), ";")
}

func isCommandWithLowerSubcommand(command string) bool {
	switch command {
	case "cargo", "go", "npm", "pnpm", "yarn", "rustup", "python", "python3", "pip":
		return true
	default:
		return false
	}
}

func commandFamily(command string, generation int) string {
	fields := strings.Fields(command)
	if len(fields) < 2 {
		return ""
	}
	if fields[0] != "cargo" {
		return ""
	}
	switch fields[1] {
	case "build":
		return fmt.Sprintf("execute_bash:cargo-build;gen=%d", generation)
	case "run":
		return fmt.Sprintf("execute_bash:cargo-run;gen=%d", generation)
	case "test":
		return fmt.Sprintf("execute_bash:cargo-test;gen=%d", generation)
	case "check":
		return fmt.Sprintf("execute_bash:cargo-check;gen=%d", generation)
	default:
		return ""
	}
}

func canonicalJSON(value any) string {
	var buffer bytes.Buffer
	writeCanonicalJSON(&buffer, value)
	if buffer.Len() == 0 {
		return ""
	}
	sum := sha256.Sum256(buffer.Bytes())
	return hex.EncodeToString(sum[:8])
}

func writeCanonicalJSON(buffer *bytes.Buffer, value any) {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		buffer.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				buffer.WriteByte(',')
			}
			encodedKey, _ := json.Marshal(key)
			buffer.Write(encodedKey)
			buffer.WriteByte(':')
			writeCanonicalJSON(buffer, typed[key])
		}
		buffer.WriteByte('}')
	case []any:
		buffer.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				buffer.WriteByte(',')
			}
			writeCanonicalJSON(buffer, item)
		}
		buffer.WriteByte(']')
	case string:
		encoded, _ := json.Marshal(collapseWhitespace(strings.TrimSpace(typed)))
		buffer.Write(encoded)
	default:
		encoded, _ := json.Marshal(typed)
		buffer.Write(encoded)
	}
}

func collapseWhitespace(value string) string {
	return strings.Join(strings.FieldsFunc(value, unicode.IsSpace), " ")
}
