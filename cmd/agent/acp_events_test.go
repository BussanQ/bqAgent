package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"bqagent/internal/acp"
	"bqagent/internal/agent"
	apptrace "bqagent/internal/trace"
	"bqagent/internal/turn"
)

func TestACPStreamConvertsSnapshotsToDeltas(t *testing.T) {
	var updates []acp.Update
	stream := &acpTurnStream{emit: func(update acp.Update) error {
		updates = append(updates, update)
		return nil
	}, cancel: func() {}, snapshots: map[string]acpTextSnapshot{}, tools: map[string]bool{}}
	stream.message(turn.Event{ItemID: "one", Seq: 1, Kind: "message.updated", Text: "你"})
	stream.message(turn.Event{ItemID: "one", Seq: 2, Kind: "message.updated", Text: "你好"})
	stream.message(turn.Event{ItemID: "one", Seq: 1, Kind: "message.updated", Text: "stale"})
	stream.message(turn.Event{ItemID: "one", Seq: 3, Kind: "message.completed", Text: "你好"})
	stream.message(turn.Event{ItemID: "two", Seq: 4, Kind: "message.completed", Text: "完成"})
	if text := acpCollectedText(updates, "agent_message_chunk"); text != "你好完成" {
		t.Fatalf("text=%q", text)
	}
	if updates[0]["messageId"] == updates[len(updates)-1]["messageId"] {
		t.Fatal("distinct messages merged")
	}
	if !stream.contains("你好") {
		t.Fatal("final response would be duplicated")
	}
}

func TestACPStreamMarksReplacedGeneration(t *testing.T) {
	var updates []acp.Update
	stream := &acpTurnStream{emit: func(update acp.Update) error { updates = append(updates, update); return nil }, cancel: func() {}, snapshots: map[string]acpTextSnapshot{}}
	stream.message(turn.Event{ItemID: "one", Seq: 1, Kind: "message.updated", Text: "partial"})
	stream.message(turn.Event{ItemID: "one", Seq: 2, Kind: "message.updated", Text: ""})
	stream.message(turn.Event{ItemID: "one", Seq: 3, Kind: "message.completed", Text: "correct"})
	text := acpCollectedText(updates, "agent_message_chunk")
	if !strings.Contains(text, "回复已中断或修订") || !strings.HasSuffix(text, "correct") {
		t.Fatalf("text=%q", text)
	}
	if updates[0]["messageId"] == updates[len(updates)-1]["messageId"] {
		t.Fatal("retry reused append-only message id")
	}
}

func TestACPStreamMapsToolCallAndResult(t *testing.T) {
	var updates []acp.Update
	stream := &acpTurnStream{emit: func(update acp.Update) error { updates = append(updates, update); return nil }, cancel: func() {}, tools: map[string]bool{}}
	stream.EmitToolEvent(agent.ToolEvent{ID: "call", Name: "bash", Kind: "tool_start", Status: "running", Arguments: map[string]any{"command": "pwd"}})
	exit := 2
	stream.EmitToolEvent(agent.ToolEvent{ID: "call", Name: "bash", Kind: "tool_result", Status: "failed", Preview: "failed command", ToolExecution: apptrace.ToolExecution{CallStatus: "error", ExitCode: &exit}})
	if updates[0]["sessionUpdate"] != "tool_call" || updates[0]["status"] != "in_progress" || updates[0]["kind"] != "execute" {
		t.Fatalf("start=%+v", updates[0])
	}
	if updates[1]["sessionUpdate"] != "tool_call_update" || updates[1]["status"] != "failed" || updates[1]["toolCallId"] != updates[0]["toolCallId"] {
		t.Fatalf("result=%+v", updates[1])
	}
	if updates[1]["rawOutput"].(map[string]any)["exitCode"] != 2 {
		t.Fatal("exit code lost")
	}
	stream.EmitToolEvent(agent.ToolEvent{ID: "async", Name: "bash", Kind: "tool_result", Status: "running", ToolExecution: apptrace.ToolExecution{CallStatus: "success", ExecutionStatus: "running", JobID: "job"}})
	if updates[2]["status"] != "completed" {
		t.Fatal("returned background call left permanently in progress")
	}
}

func TestACPStreamOutputFailureCancelsTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writeErr := errors.New("closed output")
	stream := &acpTurnStream{emit: func(acp.Update) error { return writeErr }, cancel: cancel, snapshots: map[string]acpTextSnapshot{}}
	stream.message(turn.Event{ItemID: "one", Seq: 1, Kind: "message.updated", Text: "hello"})
	if !errors.Is(stream.failure(), writeErr) || ctx.Err() == nil {
		t.Fatal("write failure did not cancel model")
	}
}

func TestACPTextChunksPreserveUTF8(t *testing.T) {
	text := strings.Repeat("你好😀", 5000)
	var joined strings.Builder
	err := emitACPText(func(update acp.Update) error {
		chunk := update["content"].(acp.ContentBlock).Text
		if !utf8.ValidString(chunk) || len(chunk) > 16*1024 {
			t.Fatalf("invalid chunk length=%d", len(chunk))
		}
		joined.WriteString(chunk)
		return nil
	}, "agent_message_chunk", "message", text)
	if err != nil || joined.String() != text {
		t.Fatalf("text changed: %v", err)
	}
}
