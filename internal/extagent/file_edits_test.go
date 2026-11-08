package extagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"bqagent/internal/transcript"
)

func TestCodexFileEditSnapshotReplacesEarlierPatchOnMatchingTurn(t *testing.T) {
	c := &codexAppServerClient{responses: map[string]chan rpcEnvelope{}, done: make(chan struct{})}
	c.stdin = modelWire(func(msg rpcEnvelope) {
		send := func(method, data string) { c.notification(rpcEnvelope{Method: method, Params: json.RawMessage(data)}) }
		send("item/fileChange/patchUpdated", `{"threadId":"s","turnId":"foreign","itemId":"wrong","changes":[{"path":"wrong","kind":{"type":"add"},"diff":"private"}]}`)
		send("item/fileChange/patchUpdated", `{"threadId":"s","turnId":"t","itemId":"edit","changes":[{"path":"x","kind":{"type":"add"},"diff":"draft"}]}`)
		send("item/fileChange/patchUpdated", `{"threadId":"s","turnId":"t","itemId":"edit","changes":[{"path":"x","kind":{"type":"update","movePath":"y"},"diff":"@@ -8,2 +10,2 @@\n-old\n+  new\n tail\n@@ -33 +35 @@\n-last\n+current"}]}`)
		send("item/completed", `{"threadId":"s","turnId":"t","item":{"id":"edit","type":"fileChange","status":"completed"}}`)
		send("turn/completed", `{"threadId":"s","turn":{"id":"t","status":"completed","items":[{"id":"answer","type":"agentMessage","text":"done"}]}}`)
		c.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(`{"turn":{"id":"t"}}`)}
	})
	var edits []*transcript.FileEdits
	var status string
	ctx := context.WithValue(t.Context(), activityContextKey{}, ActivitySink(func(a Activity) {
		if a.CallID == "wrong" {
			t.Error("foreign edit")
		}
		if a.Edits != nil {
			edits = append(edits, a.Edits)
		}
		if a.CallID == "edit" {
			status = a.Status
		}
	}))
	reply, err := c.Prompt(ctx, "s", "hi")
	if err != nil || reply != "done" || status != "succeeded" || len(edits) != 2 {
		t.Fatal(reply, err, status, edits)
	}
	f := edits[1].Files[0]
	if f.Path != "y" || f.OldPath != "x" || f.Kind != "renamed" || f.Added != 2 || f.Removed != 2 || f.Lines[0].Old != 8 || f.Lines[1].New != 10 || f.Lines[1].Text != "  new" || f.Lines[3].Kind != "gap" || f.Lines[4].Old != 33 {
		t.Fatal(f)
	}
}

func TestClaudeEditUsesResolvedHunksOnlyForOneResult(t *testing.T) {
	const call = `{"type":"assistant","session_id":"s","message":{"content":[{"type":"tool_use","id":"a","name":"Edit","input":{"file_path":"app.go","old_string":"old","new_string":"new"}}]}}`
	result := func(blocks string) string {
		return `{"type":"user","session_id":"s","message":{"content":` + blocks + `},"tool_use_result":{"filePath":"app.go","structuredPatch":[{"oldStart":22,"newStart":25,"lines":["-old","+new"]}]}}`
	}
	for _, tc := range []struct {
		blocks   string
		resolved bool
	}{
		{`[{"type":"tool_result","tool_use_id":"a","content":"ok"}]`, true},
		{`[{"type":"tool_result","tool_use_id":"a","content":"ok"},{"type":"tool_result","tool_use_id":"b","content":"other"}]`, false},
		{`[{"type":"tool_result","tool_use_id":"a","is_error":true,"content":"failed"}]`, false},
	} {
		var start, finish *transcript.FileEdits
		_, _, err := readClaudeStream(strings.NewReader(strings.Join([]string{call, result(tc.blocks), `{"type":"result","session_id":"s","result":"done"}`}, "\n")), func(a Activity) {
			if a.CallID == "a" {
				if a.Status == "running" {
					start = a.Edits
				} else {
					finish = a.Edits
				}
			}
		})
		if err != nil || start == nil || !start.Files[0].Snippet || start.Files[0].Lines[0].Old != 0 || (finish != nil) != tc.resolved {
			t.Fatal(err, start, finish)
		}
		if finish != nil && (finish.Files[0].Lines[0].Old != 22 || finish.Files[0].Lines[1].New != 25) {
			t.Fatal(finish)
		}
	}
}

func TestPiEditUsesNumberedDiffFromMatchingCallOnly(t *testing.T) {
	var output piOutput
	var last Activity
	send := func(e piEvent) {
		_, err := output.observe(e, func(a Activity) { last = a })
		if err != nil {
			t.Fatal(err)
		}
	}
	send(piEvent{Type: "tool_execution_start", ToolName: "edit", ToolCallID: "x", Args: json.RawMessage(`{"path":"a.go","oldText":"x","newText":"y"}`)})
	const result = `{"details":{"diff":"- 12 x\n+ 12 y\n     ...\n  31 tail"}}`
	send(piEvent{Type: "tool_execution_end", ToolName: "bash", ToolCallID: "other", Result: json.RawMessage(result)})
	if last.Edits != nil {
		t.Fatal(last)
	}
	send(piEvent{Type: "tool_execution_end", ToolName: "edit", ToolCallID: "x", Result: json.RawMessage(result)})
	if last.Edits == nil || last.Edits.Files[0].Path != "a.go" || last.Edits.Files[0].Lines[1].New != 12 || last.Edits.Files[0].Lines[2].Kind != "gap" {
		t.Fatal(last)
	}
}

func TestACPFileEditPairsBoundedContentAndKeepsRealPositions(t *testing.T) {
	var got *transcript.FileEdits
	c := &stdioACPClient{activity: map[string]ActivitySink{"s": func(a Activity) { got = a.Edits }}}
	raw, _ := json.Marshal(map[string]any{"sessionId": "s", "update": map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "e", "kind": "edit", "status": "completed", "content": []any{
		map[string]any{"type": "diff", "path": "a", "oldText": strings.Repeat("same\n", 200) + "before\nsame\n", "newText": strings.Repeat("same\n", 200) + "after\nsame\n"},
		map[string]any{"type": "diff", "path": "b", "oldText": nil, "newText": strings.Repeat("界", maxEditBytes)},
	}}})
	c.handleSessionUpdate(raw)
	if got == nil || len(got.Files) != 2 {
		t.Fatal(got)
	}
	a, b := got.Files[0], got.Files[1]
	if a.Added != 1 || a.Removed != 1 || len(a.Lines) != 6 || a.Lines[3].Old != 201 || a.Lines[4].New != 201 || !b.Partial || b.Kind != "added" || !utf8.ValidString(b.Lines[0].Text) {
		t.Fatal(a, b.Partial)
	}
}

func TestDedicatedPatchEnvelopeKeepsMovesAndIgnoresQuotedPatchesInOtherTools(t *testing.T) {
	const patch = "*** Begin Patch\n*** Update File: old.go\n*** Move to: new.go\n@@ context\n-old\n+new\n*** Delete File: gone\n*** Add File: added\n++literal\n*** End Patch"
	input := map[string]any{"input": patch}
	if e := editInput("bash", input); e != nil {
		t.Fatal("read-only command treated as an edit", e)
	}
	w := editInput("Write", map[string]any{"file_path": "fixture", "content": patch})
	if len(w.Files) != 1 || w.Files[0].Path != "fixture" || w.Files[0].Kind != "written" || w.Files[0].Added != 0 {
		t.Fatal(w)
	}
	e := editInput("apply_patch", input)
	if e == nil || len(e.Files) != 3 || e.Files[0].OldPath != "old.go" || e.Files[0].Path != "new.go" || e.Files[0].Added != 1 || e.Files[0].Removed != 1 || !e.Files[1].Partial || e.Files[2].Lines[0].Text != "+literal" || e.Files[2].Lines[0].New != 1 {
		t.Fatal(e)
	}
	raw := codexFileEdits(json.RawMessage(`[{"path":"literal","kind":{"type":"add"},"diff":"first\nMoved to: still-content"}]`))
	if raw.Files[0].Path != "literal" || raw.Files[0].Added != 2 {
		t.Fatal(raw)
	}
	command := "apply_patch <<'PATCH'\n" + patch + "\nPATCH"
	if shell := editInput("commandExecution", map[string]any{"command": command}); shell == nil || len(shell.Files) != 3 {
		t.Fatal(shell)
	}
	for _, quoted := range []string{"cat <<'PATCH'\n" + patch + "\nPATCH", command + "\ntrue", "echo " + command, "false && " + command} {
		if got := editInput("Bash", map[string]any{"command": quoted}); got != nil {
			t.Fatal(quoted, got)
		}
	}
}
