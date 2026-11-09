package extagent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"bqagent/internal/session"
)

func TestCodexContextUsageUsesLastRequestOnlyOnConfirmedTurn(t *testing.T) {
	client := &codexAppServerClient{responses: map[string]chan rpcEnvelope{}, done: make(chan struct{})}
	client.stdin = modelWire(func(msg rpcEnvelope) {
		for _, frame := range []struct{ turn, thread, value string }{
			{"other", "parent", "900"}, {"live", "foreign", "800"}, {"live", "parent", "120"},
		} {
			data := `{"threadId":"` + frame.thread + `","turnId":"` + frame.turn + `","tokenUsage":{"last":{"totalTokens":` + frame.value + `},"total":{"totalTokens":99000},"modelContextWindow":1000}}`
			client.notification(rpcEnvelope{Method: "thread/tokenUsage/updated", Params: json.RawMessage(data)})
		}
		client.notification(rpcEnvelope{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"parent","turn":{"id":"live","status":"completed","items":[{"id":"a","type":"agentMessage","text":"ok"}]}}`)})
		client.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(`{"turn":{"id":"live"}}`)}
	})
	var seen []ContextUsage
	ctx, finish := trackContextUsage(t.Context(), "", 0, SessionState{}, "", func(u ContextUsage) { seen = append(seen, u) })
	defer finish()
	if _, err := client.Prompt(ctx, "parent", "hi"); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0].Used != 120 || seen[0].Window != 1000 || seen[0].Estimated {
		t.Fatal(seen)
	}
}

func TestContextUsageRestoresOnlyMatchingBindingAndEpoch(t *testing.T) {
	store := NewStateStore(t.TempDir())
	chat, err := store.sessions.Create(session.CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	state := SessionState{Agent: AgentPi, Transport: TransportCLI, BQSessionID: chat.ID(), ExternalSessionID: "native", Model: "p/m"}
	b := NewBroker(store, nil, nil)
	defer b.Close()
	ctx, finish := trackContextUsage(t.Context(), chat.ContextDir(), chat.ContextNumber(), state, "p/m", func(ContextUsage) {})
	sink := contextUsageSink(ctx)
	sink("native", ContextUsage{Used: 17, Window: 100})
	finish()
	sink("native", ContextUsage{Used: 99, Window: 100})
	if got := b.readContextUsage(state); got == nil || got.Used != 17 || got.Captured <= 0 {
		t.Fatal(got)
	}
	for _, field := range []string{"model", "session", "transport", "agent"} {
		other := state
		switch field {
		case "model":
			other.Model = "p/next"
		case "session":
			other.ExternalSessionID = "other"
		case "transport":
			other.Transport = TransportACP
		case "agent":
			other.Agent = AgentClaude
		}
		if got := b.readContextUsage(other); got != nil {
			t.Fatal(field, got)
		}
	}
	if err := chat.StartContext("clear", "pi", func(*session.Session) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got := b.readContextUsage(state); got != nil {
		t.Fatal("old epoch", got)
	}
}

func TestACPContextUsageAcceptsZeroAndKeepsSessionsSeparate(t *testing.T) {
	var seen []Activity
	c := &stdioACPClient{activity: map[string]ActivitySink{"s": func(a Activity) { seen = append(seen, a) }}}
	for _, raw := range []string{
		`{"sessionId":"other","update":{"sessionUpdate":"usage_update","size":200,"used":5}}`,
		`{"sessionId":"s","update":{"sessionUpdate":"usage_update","size":200}}`,
		`{"sessionId":"s","update":{"sessionUpdate":"usage_update","size":200,"used":0}}`,
	} {
		c.handleSessionUpdate(json.RawMessage(raw))
	}
	if len(seen) != 1 || seen[0].NativeSession != "s" || seen[0].ContextUsage == nil || seen[0].ContextUsage.Used != 0 || !seen[0].ContextUsage.Estimated {
		t.Fatal(seen)
	}
}

func TestCodexIdleUsageAcceptsOnlyLastNativeTurnAndCannotResurrectADeletedChat(t *testing.T) {
	store := NewStateStore(t.TempDir())
	chat, err := store.sessions.Create(session.CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	b := NewBroker(store, nil, nil)
	defer b.Close()
	state := SessionState{Agent: AgentCodex, Transport: TransportAppServer, BQSessionID: chat.ID(), ExternalSessionID: "thread", Model: "gpt"}
	ctx, finish := trackContextUsage(t.Context(), chat.ContextDir(), chat.ContextNumber(), state, state.Model, func(ContextUsage) {})
	client := &codexAppServerClient{responses: map[string]chan rpcEnvelope{}, done: make(chan struct{})}
	client.stdin = modelWire(func(msg rpcEnvelope) {
		client.notification(rpcEnvelope{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread","turn":{"id":"turn","status":"completed","items":[{"id":"a","type":"agentMessage","text":"answer"}]}}`)})
		client.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(`{"turn":{"id":"turn"}}`)}
	})
	if _, err := client.Prompt(ctx, "thread", "hello"); err != nil {
		t.Fatal(err)
	}
	finish()
	update := func(turnID, value string) {
		client.notification(rpcEnvelope{Method: "thread/tokenUsage/updated", Params: json.RawMessage(`{"threadId":"thread","turnId":"` + turnID + `","tokenUsage":{"last":{"totalTokens":` + value + `},"modelContextWindow":1000}}`)})
	}
	update("turn", "73")
	update("foreign", "900")
	if u := b.readContextUsage(state); u == nil || u.Used != 73 {
		t.Fatal("late report", u)
	}
	next, finishNext := trackContextUsage(t.Context(), chat.ContextDir(), chat.ContextNumber(), state, state.Model, func(ContextUsage) {})
	contextUsageSink(next)("thread", ContextUsage{Used: 28, Window: 1000})
	finishNext()
	update("turn", "910")
	if u := b.readContextUsage(state); u == nil || u.Used != 28 {
		t.Fatal("older turn replaced a newer reading", u)
	}
	if err := store.sessions.Delete(chat.ID()); err != nil {
		t.Fatal(err)
	}
	update("turn", "920")
	if _, err := os.Stat(chat.Dir()); !os.IsNotExist(err) {
		t.Fatal("deleted conversation recreated", err)
	}
}

func TestACPContextUsageReplaysLoadReportOnlyToItsOwnPrompt(t *testing.T) {
	c := &stdioACPClient{responses: map[string]chan rpcEnvelope{}, collectors: map[string]*strings.Builder{}}
	c.stdin = modelWire(func(msg rpcEnvelope) { c.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(`{}`)} })
	c.handleSessionUpdate(json.RawMessage(`{"sessionId":"resumed","update":{"sessionUpdate":"usage_update","size":1000,"used":112}}`))
	var events []Activity
	ctx := context.WithValue(t.Context(), activityContextKey{}, ActivitySink(func(a Activity) { events = append(events, a) }))
	if _, err := c.Prompt(ctx, "other", "hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Prompt(ctx, "resumed", "hello"); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ContextUsage.Used != 112 || events[0].NativeSession != "resumed" {
		t.Fatal(events)
	}
}

func TestPiContextUsageUsesStatsAfterEstimateAndInvalidatesOnCompaction(t *testing.T) {
	var readings []ContextUsage
	reader := &piContextReader{id: "s", model: piModel{Provider: "p", ID: "m", ContextWindow: 1000}, report: func(id string, u ContextUsage) {
		if id != "s" {
			t.Fatal(id)
		}
		readings = append(readings, u)
	}}
	reader.observe(piEvent{Type: "message_end", Message: json.RawMessage(`{"role":"assistant","usage":{"input":30,"cacheWrite":15,"cacheRead":20,"output":80}}`)})
	reader.observe(piEvent{Type: "auto_compaction_start"})
	reader.compacted(json.RawMessage(`{"estimatedTokensAfter":40}`))
	w := newPiTestWire()
	rpc := w.rpc()
	rpc.usage = reader
	for _, raw := range []string{`{"sessionId":"foreign","contextUsage":{"tokens":999,"contextWindow":2000}}`, `{"sessionId":"s","tokens":{"total":99999},"contextUsage":{"tokens":0,"contextWindow":2000}}`} {
		done := make(chan struct{})
		go func() { rpc.readContextStats(context.Background()); close(done) }()
		w.reply(w.next(t, "get_session_stats"), raw)
		<-done
	}
	if len(readings) != 4 || readings[0].Used != 65 || !readings[0].Estimated || readings[1].Window != 0 || readings[2].Used != 40 || readings[3].Window != 2000 || readings[3].Used != 0 || readings[3].Estimated {
		t.Fatal(readings)
	}
}

func TestClaudeContextUsageCountsOnlyMainResponseAndResetsAtBoundary(t *testing.T) {
	data := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"root","model":"main[1m]"}`,
		`{"type":"assistant","session_id":"root","message":{"model":"main","usage":{"input_tokens":11,"cache_creation_input_tokens":12,"cache_read_input_tokens":13,"output_tokens":500}}}`,
		`{"type":"assistant","session_id":"root","parent_tool_use_id":"nested","message":{"model":"child","usage":{"input_tokens":900}}}`,
		`{"type":"assistant","session_id":"foreign","message":{"model":"child","usage":{"input_tokens":800}}}`,
		`{"type":"result","session_id":"root","result":"ok","modelUsage":{"child":{"contextWindow":999999},"main[1m]":{"contextWindow":880000}}}`,
		`{"type":"system","session_id":"root","subtype":"compact_boundary"}`,
	}, "\n")
	var seen []ContextUsage
	_, _, err := readClaudeContextStream(strings.NewReader(data), func(Activity) {}, "root", func(id string, u ContextUsage) {
		if id != "root" {
			t.Fatal(id)
		}
		seen = append(seen, u)
	}, nil)
	if err != nil || len(seen) != 3 || seen[0].Used != 36 || seen[0].Window != 1000000 || !seen[1].Estimated || seen[1].Used != 36 || seen[1].Window != 880000 || seen[2].Window != 0 {
		t.Fatal(seen, err)
	}
}

func TestPiContextStatsFailureCannotFailOrResendACompletedPrompt(t *testing.T) {
	w := newPiTestWire()
	ctx := context.WithValue(t.Context(), usageKey{}, usageSink(func(string, ContextUsage) { t.Error("unexpected measurement") }))
	done := make(chan error, 1)
	go func() {
		out, err := w.rpc().prompt(ctx, SessionState{Agent: AgentPi, ExternalSessionID: "native"}, "one", nil, nil)
		if err == nil && (out.State.ExternalSessionID != "native" || out.Reply == "") {
			t.Error(out)
		}
		done <- err
	}()
	state := `{"sessionId":"native","model":{"provider":"p","id":"m","contextWindow":1000}}`
	w.reply(w.next(t, "get_state"), state)
	w.reply(w.next(t, "prompt"), `{"disposition":"handled","agentInvoked":false}`)
	w.reply(w.next(t, "get_state"), state)
	stats := w.next(t, "get_session_stats")
	w.frames <- piFrame{event: piEvent{Type: "response", ID: piRequestID{text: stats["id"].(string)}, Command: "get_session_stats", Error: json.RawMessage(`"unsupported"`)}}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(w.sent) != 0 {
		t.Fatal("metadata failure redispatched work")
	}
}
