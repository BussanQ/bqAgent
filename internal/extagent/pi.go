package extagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

func (adapter CLIAdapter) runPi(ctx context.Context, spec CommandSpec, state SessionState, cwd, prompt string) (TurnResponse, error) {
	args := append([]string{}, spec.Args...)
	if state.ExternalSessionID != "" {
		args = append(args, "--session", state.ExternalSessionID)
	}
	command := exec.CommandContext(ctx, spec.Command, args...)
	command.Dir = cwd
	// Stdin preserves multiline/shared context and prevents leading @ or - from
	// being interpreted as Pi file attachments or command-line options.
	command.Stdin = strings.NewReader(prompt)
	configureExternalProcess(command)
	output, err := command.Output()
	if ctx.Err() != nil {
		return TurnResponse{}, ctx.Err()
	}
	if err != nil {
		return TurnResponse{}, formatCLIError(err)
	}
	reply, sessionID, err := parsePiJSON(output)
	if err != nil {
		return TurnResponse{}, err
	}
	state.ExternalSessionID = sessionID
	return TurnResponse{Reply: reply, State: state}, nil
}

// parsePiJSON reads authoritative message_end events, ignoring deltas, tool
// results and thinking blocks. Later successful responses supersede retry
// errors. JSON mode can exit zero even when the final assistant message failed.
func parsePiJSON(output []byte) (string, string, error) {
	decoder := json.NewDecoder(bytes.NewReader(output))
	var reply, sessionID, failure string
	for {
		var event struct {
			Type    string          `json:"type"`
			ID      string          `json:"id"`
			Aborted bool            `json:"aborted"`
			Message json.RawMessage `json:"message"`
		}
		if err := decoder.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			return "", "", fmt.Errorf("invalid pi JSON output: %w", err)
		}
		switch event.Type {
		case "session":
			sessionID = strings.TrimSpace(event.ID)
		case "agent_settled":
			if event.Aborted {
				failure = "request aborted"
			}
		case "message_end":
			var message struct {
				Role         string          `json:"role"`
				Content      json.RawMessage `json:"content"`
				StopReason   string          `json:"stopReason"`
				ErrorMessage string          `json:"errorMessage"`
			}
			if err := json.Unmarshal(event.Message, &message); err != nil {
				return "", "", fmt.Errorf("invalid pi message: %w", err)
			}
			if message.Role != "assistant" {
				continue
			}
			reply, failure = "", ""
			if message.StopReason == "error" || message.StopReason == "aborted" {
				failure = firstNonEmpty(message.ErrorMessage, message.StopReason)
				continue
			}
			var content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal(message.Content, &content); err != nil {
				return "", "", fmt.Errorf("invalid pi assistant content: %w", err)
			}
			var parts []string
			for _, block := range content {
				if block.Type == "text" {
					parts = append(parts, block.Text)
				}
			}
			reply = strings.TrimSpace(strings.Join(parts, "\n"))
			if message.StopReason == "toolUse" {
				reply = "" // Tool preambles alone are not a completed reply.
			}
		}
	}
	if failure != "" {
		return "", "", fmt.Errorf("pi: %s", failure)
	}
	if sessionID == "" {
		return "", "", fmt.Errorf("pi did not return a session ID")
	}
	if reply == "" {
		return "", "", fmt.Errorf("pi returned an empty final response")
	}
	return reply, sessionID, nil
}
