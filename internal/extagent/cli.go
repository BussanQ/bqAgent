package extagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type CLIAdapter struct{}

func (adapter CLIAdapter) SendTurn(ctx context.Context, spec CommandSpec, state SessionState, cwd, prompt string) (TurnResponse, error) {
	switch state.Agent {
	case AgentClaude:
		return adapter.runClaude(ctx, spec, state, cwd, prompt)
	case AgentCodex:
		return adapter.runCodex(ctx, spec, state, cwd, prompt)
	default:
		return TurnResponse{}, fmt.Errorf("cli is not supported for %s", state.Agent)
	}
}

func (adapter CLIAdapter) runClaude(ctx context.Context, spec CommandSpec, state SessionState, cwd, prompt string) (TurnResponse, error) {
	args := append([]string{}, spec.Args...)
	if state.ExternalSessionID != "" {
		args = append([]string{"-r", state.ExternalSessionID}, args...)
	}
	args = append(args, prompt)
	command := exec.CommandContext(ctx, spec.Command, args...)
	command.Dir = cwd
	output, err := command.Output()
	if err != nil {
		return TurnResponse{}, formatCLIError(err)
	}
	reply, sessionID := parseClaudeJSON(output)
	if strings.TrimSpace(reply) == "" {
		reply = strings.TrimSpace(string(output))
	}
	state.ExternalSessionID = firstNonEmpty(sessionID, state.ExternalSessionID)
	return TurnResponse{Reply: reply, State: state}, nil
}

func (adapter CLIAdapter) runCodex(ctx context.Context, spec CommandSpec, state SessionState, cwd, prompt string) (TurnResponse, error) {
	outputFile, err := os.CreateTemp("", "bqagent-codex-*.txt")
	if err != nil {
		return TurnResponse{}, err
	}
	outputPath := outputFile.Name()
	_ = outputFile.Close()
	defer os.Remove(outputPath)

	args := codexArgs(spec, state.ExternalSessionID)
	args = append(args, "--output-last-message", outputPath)
	args = append(args, prompt)

	command := exec.CommandContext(ctx, spec.Command, args...)
	command.Dir = cwd
	output, err := command.Output()
	if err != nil {
		return TurnResponse{}, formatCLIError(err)
	}
	replyContent, _ := os.ReadFile(outputPath)
	reply := strings.TrimSpace(string(replyContent))
	if reply == "" {
		reply = strings.TrimSpace(string(output))
	}
	state.ExternalSessionID = firstNonEmpty(extractSessionIDFromJSONLines(output), state.ExternalSessionID)
	return TurnResponse{Reply: reply, State: state}, nil
}

func codexArgs(spec CommandSpec, resumeID string) []string {
	if strings.TrimSpace(resumeID) == "" {
		return append([]string{}, spec.Args...)
	}
	args := []string{"exec", "resume", strings.TrimSpace(resumeID)}
	for _, arg := range spec.Args {
		trimmed := strings.TrimSpace(arg)
		switch trimmed {
		case "", "exec":
			continue
		case "resume":
			continue
		default:
			args = append(args, arg)
		}
	}
	return args
}

func parseClaudeJSON(payload []byte) (reply string, sessionID string) {
	var result struct {
		Result    string `json:"result"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(payload, &result); err == nil {
		return strings.TrimSpace(result.Result), strings.TrimSpace(result.SessionID)
	}
	return "", ""
}

func extractSessionIDFromJSONLines(payload []byte) string {
	for _, line := range strings.Split(string(payload), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var decoded any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			continue
		}
		if id := findStringField(decoded, "thread_id", "session_id", "sessionId"); id != "" && looksLikeSessionID(id) {
			return id
		}
	}
	return ""
}

func findStringField(value any, keys ...string) string {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range keys {
			if text, ok := typed[key].(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
		for _, item := range typed {
			if text := findStringField(item, keys...); text != "" {
				return text
			}
		}
	case []any:
		for _, item := range typed {
			if text := findStringField(item, keys...); text != "" {
				return text
			}
		}
	}
	return ""
}

var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{6,}$`)

func looksLikeSessionID(value string) bool {
	return sessionIDPattern.MatchString(strings.TrimSpace(value))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func formatCLIError(err error) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return err
	}
	stderr := strings.TrimSpace(string(exitErr.Stderr))
	if stderr == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, stderr)
}

func logPath(root, name string) string {
	return filepath.Join(root, name)
}
