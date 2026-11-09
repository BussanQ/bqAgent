package extagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const questionParams = `{"threadId":"thread","turnId":"live","itemId":"tool","isBlocking":true,"questions":[
 {"id":"scope","header":"Scope","question":"Which folder?","options":[{"label":"src","description":"Source files"},{"label":"test","description":"Tests"}]},
 {"id":"note","header":"Note","question":"Which setting?","isOther":true,"options":[{"label":"standard","description":"Use defaults"}]},
 {"id":"token","header":"Token","question":"Enter a value","isSecret":true}
]}`

func takeQuestionEvent(t *testing.T, events <-chan ACPPermissionRequest) ACPPermissionRequest {
	t.Helper()
	select {
	case e := <-events:
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("missing question event")
		return ACPPermissionRequest{}
	}
}

func TestCodexQuestionsRoundTripBeforeTurnStartReplyAndNeverReplayAnswers(t *testing.T) {
	b := NewBroker(NewStateStore(t.TempDir()), nil, nil)
	sink := permissionReplaySink{make(chan ACPPermissionRequest, 8), make(chan ACPPermissionRequest, 8)}
	r, w := io.Pipe()
	c := &codexAppServerClient{responses: map[string]chan rpcEnvelope{}, done: make(chan struct{})}
	b.acpClients[acpClientKey{sessionID: "chat", agent: AgentCodex}] = c
	defer func() {
		w.Close()
		<-c.done
		delete(b.acpClients, acpClientKey{sessionID: "chat", agent: AgentCodex})
		b.Close()
	}()
	c.setContextPermissionHandler(func(ctx context.Context, p acpPermissionParams) acpPermissionOutcome {
		return b.awaitPermission(ctx, "chat", AgentCodex, p, sink, 0)
	})
	replies := make(chan rpcEnvelope, 2)
	posted := make(chan struct{})
	allowAck := make(chan struct{})
	ack := sync.OnceFunc(func() { close(allowAck) })
	defer ack()
	enc := json.NewEncoder(w)
	c.stdin = modelWire(func(msg rpcEnvelope) {
		if msg.Method == "turn/start" {
			_ = enc.Encode(rpcEnvelope{ID: json.RawMessage(`"native-request"`), Method: "item/tool/requestUserInput", Params: json.RawMessage(questionParams)})
			close(posted)
			<-allowAck
			_ = enc.Encode(rpcEnvelope{ID: msg.ID, Result: json.RawMessage(`{"turn":{"id":"live"}}`)})
		} else {
			replies <- msg
		}
	})
	go c.readLoop(r)
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	result := make(chan error, 1)
	released := make(chan struct{}, 1)
	go func() {
		reply, err := c.PromptWithOptions(ctx, "thread", "hello", PromptOptions{QuestionsSettled: func() { released <- struct{}{} }})
		if err == nil && reply != "done" {
			err = errors.New("wrong final answer: " + reply)
		}
		result <- err
	}()
	select {
	case <-posted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-sink.start:
		t.Fatal("unconfirmed turn published a question")
	default:
	}
	ack()
	scope, note, secret := takeQuestionEvent(t, sink.start), takeQuestionEvent(t, sink.start), takeQuestionEvent(t, sink.start)
	if scope.Dialog.Title != "Which folder?" || scope.Dialog.FreeText || scope.Options[0].Description != "Source files" ||
		!note.Dialog.FreeText || !secret.Dialog.Secret || secret.ExternalSessionID != "thread" || !b.HasPendingPrompt("chat") || b.HasPendingPrompt("other") {
		t.Fatal(scope, note, secret)
	}
	token := "secret-value"
	free := " custom\nsetting "
	if err := b.RespondPrompt(secret.RequestID, "submit", &token); err != nil {
		t.Fatal(err)
	}
	private := takeQuestionEvent(t, sink.end)
	encoded, _ := json.Marshal(private)
	if private.Selected != "answered" || strings.Contains(string(encoded), token) {
		t.Fatal("secret answer leaked into replay")
	}
	if err := b.RespondPrompt(note.RequestID, "choice-0", &free); err == nil {
		t.Fatal("choice received an unoffered text value")
	}
	blank := "  "
	if err := b.RespondPrompt(note.RequestID, "submit", &blank); err == nil {
		t.Fatal("blank answer accepted")
	}
	if err := b.RespondPrompt(note.RequestID, "submit", &free); err != nil {
		t.Fatal(err)
	}
	partial := takeQuestionEvent(t, sink.end)
	encoded, _ = json.Marshal(partial)
	if partial.Selected != "submit" || strings.Contains(string(encoded), "custom") {
		t.Fatal("free text was journalled")
	}
	select {
	case <-replies:
		t.Fatal("incomplete batch replied")
	default:
	}
	if err := b.RespondPermission(scope.RequestID, "choice-1"); err != nil {
		t.Fatal(err)
	}
	takeQuestionEvent(t, sink.end)
	if err := b.RespondPermission(scope.RequestID, "choice-1"); !errors.Is(err, ErrPermissionNotFound) {
		t.Fatal(err)
	}
	var frame rpcEnvelope
	select {
	case frame = <-replies:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var response struct{ Answers map[string]codexAnswer }
	want := map[string]codexAnswer{"scope": {[]string{"test"}}, "note": {[]string{free}}, "token": {[]string{token}}}
	if string(frame.ID) != `"native-request"` || frame.Error != nil || json.Unmarshal(frame.Result, &response) != nil || !reflect.DeepEqual(response.Answers, want) {
		t.Fatal("incorrect native reply")
	}
	select {
	case <-released:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if b.HasPendingPrompt("chat") {
		t.Fatal("answered batch blocked queue")
	}
	_ = enc.Encode(rpcEnvelope{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread","turn":{"id":"live","status":"completed","items":[{"id":"m","type":"agentMessage","text":"done"}]}}`)})
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-replies:
		t.Fatal("duplicate reply")
	default:
	}
}

func TestCodexQuestionResolutionCancelsOnlyItsNativeIDAndGroupCancelDiscardsPartialAnswers(t *testing.T) {
	b := NewBroker(NewStateStore(t.TempDir()), nil, nil)
	defer b.Close()
	sink := permissionReplaySink{make(chan ACPPermissionRequest, 12), make(chan ACPPermissionRequest, 12)}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	run := &appServerRun{threadID: "thread", turnID: "live", ctx: ctx, identified: make(chan struct{})}
	close(run.identified)
	replies := make(chan rpcEnvelope, 4)
	c := &codexAppServerClient{active: run, stdin: modelWire(func(e rpcEnvelope) { replies <- e }), done: make(chan struct{})}
	c.setContextPermissionHandler(func(ctx context.Context, p acpPermissionParams) acpPermissionOutcome {
		return b.awaitPermission(ctx, "chat", AgentCodex, p, sink, 0)
	})
	start := func(id, params string) {
		c.startQuestions(rpcEnvelope{ID: json.RawMessage(id), Method: "item/tool/requestUserInput", Params: json.RawMessage(params)})
	}
	resolve := func(thread, id string) {
		c.notification(rpcEnvelope{Method: "serverRequest/resolved", Params: json.RawMessage(`{"threadId":"` + thread + `","requestId":` + id + `}`)})
	}
	start("7", strings.Replace(questionParams, `"isBlocking":true`, `"isBlocking":false`, 1))
	a := takeQuestionEvent(t, sink.start)
	takeQuestionEvent(t, sink.start)
	takeQuestionEvent(t, sink.start)
	if !a.Dialog.NonBlocking || b.HasPendingPrompt("chat") || c.blockingPrompt() {
		t.Fatal("nonblocking request gated steering")
	}
	start(`"7"`, questionParams)
	first, second, third := takeQuestionEvent(t, sink.start), takeQuestionEvent(t, sink.start), takeQuestionEvent(t, sink.start)
	resolve("other", "7")
	if err := b.RespondPermission(a.RequestID, "choice-0"); err != nil {
		t.Fatal(err)
	}
	takeQuestionEvent(t, sink.end)
	resolve("thread", "7")
	for range 2 {
		if !takeQuestionEvent(t, sink.end).Expired {
			t.Fatal("native resolution did not expire the remaining cards")
		}
	}
	if err := b.RespondPermission(first.RequestID, "choice-1"); err != nil {
		t.Fatal(err)
	}
	takeQuestionEvent(t, sink.end)
	if err := b.RespondPermission(second.RequestID, "cancel"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if !takeQuestionEvent(t, sink.end).Expired {
			t.Fatal("group cancel left a sibling open")
		}
	}
	run.promptWorkers.Wait()
	if err := b.RespondPermission(third.RequestID, "submit"); !errors.Is(err, ErrPermissionNotFound) {
		t.Fatal(err)
	}
	got := <-replies
	if string(got.ID) != `"7"` || string(got.Result) != `{"answers":{}}` {
		t.Fatal(got)
	}
	select {
	case <-replies:
		t.Fatal("answered a request withdrawn by Codex")
	default:
	}
}

func TestCodexQuestionsRejectForeignTurnsAndExpireWhenRunEnds(t *testing.T) {
	b := NewBroker(NewStateStore(t.TempDir()), nil, nil)
	defer b.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	run := &appServerRun{ctx: ctx, threadID: "thread", identified: make(chan struct{})}
	replies := make(chan rpcEnvelope, 4)
	c := &codexAppServerClient{active: run, stdin: modelWire(func(e rpcEnvelope) { replies <- e }), done: make(chan struct{})}
	sink := permissionReplaySink{make(chan ACPPermissionRequest, 8), make(chan ACPPermissionRequest, 8)}
	c.setContextPermissionHandler(func(ctx context.Context, p acpPermissionParams) acpPermissionOutcome {
		return b.awaitPermission(ctx, "chat", AgentCodex, p, sink, 0)
	})
	start := func(id, params string) {
		c.startQuestions(rpcEnvelope{ID: json.RawMessage(id), Params: json.RawMessage(params)})
	}
	takeReply := func() rpcEnvelope {
		t.Helper()
		select {
		case msg := <-replies:
			return msg
		case <-time.After(2 * time.Second):
			t.Fatal("no native reply")
			return rpcEnvelope{}
		}
	}
	start("1", strings.Replace(questionParams, `"live"`, `"wrong"`, 1))
	c.mu.Lock()
	run.turnID = "live"
	close(run.identified)
	c.mu.Unlock()
	run.promptWorkers.Wait()
	if got := takeReply(); string(got.Result) != `{"answers":{}}` {
		t.Fatal(got)
	}
	start("2", strings.Replace(questionParams, `"id":"token"`, `"id":"scope"`, 1))
	if got := takeReply(); got.Error == nil || got.Error.Code != -32602 {
		t.Fatal(got)
	}
	select {
	case <-sink.start:
		t.Fatal("foreign or malformed question became actionable")
	default:
	}
	start("3", questionParams)
	start("3", questionParams)
	var ids []string
	for range 3 {
		ids = append(ids, takeQuestionEvent(t, sink.start).RequestID)
	}
	cancel()
	run.promptWorkers.Wait()
	for range 3 {
		if !takeQuestionEvent(t, sink.end).Expired {
			t.Fatal("stopped request still live")
		}
	}
	if got := takeReply(); string(got.ID) != "3" || string(got.Result) != `{"answers":{}}` {
		t.Fatal(got)
	}
	for _, id := range ids {
		if err := b.RespondPermission(id, "choice-0"); !errors.Is(err, ErrPermissionNotFound) {
			t.Fatal(err)
		}
	}
	select {
	case <-sink.start:
		t.Fatal("duplicated request")
	case <-replies:
		t.Fatal("duplicated reply")
	default:
	}
}

func TestCodexQuestionWithdrawalWinsOverUnwrittenAnswerWhenIDIsReused(t *testing.T) {
	b := NewBroker(NewStateStore(t.TempDir()), nil, nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	run := &appServerRun{ctx: ctx, threadID: "thread", turnID: "live", identified: make(chan struct{})}
	close(run.identified)
	sink := permissionReplaySink{make(chan ACPPermissionRequest, 4), make(chan ACPPermissionRequest, 4)}
	replies := make(chan rpcEnvelope, 3)
	c := &codexAppServerClient{active: run, done: make(chan struct{}), stdin: modelWire(func(e rpcEnvelope) { replies <- e })}
	key := acpClientKey{sessionID: "chat", agent: AgentCodex}
	b.acpClients[key] = c
	defer func() { delete(b.acpClients, key); b.Close() }()
	c.setContextPermissionHandler(func(ctx context.Context, p acpPermissionParams) acpPermissionOutcome {
		return b.awaitPermission(ctx, "chat", AgentCodex, p, sink, 0)
	})
	request := rpcEnvelope{ID: json.RawMessage(`42`), Params: json.RawMessage(`{"threadId":"thread","turnId":"live","itemId":"tool","questions":[{"id":"q","question":"Pick a value"}]}`)}
	c.writeMu.Lock()
	unlock := sync.OnceFunc(c.writeMu.Unlock)
	defer unlock()
	c.startQuestions(request)
	first := takeQuestionEvent(t, sink.start)
	old := "withdrawn"
	if err := b.RespondPrompt(first.RequestID, "submit", &old); err != nil {
		t.Fatal(err)
	}
	takeQuestionEvent(t, sink.end)
	if !b.HasPendingPrompt("chat") {
		t.Fatal("blocking group released before its RPC response could be sent")
	}
	c.resolvePrompt(json.RawMessage(`{"threadId":"thread","requestId":42}`))
	c.startQuestions(request)
	second := takeQuestionEvent(t, sink.start)
	if first.RequestID == second.RequestID {
		t.Fatal("reused local form ID")
	}
	value := "current"
	if err := b.RespondPrompt(second.RequestID, "submit", &value); err != nil {
		t.Fatal(err)
	}
	takeQuestionEvent(t, sink.end)
	unlock()
	run.promptWorkers.Wait()
	if len(replies) != 1 {
		t.Fatalf("native replies = %d, want 1", len(replies))
	}
	if result := string((<-replies).Result); result != `{"answers":{"q":{"answers":["current"]}}}` {
		t.Fatal(result)
	}
}
