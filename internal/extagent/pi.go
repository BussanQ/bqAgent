package extagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"bqagent/internal/media"
)

func (adapter CLIAdapter) runPi(ctx context.Context, spec CommandSpec, state SessionState, cwd, prompt string, images []media.Image) (TurnResponse, error) {
	args := append([]string{}, spec.Args...)
	if state.ExternalSessionID != "" {
		args = append(args, "--session", state.ExternalSessionID)
	}
	paths, cleanup, err := cliImageFiles(images)
	if err != nil {
		return TurnResponse{}, err
	}
	defer cleanup()
	for _, path := range paths {
		args = append(args, "@"+path)
	}
	if len(paths) > 0 && prompt == "" {
		prompt = "请查看所附图片。"
	}
	command := exec.CommandContext(ctx, spec.Command, args...)
	command.Dir = cwd
	// Stdin preserves multiline/shared context and prevents leading @ or - from
	// being interpreted as Pi file attachments or command-line options.
	command.Stdin = strings.NewReader(prompt)
	configureExternalProcess(command)
	var reply, sessionID string
	err = runCLIStream(ctx, command, func(r io.Reader) (err error) {
		reply, sessionID, err = readPiJSON(r, ActivitySinkFromContext(ctx))
		return err
	})
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
	return readPiJSON(bytes.NewReader(output), nil)
}

type piEvent struct {
	FinalError                                        string
	WillRetry                                         bool
	Steering, FollowUp                                []string
	Title, Placeholder, Prefill                       string
	Options                                           json.RawMessage
	Timeout                                           float64
	Type, Command, Method, ToolCallID, ToolName       string
	ID                                                piRequestID
	Success, Aborted, IsError                         bool
	Message, Args, PartialResult, Result, Data, Error json.RawMessage
	AssistantMessageEvent                             struct{ Type, Delta string }
}

// Pi extensions may send a numeric request ID even though our own RPC
// commands use strings. A response must echo the original JSON type.
type piRequestID struct {
	text   string
	number json.Number
}

func (id *piRequestID) UnmarshalJSON(data []byte) error {
	*id = piRequestID{}
	if len(data) > 0 && (data[0] == '"' || data[0] == 'n') {
		return json.Unmarshal(data, &id.text)
	}
	return json.Unmarshal(data, &id.number)
}

func (id piRequestID) value() any {
	if id.number != "" {
		return id.number
	}
	return id.text
}

type piOutput struct {
	editPaths                 map[string]string
	reply, sessionID, failure string
	streamed                  strings.Builder
	messageIndex              int
}

func readPiJSON(reader io.Reader, sink ActivitySink) (string, string, error) {
	decoder := json.NewDecoder(reader)
	var output piOutput
	for {
		var event piEvent
		if err := decoder.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			return "", "", fmt.Errorf("invalid pi JSON output: %w", err)
		}
		if _, err := output.observe(event, sink); err != nil {
			return "", "", err
		}
	}
	return output.final()
}

// Both JSON and RPC modes publish the same native message and tool events.
// A user echo identifies queued input Pi actually delivered to its agent.
func (p *piOutput) observe(event piEvent, sink ActivitySink) (*string, error) {
	switch event.Type {
	case "message_update":
		if sink != nil && event.AssistantMessageEvent.Type == "text_delta" && event.AssistantMessageEvent.Delta != "" {
			p.streamed.WriteString(event.AssistantMessageEvent.Delta)
			sink(Activity{TextID: fmt.Sprint(p.messageIndex), Text: event.AssistantMessageEvent.Delta})
		}
	case "tool_execution_start", "tool_execution_update", "tool_execution_end":
		if sink != nil && event.ToolCallID != "" {
			e := Activity{CallID: event.ToolCallID, Name: event.ToolName, Input: activityInput(event.Args), Status: "running", Output: activityOutput(event.PartialResult)}
			if event.ToolName == "edit" || event.ToolName == "write" || event.ToolName == "apply_patch" || event.ToolName == "bash" {
				e.Edits = editInput(event.ToolName, e.Input)
				if path, ok := inputText(e.Input, "path"); ok && len(path) <= 4096 && event.ToolName == "edit" {
					if p.editPaths == nil {
						p.editPaths = map[string]string{}
					}
					p.editPaths[e.CallID] = path
				}
			}
			if event.Type == "tool_execution_end" {
				e.Output = activityOutput(event.Result)
				e.Status = "succeeded"
				if event.IsError {
					e.Status = "failed"
				} else if patch := piFileEdits(p.editPaths[e.CallID], event.Result); patch != nil {
					e.Edits = patch
				}
				delete(p.editPaths, e.CallID)
			}
			sink(e)
		}
	case "session":
		p.sessionID = strings.TrimSpace(event.ID.text)
	case "agent_settled":
		if event.Aborted {
			p.failure = "request aborted"
		}
	case "auto_retry_end":
		if event.Success {
			p.failure = ""
		} else {
			p.failure = firstNonEmpty(event.FinalError, "Pi 自动重试失败")
		}
	case "compaction_end", "auto_compaction_end":
		if event.WillRetry {
			p.failure = ""
		}
	case "message_end":
		var message struct {
			Role, StopReason, ErrorMessage string
			Content                        json.RawMessage
		}
		if err := json.Unmarshal(event.Message, &message); err != nil {
			return nil, fmt.Errorf("invalid pi message: %w", err)
		}
		if message.Role == "user" {
			text := extractACPText(message.Content)
			return &text, nil
		}
		if message.Role != "assistant" {
			return nil, nil
		}
		defer p.streamed.Reset()
		defer func() { p.messageIndex++ }()
		p.reply, p.failure = "", ""
		if message.StopReason == "error" || message.StopReason == "aborted" {
			p.failure = firstNonEmpty(message.ErrorMessage, message.StopReason)
			return nil, nil
		}
		var content []struct{ Type, Text string }
		if err := json.Unmarshal(message.Content, &content); err != nil {
			return nil, fmt.Errorf("invalid pi assistant content: %w", err)
		}
		var parts []string
		for _, block := range content {
			if block.Type == "text" {
				parts = append(parts, block.Text)
			}
		}
		text := strings.Join(parts, "\n")
		if sink != nil {
			if rest, ok := strings.CutPrefix(text, p.streamed.String()); !ok {
				sink(Activity{TextID: fmt.Sprint(p.messageIndex), Text: text, TextSnapshot: true})
			} else if rest != "" {
				sink(Activity{TextID: fmt.Sprint(p.messageIndex), Text: rest})
			}
		}
		if message.StopReason != "toolUse" {
			p.reply = strings.TrimSpace(text)
		}
	}
	return nil, nil
}

func (p *piOutput) final() (string, string, error) {
	if p.failure != "" {
		return "", "", fmt.Errorf("pi: %s", p.failure)
	}
	if p.sessionID == "" {
		return "", "", fmt.Errorf("pi did not return a session ID")
	}
	if p.reply == "" {
		return "", "", fmt.Errorf("pi returned an empty final response")
	}
	return p.reply, p.sessionID, nil
}
