package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"bqagent/internal/acp"
	"bqagent/internal/agent"
	apptrace "bqagent/internal/trace"
	"bqagent/internal/turn"
)

// ACP chunks append text; bqagent events replace a message snapshot.
type acpTextSnapshot struct {
	text     string
	seq      uint64
	revision int
}

type acpTurnStream struct {
	mu        sync.Mutex
	emit      acp.Emit
	cancel    context.CancelFunc
	snapshots map[string]acpTextSnapshot
	tools     map[string]bool
	err       error
}

func (stream *acpTurnStream) failure() error {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.err
}

func (stream *acpTurnStream) contains(text string) bool {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if text == "" {
		return true
	}
	for _, snapshot := range stream.snapshots {
		if snapshot.text == text {
			return true
		}
	}
	return false
}

func (stream *acpTurnStream) send(update acp.Update) error {
	if stream.err != nil {
		return stream.err
	}
	stream.err = stream.emit(update)
	if stream.err != nil {
		stream.cancel()
	}
	return stream.err
}

func (stream *acpTurnStream) message(event turn.Event) {
	if event.Kind != "message.updated" && event.Kind != "message.completed" {
		return
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	previous, exists := stream.snapshots[event.ItemID]
	if exists && event.Seq <= previous.seq {
		return
	}
	revision := previous.revision
	delta := event.Text
	if strings.HasPrefix(event.Text, previous.text) {
		delta = event.Text[len(previous.text):]
	} else {
		revision++
		// ACP has no retraction: identify the replaced generation instead of joining it to its retry.
		_ = emitACPText(stream.send, "agent_message_chunk", fmt.Sprintf("%s/retry/%d", event.ItemID, revision), "\n[上一段回复已中断或修订，以下为更新后的回复。]\n")
	}
	stream.snapshots[event.ItemID] = acpTextSnapshot{text: event.Text, seq: event.Seq, revision: revision}
	id := fmt.Sprintf("%s/revision/%d", event.ItemID, revision)
	_ = emitACPText(stream.send, "agent_message_chunk", id, delta)
}

func (stream *acpTurnStream) EmitToolEvent(event agent.ToolEvent) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	id := event.ID
	if event.TurnID != "" {
		id = event.TurnID + "/" + id
	}
	update := acpToolUpdate(id, event.Name, event.Status, event.Arguments, event.Preview, event.ToolExecution)
	if !stream.tools[id] {
		update["sessionUpdate"] = "tool_call"
		stream.tools[id] = true
	}
	if event.Edits != nil {
		locations := make([]map[string]any, 0, len(event.Edits.Files))
		for _, file := range event.Edits.Files {
			locations = append(locations, map[string]any{"path": file.Path})
		}
		update["locations"] = locations
	}
	_ = stream.send(update)
}

func acpToolUpdate(id, name, status string, arguments map[string]any, output string, execution apptrace.ToolExecution) acp.Update {
	state := "completed"
	switch {
	case execution.CallStatus == "error" || status == "failed" || status == "cancelled" || status == "killed" || status == "timed_out":
		state = "failed"
	case (status == "running" || status == "pending") && execution.CallStatus == "":
		state = "in_progress"
	}
	kind := "other"
	switch name {
	case "bash", "bash_wait", "bash_kill":
		kind = "execute"
	case "read_file":
		kind = "read"
	case "write_file", "edit_file":
		kind = "edit"
	case "search", "grep", "glob", "list_files":
		kind = "search"
	}
	update := acp.Update{
		"sessionUpdate": "tool_call_update", "toolCallId": id,
		"title": name, "kind": kind, "status": state,
	}
	if arguments != nil {
		update["rawInput"] = arguments
		if path, ok := arguments["path"].(string); ok && path != "" {
			update["locations"] = []map[string]any{{"path": path}}
		}
	}
	if output != "" {
		update["content"] = []map[string]any{{"type": "content", "content": acp.ContentBlock{Type: "text", Text: output}}}
		update["rawOutput"] = output
	}
	if kind == "execute" && execution.CallStatus != "" {
		raw := map[string]any{"stdout": output}
		if execution.ExitCode != nil {
			raw["exitCode"] = *execution.ExitCode
		}
		if execution.ExecutionStatus != "" {
			raw["executionStatus"] = execution.ExecutionStatus
		}
		if execution.JobID != "" {
			raw["jobId"] = execution.JobID
		}
		update["rawOutput"] = raw
	}
	return update
}

func emitACPText(emit acp.Emit, kind, id, text string) error {
	const maxChunkBytes = 16 * 1024
	for len(text) > 0 {
		size := min(len(text), maxChunkBytes)
		for size < len(text) && !utf8.RuneStart(text[size]) {
			size--
		}
		if err := emit(acp.Update{
			"sessionUpdate": kind, "messageId": id,
			"content": acp.ContentBlock{Type: "text", Text: text[:size]},
		}); err != nil {
			return err
		}
		text = text[size:]
	}
	return nil
}
