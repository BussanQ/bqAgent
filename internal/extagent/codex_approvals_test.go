package extagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

type codexApprovalWire struct {
	client *codexAppServerClient
	broker *Broker
	run    *appServerRun
	input  *json.Encoder
	output chan rpcEnvelope
	sink   permissionReplaySink
}

func newCodexApprovalWire(t *testing.T) *codexApprovalWire {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	run := &appServerRun{threadID: "thread", turnID: "live", ctx: ctx, cancel: cancel, identified: make(chan struct{}), turns: map[string]*appServerProgress{}, changed: make(chan struct{}, 1)}
	close(run.identified)
	b := NewBroker(NewStateStore(t.TempDir()), nil, nil)
	out := make(chan rpcEnvelope, 16)
	c := &codexAppServerClient{active: run, done: make(chan struct{}), stdin: modelWire(func(e rpcEnvelope) { out <- e })}
	key := acpClientKey{sessionID: "chat", agent: AgentCodex}
	b.acpClients[key] = c
	r, w := io.Pipe()
	f := &codexApprovalWire{client: c, broker: b, run: run, input: json.NewEncoder(w), output: out,
		sink: permissionReplaySink{make(chan ACPPermissionRequest, 16), make(chan ACPPermissionRequest, 16)}}
	c.setContextPermissionHandler(func(ctx context.Context, p acpPermissionParams) acpPermissionOutcome {
		return b.awaitPermission(ctx, "chat", AgentCodex, p, f.sink, 0)
	})
	go c.readLoop(r)
	t.Cleanup(func() {
		cancel()
		w.Close()
		<-c.done
		run.promptWorkers.Wait()
		delete(b.acpClients, key)
		b.Close()
	})
	return f
}

func (f *codexApprovalWire) send(t *testing.T, id, method string, params map[string]any) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.input.Encode(rpcEnvelope{ID: json.RawMessage(id), Method: method, Params: raw}); err != nil {
		t.Fatal(err)
	}
}

func (f *codexApprovalWire) nextReply(t *testing.T, id string) map[string]any {
	t.Helper()
	select {
	case got := <-f.output:
		var result map[string]any
		if string(got.ID) != id || got.Error != nil || json.Unmarshal(got.Result, &result) != nil {
			t.Fatal(got)
		}
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("missing native reply")
		return nil
	}
}

func mcpApprovalFixture(persist any) map[string]any {
	return map[string]any{"threadId": "thread", "turnId": "live", "serverName": "drive", "mode": "form", "message": "Move this file?",
		"requestedSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		"_meta": map[string]any{"codex_approval_kind": "mcp_tool_call", "persist": persist, "tool_name": "move_file",
			"tool_params":         map[string]string{"private": "undisplayed-argument"},
			"tool_params_display": []map[string]string{{"name": "to", "display_name": "Destination", "value": "<public>"}},
			"unrelated":           "private-metadata"}}
}

func TestCodexMCPApprovalOffersOnlyNativeScopesAndReturnsExactDecision(t *testing.T) {
	f := newCodexApprovalWire(t)
	for i, tc := range []struct {
		persist          any
		decision, action string
		wantMeta         map[string]any
		session          bool
	}{
		{"always", "accept", "accept", nil, false},
		{"session", "acceptForSession", "accept", map[string]any{"persist": "session"}, true},
		{[]string{"always", "session"}, "cancel", "cancel", map[string]any{"codex_approval_cancellation_reason": "user"}, true},
		{nil, "cancel", "cancel", map[string]any{"codex_approval_cancellation_reason": "user"}, false},
	} {
		id := strconv.Itoa(i + 1)
		f.send(t, id, "mcpServer/elicitation/request", mcpApprovalFixture(tc.persist))
		req := takeQuestionEvent(t, f.sink.start)
		if req.Dialog != nil || req.BQSessionID != "chat" || req.ExternalSessionID != "thread" || !f.broker.HasPendingPrompt("chat") {
			t.Fatal(req)
		}
		var tool struct{ Kind, Source, Title, Server, Tool, Detail string }
		if json.Unmarshal(req.ToolCall, &tool) != nil || tool.Kind != "mcp_tool_call" || tool.Title != "Move this file?" || tool.Server != "drive" ||
			tool.Tool != "move_file" || !strings.Contains(tool.Detail, "Destination") || !strings.Contains(tool.Detail, "<public>") || strings.Contains(string(req.ToolCall), "undisplayed-argument") || strings.Contains(string(req.ToolCall), "private-metadata") {
			t.Fatal(tool)
		}
		if tc.session != slices.ContainsFunc(req.Options, func(o ACPPermissionOption) bool { return o.OptionID == "acceptForSession" }) {
			t.Fatal(req.Options)
		}
		if !tc.session && f.broker.RespondPermission(req.RequestID, "acceptForSession") == nil {
			t.Fatal("unoffered scope accepted")
		}
		if f.broker.RespondPermission(req.RequestID, "always") == nil {
			t.Fatal("persistent approval accepted")
		}
		if err := f.broker.RespondPermission(req.RequestID, tc.decision); err != nil {
			t.Fatal(err)
		}
		result := f.nextReply(t, id)
		meta, ok := result["_meta"]
		content, present := result["content"]
		raw, _ := json.Marshal(meta)
		want, _ := json.Marshal(tc.wantMeta)
		if len(result) != 3 || result["action"] != tc.action || !ok || !present || content != nil || string(raw) != string(want) {
			t.Fatal(result)
		}
		end := takeQuestionEvent(t, f.sink.end)
		if end.Expired || end.Selected != tc.decision {
			t.Fatal(end)
		}
		if err := f.broker.RespondPermission(req.RequestID, "accept"); !errors.Is(err, ErrPermissionNotFound) {
			t.Fatal(err)
		}
	}
}

func TestCodexMCPNonApprovalElicitationsAndForeignTurnsDeclineWithoutCards(t *testing.T) {
	f := newCodexApprovalWire(t)
	for i, change := range []func(map[string]any){
		func(p map[string]any) { p["mode"] = "url"; p["url"] = "https://example.invalid" },
		func(p map[string]any) {
			p["requestedSchema"] = map[string]any{"type": "object", "properties": map[string]any{"input": map[string]string{"type": "string"}}}
		},
		func(p map[string]any) {
			p["requestedSchema"] = map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{"missing"}}
		},
		func(p map[string]any) { p["requestedSchema"] = map[string]any{"type": "object", "properties": []any{}} },
		func(p map[string]any) { p["_meta"] = map[string]string{"codex_approval_kind": "other"} },
		func(p map[string]any) { delete(p, "turnId") },
		func(p map[string]any) { p["threadId"] = "other" },
		func(p map[string]any) { p["turnId"] = "foreign" },
	} {
		p := mcpApprovalFixture("session")
		change(p)
		id := strconv.Itoa(i + 1)
		f.send(t, id, "mcpServer/elicitation/request", p)
		result := f.nextReply(t, id)
		if len(result) != 3 || result["action"] != "decline" || result["content"] != nil || result["_meta"] != nil {
			t.Fatal(result)
		}
		select {
		case req := <-f.sink.start:
			t.Fatal("unexpected form", req)
		default:
		}
	}
	p := mcpApprovalFixture(nil)
	delete(p, "mode")
	delete(p, "requestedSchema")
	f.send(t, `"older"`, "mcpServer/elicitation/request", p)
	req := takeQuestionEvent(t, f.sink.start)
	if err := f.broker.RespondPermission(req.RequestID, "accept"); err != nil {
		t.Fatal(err)
	}
	if f.nextReply(t, `"older"`)["action"] != "accept" {
		t.Fatal("legacy tool approval was lost")
	}
	takeQuestionEvent(t, f.sink.end)
}

func TestCodexWithdrawalExpiresMCPFileCommandAndPermissionApprovals(t *testing.T) {
	f := newCodexApprovalWire(t)
	for _, method := range []string{"mcpServer/elicitation/request", "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval"} {
		params := map[string]any{"threadId": "thread", "turnId": "live", "itemId": "same-item", "permissions": map[string]any{"network": map[string]bool{"enabled": true}}}
		if method == "mcpServer/elicitation/request" {
			params = mcpApprovalFixture("session")
		}
		f.send(t, "12", method, params)
		old := takeQuestionEvent(t, f.sink.start)
		f.client.writeMu.Lock()
		unlock := sync.OnceFunc(f.client.writeMu.Unlock)
		t.Cleanup(unlock)
		if err := f.broker.RespondPermission(old.RequestID, "accept"); err != nil {
			t.Fatal(err)
		}
		if !f.broker.HasPendingPrompt("chat") {
			t.Fatal("queue released before native write")
		}
		select {
		case e := <-f.sink.end:
			t.Fatal("published selection before write", e)
		default:
		}
		f.send(t, "", "serverRequest/resolved", map[string]any{"threadId": "thread", "requestId": 12})
		// A later request using the same native ID must own the wire exclusively.
		f.send(t, "12", "mcpServer/elicitation/request", mcpApprovalFixture(nil))
		replacement := takeQuestionEvent(t, f.sink.start)
		if old.RequestID == replacement.RequestID {
			t.Fatal("reused local request ID")
		}
		if err := f.broker.RespondPermission(old.RequestID, "accept"); !errors.Is(err, ErrPermissionNotFound) {
			t.Fatal(err)
		}
		unlock()
		end := takeQuestionEvent(t, f.sink.end)
		if !end.Expired || end.Selected != "" || end.RequestID != old.RequestID {
			t.Fatal(end)
		}
		if err := f.broker.RespondPermission(replacement.RequestID, "cancel"); err != nil {
			t.Fatal(err)
		}
		result := f.nextReply(t, "12")
		if result["action"] != "cancel" {
			t.Fatal("stale approval reached the wire", result)
		}
		takeQuestionEvent(t, f.sink.end)
		f.run.promptWorkers.Wait()
		select {
		case e := <-f.output:
			t.Fatal("unexpected second reply", e)
		default:
		}
	}
}

func TestCodexMCPDecisionExpiresIfTurnFinishesBeforeWrite(t *testing.T) {
	f := newCodexApprovalWire(t)
	f.send(t, `"last"`, "mcpServer/elicitation/request", mcpApprovalFixture("session"))
	req := takeQuestionEvent(t, f.sink.start)
	f.client.writeMu.Lock()
	unlock := sync.OnceFunc(f.client.writeMu.Unlock)
	defer unlock()
	if err := f.broker.RespondPermission(req.RequestID, "acceptForSession"); err != nil {
		t.Fatal(err)
	}
	f.client.notification(rpcEnvelope{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread","turn":{"id":"live","status":"completed"}}`)})
	unlock()
	result := f.nextReply(t, `"last"`)
	if result["action"] != "decline" || result["_meta"] != nil || result["content"] != nil {
		t.Fatal(result)
	}
	if end := takeQuestionEvent(t, f.sink.end); !end.Expired || end.Selected != "" {
		t.Fatal(end)
	}
	f.run.promptWorkers.Wait()
	if f.broker.HasPendingPrompt("chat") {
		t.Fatal("expired approval gated input")
	}
}

func TestCodexInvalidatedSessionCannotWriteAnAlreadySubmittedMCPGrant(t *testing.T) {
	f := newCodexApprovalWire(t)
	f.send(t, `"last"`, "mcpServer/elicitation/request", mcpApprovalFixture("session"))
	request := takeQuestionEvent(t, f.sink.start)
	f.client.writeMu.Lock()
	unlock := sync.OnceFunc(f.client.writeMu.Unlock)
	defer unlock()
	if err := f.broker.RespondPermission(request.RequestID, "acceptForSession"); err != nil {
		t.Fatal(err)
	}
	f.broker.invalidateSession("chat")
	unlock()
	if r := f.nextReply(t, `"last"`); r["action"] != "decline" || r["_meta"] != nil {
		t.Fatal(r)
	}
	if end := takeQuestionEvent(t, f.sink.end); !end.Expired || end.Selected != "" {
		t.Fatal(end)
	}
}

func TestCodexMCPParameterPreviewHonorsDisplayProjectionAndByteLimit(t *testing.T) {
	m := mcpApprovalMeta{Params: json.RawMessage(`{"file":"only-raw"}`)}
	if m.detail() != string(m.Params) {
		t.Fatal("missing legacy parameters")
	}
	for _, display := range []string{`[]`, `{"unknown":"variant"}`, `[{"name":"unused"}]`} {
		m.Display = json.RawMessage(display)
		if got := m.detail(); got != "" {
			t.Fatal("display projection exposed fallback arguments", got)
		}
	}
	data, _ := json.Marshal([]map[string]any{{"name": "file", "display_name": "目标", "value": strings.Repeat("文", maxApprovalPreview)}})
	m.Display = data
	got := m.detail()
	if !utf8.ValidString(got) || !strings.HasPrefix(got, "目标: 文") || !strings.HasSuffix(got, "…（已截断）") || len(got) > maxApprovalPreview+32 || strings.Contains(got, "only-raw") {
		t.Fatal("invalid bounded preview")
	}
}

type brokenApprovalPipe struct{}

func (brokenApprovalPipe) Close() error              { return nil }
func (brokenApprovalPipe) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestCodexMCPWriteFailureExpiresGrantAndReleasesPendingInput(t *testing.T) {
	f := newCodexApprovalWire(t)
	f.send(t, "25", "mcpServer/elicitation/request", mcpApprovalFixture("session"))
	req := takeQuestionEvent(t, f.sink.start)
	f.client.writeMu.Lock()
	f.client.stdin = brokenApprovalPipe{}
	f.client.writeMu.Unlock()
	if err := f.broker.RespondPermission(req.RequestID, "acceptForSession"); err != nil {
		t.Fatal(err)
	}
	end := takeQuestionEvent(t, f.sink.end)
	if !end.Expired || end.Selected != "" || end.RequestID != req.RequestID {
		t.Fatal(end)
	}
	f.run.promptWorkers.Wait()
	if f.broker.HasPendingPrompt("chat") || !errors.Is(f.client.failure(), io.ErrClosedPipe) {
		t.Fatal("broken transport kept an approval alive")
	}
}
