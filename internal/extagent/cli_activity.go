package extagent

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

func readCodexCLI(r io.Reader, sink ActivitySink) (string, string, error) {
	decoder := json.NewDecoder(r)
	var id, reply, failure string
	var lastPlan *PlanUpdate
	for {
		var frame struct {
			Type     string
			ThreadID string `json:"thread_id"`
			Error    struct{ Message string }
			Item     struct {
				appServerItem
				Items  []codexTodo `json:"items"`
				Output string      `json:"aggregated_output"`
				Code   *int        `json:"exit_code"`
			}
		}
		if err := decoder.Decode(&frame); err == io.EOF {
			break
		} else if err != nil {
			return "", "", err
		}
		switch frame.Type {
		case "thread.started":
			id = frame.ThreadID
		case "turn.failed":
			failure = firstNonEmpty(frame.Error.Message, "Codex turn failed")
		case "item.started", "item.updated", "item.completed":
			item := frame.Item.appServerItem
			switch item.Type {
			case "reasoning":
				// codex exec's published ReasoningItem.text is a summary.
				// The App Server exposes a different schema for raw reasoning.
				if sink != nil && item.ID != "" && (frame.ThreadID == "" || frame.ThreadID == id) {
					update := &SummaryUpdate{ID: item.ID, Ended: frame.Type == "item.completed"}
					if item.Text != "" {
						update.Sections = []string{item.Text}
					}
					if update.Ended || len(update.Sections) != 0 {
						sink(Activity{Summary: update})
					}
				}
				continue
			case "todo_list":
				if item.ID != "" && frame.Item.Items != nil {
					lastPlan = cliSteps(item.ID, frame.Item.Items)
					if sink != nil {
						sink(Activity{Plan: lastPlan})
					}
				}
				continue
			case "agent_message":
				if frame.Type == "item.completed" {
					reply = item.Text
				}
				if sink != nil {
					sink(Activity{TextID: item.ID, Text: item.Text, TextSnapshot: true})
				}
				continue
			case "command_execution":
				item.Type = "commandExecution"
			case "mcp_tool_call":
				item.Type = "mcpToolCall"
			case "file_change":
				item.Type = "fileChange"
			case "web_search":
				item.Type = "webSearch"
			case "collab_tool_call":
				item.Type = "collabAgentToolCall"
			}
			if sink == nil {
				continue
			}
			item.AggregatedOutput, item.ExitCode = frame.Item.Output, frame.Item.Code
			if event, ok := codexItemActivity(item, frame.Type == "item.completed"); ok {
				sink(event)
			}
		}
	}
	if failure != "" {
		return "", id, fmt.Errorf("codex: %s", failure)
	}
	if reply == "" && lastPlan != nil {
		reply = lastPlan.Plan.ReplyText(lastPlan.Text)
	}
	return reply, id, nil
}

func readClaudeStream(r io.Reader, sink ActivitySink) (string, string, error) {
	return readClaudeContextStream(r, sink, "", nil, nil)
}

func readClaudeContextStream(r io.Reader, sink ActivitySink, expectedID string, report usageSink, control *claudeUsageControl) (string, string, error) {
	usage := claudeContextReader{id: expectedID}
	var host *claudePromptHost
	if control != nil && control.mode != nil {
		host = control.host
		defer control.finish()
	}
	editPaths := make(map[string]string)
	questionTools := make(map[string]bool)
	toolStart := func(id, name string, raw json.RawMessage) {
		if host != nil && name == "AskUserQuestion" {
			questionTools[id] = true
			sink(Activity{CallID: id, Name: name, Status: "running"})
			return
		}
		input := activityInput(raw)
		edits := editInput(name, input)
		if edits != nil && len(edits.Files) > 0 && (name == "Edit" || name == "MultiEdit" || name == "Write" || name == "str_replace") {
			editPaths[id] = edits.Files[0].Path
		}
		sink(Activity{CallID: id, Name: name, Status: "running", Input: input, Edits: edits})
	}
	type block struct {
		Type, ID, Name, Text string
		Input, Content       json.RawMessage
		CallID               string `json:"tool_use_id"`
		Failed               bool   `json:"is_error"`
	}
	decoder := json.NewDecoder(r)
	var id, reply, failure string
	var streamID string
	latestBlock := -1
	var messageNumber int
	texts := make(map[int]*strings.Builder)
	textID := func(index int) string {
		return fmt.Sprintf("%s/block/%d", firstNonEmpty(streamID, fmt.Sprintf("message/%d", messageNumber)), index)
	}
	for {
		var frame struct {
			RequestID             string `json:"request_id"`
			Request               json.RawMessage
			PermissionMode        string
			Response              claudeControlReply
			ToolResult            json.RawMessage `json:"tool_use_result"`
			Model                 string
			ModelUsage            map[string]claudeWindow
			Type, Subtype, Result string
			IsReplay              bool
			SessionID             string `json:"session_id"`
			ParentID              string `json:"parent_tool_use_id"`
			IsError               bool   `json:"is_error"`
			Message               json.RawMessage
			Errors                []string
			Event                 struct {
				Type    string
				Index   int
				Message struct{ ID string }
				Block   block `json:"content_block"`
				Delta   struct{ Type, Text string }
			}
		}
		if err := decoder.Decode(&frame); err == io.EOF {
			break
		} else if err != nil {
			if control != nil && control.session != "" && reply != "" {
				break
			}
			return "", "", err
		}
		if control != nil && control.mode != nil {
			if frame.SessionID != "" && usage.id != "" && usage.id != frame.SessionID {
				continue
			}
			if frame.ParentID == "" && !frame.IsReplay && frame.Type == "system" && frame.Subtype == "init" && usage.id == "" {
				usage.id = frame.SessionID
			}
			if frame.Type == "system" && frame.ParentID == "" && !frame.IsReplay && usage.id != "" && frame.SessionID == usage.id {
				control.mode.observe(frame.Subtype, frame.PermissionMode)
			}
		}
		if host != nil {
			switch frame.Type {
			case "control_request":
				var request claudeToolRequest
				if err := json.Unmarshal(frame.Request, &request); err != nil {
					return "", "", fmt.Errorf("无效的 Claude 控制请求：%w", err)
				}
				if request.Subtype == "can_use_tool" && request.Name == "AskUserQuestion" && request.ToolID != "" {
					questionTools[request.ToolID] = true
				}
				request.subagent = frame.ParentID != ""
				if err := control.dispatch(frame.RequestID, firstNonEmpty(usage.id, frame.SessionID), request); err != nil {
					return "", "", err
				}
				continue
			case "control_cancel_request":
				host.withdraw(frame.RequestID)
				continue
			}
		}
		if report != nil && frame.ParentID == "" && !frame.IsReplay {
			usage.observe(report, frame.SessionID, frame.Type, frame.Subtype, frame.Model, frame.Message, frame.ModelUsage)
		}
		if control != nil && frame.ParentID == "" {
			switch frame.Type {
			case "control_response":
				if !frame.IsReplay && control.mode != nil {
					control.mode.reply(frame.Response)
					if control.setup != nil {
						control.setup.receive(frame.Response, control.mode)
					}
				}
				if frame.IsReplay {
					continue
				}
				control.reply(frame.Response)
				continue
			case "control_request":
				control.release()
				continue
			case "assistant", "stream_event", "user":
				if usage.id != "" && usage.id == frame.SessionID {
					control.activity()
				}
			case "system":
				if frame.Subtype == "init" && usage.id != "" && usage.id == frame.SessionID {
					control.activity()
				}
			case "result":
				if control.mode != nil && usage.id == "" && frame.SessionID != "" {
					usage.id = frame.SessionID
				}
				if usage.id != "" && usage.id == frame.SessionID {
					control.result(frame.SessionID)
				} else {
					if host != nil {
						host.end()
					}
					control.release()
				}
			}
		}
		if report != nil && usage.id != "" && frame.ParentID == "" && frame.SessionID != "" && frame.SessionID != usage.id {
			continue
		}
		if frame.ParentID == "" && frame.SessionID != "" && (expectedID == "" || expectedID == frame.SessionID) {
			id = frame.SessionID
		}
		switch frame.Type {
		case "stream_event":
			ev := frame.Event
			if ev.Type == "message_start" && frame.ParentID == "" {
				streamID = ev.Message.ID
				latestBlock = -1
				clear(texts)
			}
			if ev.Type == "content_block_start" {
				if frame.ParentID == "" {
					latestBlock = ev.Index
				}
				b := ev.Block
				switch b.Type {
				case "tool_use", "server_tool_use":
					toolStart(b.ID, b.Name, b.Input)
				case "text":
					if frame.ParentID == "" {
						texts[ev.Index] = &strings.Builder{}
						texts[ev.Index].WriteString(b.Text)
						sink(Activity{TextID: textID(ev.Index), Text: b.Text, TextSnapshot: true})
					}
				}
			}
			if frame.ParentID == "" && ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
				if texts[ev.Index] == nil {
					texts[ev.Index] = &strings.Builder{}
				}
				texts[ev.Index].WriteString(ev.Delta.Text)
				sink(Activity{TextID: textID(ev.Index), Text: ev.Delta.Text})
			}
		case "assistant", "user":
			var message struct {
				ID      string
				Content json.RawMessage
			}
			var blocks []block
			if json.Unmarshal(frame.Message, &message) != nil || json.Unmarshal(message.Content, &blocks) != nil {
				continue
			}
			if frame.Type == "assistant" && frame.ParentID == "" && message.ID != "" && streamID != message.ID && (streamID != "" || len(texts) == 0) {
				streamID = message.ID
				latestBlock = -1
				clear(texts)
			}
			results := 0
			for _, b := range blocks {
				if b.Type == "tool_result" {
					results++
				}
			}
			for idx, b := range blocks {
				switch b.Type {
				case "text":
					if len(blocks) == 1 && latestBlock >= 0 && texts[latestBlock] != nil {
						idx = latestBlock
					}
					if frame.Type == "assistant" && frame.ParentID == "" && (texts[idx] == nil || texts[idx].String() != b.Text) {
						sink(Activity{TextID: textID(idx), Text: b.Text, TextSnapshot: true})
					}
				case "tool_use", "server_tool_use":
					toolStart(b.ID, b.Name, b.Input)
				case "tool_result":
					status := "succeeded"
					if b.Failed {
						status = "failed"
					}
					event := Activity{CallID: b.CallID, Status: status}
					// Claude can echo the full updated input and answers in a
					// tool result. The question card owns their public projection.
					if !questionTools[b.CallID] {
						event.Output = activityOutput(b.Content)
					}
					if !b.Failed && results == 1 {
						event.Edits = claudeFileEdits(editPaths[b.CallID], frame.ToolResult)
					}
					delete(editPaths, b.CallID)
					sink(event)
				}
			}
			if frame.Type == "assistant" && frame.ParentID == "" && streamID == "" {
				latestBlock = -1
				clear(texts)
				messageNumber++
			}
		case "result", "":
			if frame.ParentID != "" || expectedID != "" && frame.SessionID != expectedID {
				continue
			}
			reply = frame.Result
			if frame.IsError || strings.HasPrefix(frame.Subtype, "error") {
				failure = firstNonEmpty(reply, strings.Join(frame.Errors, "\n"), frame.Subtype, "Claude turn failed")
			}
		}
	}
	if failure != "" {
		return "", id, fmt.Errorf("claude: %s", failure)
	}
	if strings.TrimSpace(reply) == "" {
		return "", id, fmt.Errorf("claude returned an empty final response")
	}
	return reply, id, nil
}
