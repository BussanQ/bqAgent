package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"bqagent/internal/agent"
	"bqagent/internal/extagent"
	appruntime "bqagent/internal/runtime"
	"bqagent/internal/session"
	"bqagent/internal/tools"
	"bqagent/internal/turn"
)

func sendCard(f *queueFixture, id, op string, version uint64) commandQueueRequest {
	return commandQueueRequest{Action: "send_now", Session: f.saved.ID(), ID: id, Operation: op, BodyVersion: &version}
}

func TestQueueSendNowOverridesFIFOAndEditHoldsWithoutDuplicatingTheChild(t *testing.T) {
	f := newQueueFixture(t)
	first := queuedText(f, "first", "first")
	second := queuedText(f, "second", "original second")
	for _, req := range []commandQueueRequest{first, second} {
		postQueueChange(t, f, req, 200)
	}
	if !f.s.StopTurn("parent") {
		t.Fatal("stop failed")
	}
	f.end(t, "parent", "stopped")
	edit := queueHold(f, "second", "editor", 0)
	postQueueChange(t, f, edit, 200)
	postQueueChange(t, f, sendCard(f, "second", "stale", 1), 409)
	send := sendCard(f, "second", "press", 0)
	var replies [2]commandQueueView
	var codes [2]int
	var wait sync.WaitGroup
	for i := range replies {
		wait.Add(1)
		go func() {
			defer wait.Done()
			copy := send
			if i == 1 {
				copy.Operation = "other-tab"
			}
			replies[i], codes[i], _ = f.req("POST", copy)
		}()
	}
	wait.Wait()
	id := replies[0].SubmissionID
	if codes[0] != 200 || codes[1] != 200 || id == "" || id != replies[1].SubmissionID {
		t.Fatal(codes, replies)
	}
	done := f.end(t, id, "done")
	if done.Reply != "opencode/chosen: original second (high)" || done.NextTurnID == "" {
		t.Fatal(done)
	}
	if last := f.end(t, done.NextTurnID, "done"); last.Reply != "opencode/chosen: first (high)" || last.NextTurnID != "" {
		t.Fatal(last)
	}
	edit.Action, edit.Operation, edit.Text = "edit", "late", "overwritten"
	postQueueChange(t, f, edit, 409)
	if retry := postQueueChange(t, f, send, 200); retry.SubmissionID != id || len(retry.Items) != 0 {
		t.Fatal(retry)
	}
	send.ID = "first"
	postQueueChange(t, f, send, 409)
}

func TestQueueSendNowBusyDifferentAgentHonorsPressOrderAndCapturedSettings(t *testing.T) {
	f := newQueueFixture(t)
	for _, id := range []string{"a", "b", "c"} {
		postQueueChange(t, f, queuedText(f, id, id), 200)
	}
	command := f.submit("compact", "pi", "/compact")
	postQueueChange(t, f, command, 200)
	postQueueChange(t, f, sendCard(f, "compact", "invalid", 0), 409)
	if _, code, _ := f.req("DELETE", command); code != 200 {
		t.Fatal(code)
	}
	c := postQueueChange(t, f, sendCard(f, "c", "third-first", 0), 200)
	b := postQueueChange(t, f, sendCard(f, "b", "second-next", 0), 200)
	if c.Receipt.State != "submitted" || b.Receipt.State != "submitted" || c.SubmissionID == b.SubmissionID {
		t.Fatal(c, b)
	}
	close(f.provider.release)
	if parent := f.end(t, "parent", "done"); parent.NextTurnID != c.SubmissionID {
		t.Fatal(parent, c)
	}
	if done := f.end(t, c.SubmissionID, "done"); done.NextTurnID != b.SubmissionID || done.Reply != "opencode/chosen: c (high)" {
		t.Fatal(done)
	}
	done := f.end(t, b.SubmissionID, "done")
	if done.Reply != "opencode/chosen: b (high)" || f.end(t, done.NextTurnID, "done").Reply != "opencode/chosen: a (high)" {
		t.Fatal(done)
	}
}

// Real journal, active-turn registry, external history and input.applied
// callback; the provider's control channel is a deterministic in-memory fake.
func newQueueSteerTest(t *testing.T, write func(turn.Input) error) (*queueFixture, *extagent.TurnSteering) {
	t.Helper()
	root := t.TempDir()
	b := extagent.NewBroker(extagent.NewStateStore(root), map[extagent.AgentName]extagent.DetectionResult{
		extagent.AgentPi: {Agent: extagent.AgentPi, Preferred: &extagent.AgentTransport{Agent: extagent.AgentPi, Kind: extagent.TransportCLI}},
	}, nil)
	t.Cleanup(func() { b.Close() })
	svc := NewService(ServiceOptions{WorkspaceRoot: root, ExternalBroker: b})
	s, err := svc.store.Create(session.CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	ch := NewWebUIChannel(svc, true)
	ctx, stop := context.WithCancel(context.Background())
	ch.Start(ctx)
	t.Cleanup(stop)
	parent, _, _ := svc.webTurns.start(ctx, "parent", s.ID())
	ch.bindWebTurn(svc, parent, s.ID())
	t.Cleanup(func() { svc.webTurns.expire(parent) })
	parent.inputTarget = extagent.SessionState{Agent: "pi", ExternalSessionID: "thread", Model: "p/m", Effort: "high"}
	q := &turn.InputQueue{}
	q.Close()
	unregister, ok := svc.activeTurns.RegisterWithInputs(parent.id, func() {}, q)
	if !ok {
		t.Fatal("register")
	}
	t.Cleanup(unregister)
	timeline := &externalTimeline{}
	hooks := timeline.steering(svc.activeTurns, parent.id, q, func(event turn.Event) {
		if event.Kind == "input.applied" {
			ch.appliedQueueInput(svc, parent, event)
		}
	}, &appruntime.Conversation{Session: s}, extagent.AgentPi)
	hooks.Ready(write, turn.Steering)
	return &queueFixture{s: svc, ch: ch, saved: s, parent: parent}, hooks
}

func TestQueueSendNowSteersFrozenMediaOnceAndWaitsForProviderConsumption(t *testing.T) {
	sent := make(chan turn.Input, 2)
	f, hooks := newQueueSteerTest(t, func(i turn.Input) error { sent <- i; return nil })
	const img = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
	file := "c25hcHNob3Q="
	row := f.submit("m", "pi", "/pi inspect")
	row.Kind, row.Model, row.Effort = "message", "p/m", "high"
	row.Images = []webUIImagePayload{{MIMEType: "image/png", DataBase64: img}}
	row.Files = []webUIFilePayload{{Name: "f.txt", DataBase64: &file}}
	postQueueChange(t, f, row, 200)
	v := postQueueChange(t, f, sendCard(f, "m", "send-one", 0), 200)
	if v.Receipt.State != "steering" || v.TurnID != "parent" {
		t.Fatal(v)
	}
	var input turn.Input
	select {
	case input = <-sent:
	case <-time.After(time.Second):
		t.Fatal("no native request")
	}
	if input.ID != v.SubmissionID || !strings.HasPrefix(input.Text, "inspect") || !strings.Contains(input.Text, "snapshot") || len(input.Images) != 1 || input.Images[0].Base64() != img {
		t.Fatal(input)
	}
	if again := postQueueChange(t, f, sendCard(f, "m", "send-two", 0), 200); again.SubmissionID != input.ID || again.Receipt.State != "steering" {
		t.Fatal(again)
	}
	hooks.Applied(input)
	last, code, body := f.req("GET", row)
	if code != 200 || last.Receipt.State != "done" || len(last.Items) != 0 || len(sent) != 0 {
		t.Fatal(body)
	}
	h, err := f.s.conversationHistory(f.saved.ID(), 0, true)
	if err != nil || len(h.Messages) != 1 || len(h.Messages[0].Images) != 1 || !strings.Contains(h.Messages[0].Content, "/pi inspect") {
		t.Fatal(h, err)
	}
	all, _, _, _, err := f.parent.batch(0)
	if err != nil || !strings.Contains(string(all), "event: queued_input_applied") || !strings.Contains(string(all), img) || strings.Contains(string(all), file) {
		t.Fatal(string(all), err)
	}
}

func waitCard(t *testing.T, f *queueFixture, id, state string) queuedCommand {
	t.Helper()
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) {
		v, code, body := f.req("GET", commandQueueRequest{Session: f.saved.ID(), ID: id})
		if code != 200 {
			t.Fatal(body)
		}
		if v.Receipt != nil && v.Receipt.State == state {
			return *v.Receipt
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("missing card state", id, state)
	return queuedCommand{}
}

func TestQueueSendNowRefusedCardRetainsOriginalReceiptAndUncertainSendCannotReplay(t *testing.T) {
	inputs := make(chan turn.Input, 4)
	f, _ := newQueueSteerTest(t, func(i turn.Input) error {
		inputs <- i
		if len(inputs) == 1 {
			return turn.ErrInputRejected
		}
		return errors.New("connection lost")
	})
	m := f.submit("one", "pi", "original")
	m.Kind = "message"
	postQueueChange(t, f, m, 200)
	press := sendCard(f, "one", "press-one", 0)
	a := postQueueChange(t, f, press, 200)
	waitCard(t, f, "one", "returned")
	retry := press
	retry.Operation, retry.ExpectedTurn = "retry", a.SubmissionID
	b := postQueueChange(t, f, retry, 200)
	if a.SubmissionID == b.SubmissionID {
		t.Fatal(a, b)
	}
	w := httptest.NewRecorder()
	f.ch.handleCommandQueue(w, httptest.NewRequest(http.MethodGet, "/api/v1/webui/command-queue?session_id="+f.saved.ID()+"&id=one&operation_id=press-one", nil))
	var receipt commandQueueView
	if json.Unmarshal(w.Body.Bytes(), &receipt) != nil || !receipt.OperationCommitted || receipt.SubmissionID != a.SubmissionID {
		t.Fatal(w.Body.String())
	}
	waitCard(t, f, "one", "unknown")
	for _, op := range []string{"press-one", "also-pressed-before-retry"} {
		press.Operation = op
		v := postQueueChange(t, f, press, 200)
		if v.SubmissionID != a.SubmissionID {
			t.Fatal(v)
		}
	}
	retry.Operation, retry.ExpectedTurn = "unsafe-retry", b.SubmissionID
	postQueueChange(t, f, retry, 409)
	if len(inputs) != 2 {
		t.Fatal(len(inputs))
	}
}

func TestQueueSendNowRestartHoldsUnsentAndDoesNotRetryHandedSteers(t *testing.T) {
	f := newQueueFixture(t)
	close(f.provider.release)
	f.end(t, "parent", "done")
	q := commandQueue{Version: queueJournalVersion, Owner: "old-process", Entries: []queuedCommand{
		{ID: "reserved", State: "submitted", Kind: "message", TurnID: "handoff", Priority: 2, Text: "text"},
		{ID: "steered", State: "steering", Kind: "message", TurnID: "sent", SteerTurn: "old-turn", Text: "uncertain"},
	}}
	if err := q.save(f.saved); err != nil {
		t.Fatal(err)
	}
	v, code, body := f.req("GET", commandQueueRequest{Session: f.saved.ID()})
	if code != 200 || !v.Paused || v.TurnID != "" || len(v.Items) != 2 || v.Items[0].State != "waiting" || v.Items[0].TurnID != "" || v.Items[0].BodyVersion != 1 || v.Items[1].State != "unknown" {
		t.Fatal(body)
	}
}

// A compact requires an idle agent, but can bypass a held head once idle.
func TestQueueSendNowCompactsOnlyAtIdle(t *testing.T) {
	f := newQueueFixture(t)
	postQueueChange(t, f, queuedText(f, "held", "held"), 200)
	postQueueChange(t, f, f.submit("compact", "pi", "/compact"), 200)
	postQueueChange(t, f, sendCard(f, "compact", "press", 0), 409)
	f.s.StopTurn("parent")
	f.end(t, "parent", "stopped")
	postQueueChange(t, f, queueHold(f, "held", "edit", 0), 200)
	v := postQueueChange(t, f, sendCard(f, "compact", "press", 0), 200)
	if done := f.end(t, v.SubmissionID, "done"); done.ReplyAgentCommand != "compact" || done.NextTurnID != "" {
		raw, _ := json.Marshal(done)
		t.Fatal(string(raw))
	}
}

func TestQueueSendNowCanJoinItsStartingParentButKeepsDifferentModelsForNextTurn(t *testing.T) {
	sent := make(chan turn.Input, 3)
	f, hooks := newQueueSteerTest(t, func(i turn.Input) error { sent <- i; return nil })
	target := f.parent.inputTarget
	f.parent.inputTarget = extagent.SessionState{}
	a, b := f.submit("a", "pi", "first"), f.submit("b", "pi", "other model")
	a.Kind, a.Model, a.Effort = "message", target.Model, target.Effort
	b.Kind, b.Model, b.Effort = "message", "different", target.Effort
	for _, row := range []commandQueueRequest{a, b} {
		postQueueChange(t, f, row, 200)
		v := postQueueChange(t, f, sendCard(f, row.ID, "press-"+row.ID, 0), 200)
		if v.Receipt.State != "submitted" {
			t.Fatal(v)
		}
	}
	f.ch.deliverReadyCards(f.s, f.parent, &target)
	select {
	case input := <-sent:
		if input.Text != "first" {
			t.Fatal(input)
		}
		hooks.Applied(input)
	case <-time.After(time.Second):
		t.Fatal("ready native turn did not receive reserved input")
	}
	waitCard(t, f, "a", "done")
	waitCard(t, f, "b", "submitted")
	f.s.StopTurn("parent")
	waitCard(t, f, "b", "waiting")
	f.ch.finishWebTurn(f.s, WorkspaceInfo{}, f.parent, "stopped", map[string]string{"turn_id": "parent"})
	v, code, body := f.req("GET", b)
	if code != 200 || !v.Paused || v.TurnID != "" || v.Receipt.BodyVersion != 1 || len(sent) != 0 {
		t.Fatal(body)
	}
}

func TestQueueSendNowRecoversARecordedConsumptionWithoutResending(t *testing.T) {
	f := newQueueFixture(t)
	close(f.provider.release)
	f.end(t, "parent", "done")
	if err := f.saved.RecordMessage(map[string]any{"role": "user", "content": "delivered", "bq_external_input": "card-confirmed"}); err != nil {
		t.Fatal(err)
	}
	q := commandQueue{Version: queueJournalVersion, Owner: "old", Entries: []queuedCommand{
		{ID: "card", TurnID: "card-confirmed", SteerTurn: "lost", State: "steering", Text: "delivered", Kind: "message"},
	}}
	if err := q.save(f.saved); err != nil {
		t.Fatal(err)
	}
	v, code, body := f.req("GET", commandQueueRequest{Session: f.saved.ID(), ID: "card"})
	if code != 200 || v.Receipt.State != "done" || len(v.Items) != 0 || v.TurnID != "" {
		t.Fatal(body)
	}
}

func TestQueueUnconfirmedHandoffRemainsInHistoryAfterRemoveClearAndRestart(t *testing.T) {
	sent := make(chan turn.Input, 1)
	f, _ := newQueueSteerTest(t, func(input turn.Input) error { sent <- input; return nil })
	row := f.submit("draft", "pi", "uncertain content")
	row.Kind = "message"
	img := queueTestImage(t)
	row.Images = []webUIImagePayload{img}
	postQueueChange(t, f, row, 200)
	v := postQueueChange(t, f, sendCard(f, row.ID, "press", 0), 200)
	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("steer missing")
	}
	f.s.StopTurn("parent")
	f.ch.finishWebTurn(f.s, WorkspaceInfo{}, f.parent, "stopped", map[string]string{"turn_id": "parent"})
	check := func(svc *Service, want int) {
		h, err := svc.ConversationHistory(f.saved.ID(), 0)
		if err != nil {
			t.Fatal(err)
		}
		var notices int
		for _, item := range h.Messages {
			if item.Delivery != nil {
				notices++
				if item.Content != row.Text || item.Delivery.ID != v.SubmissionID || item.Delivery.State != "unknown" || len(item.Delivery.Attachments) != 1 {
					t.Fatal(item)
				}
			}
		}
		if notices != want {
			t.Fatal(h)
		}
	}
	check(f.s, 1)
	base, err := f.parent.history(t.Context(), f.s)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(base)
	if strings.Contains(string(raw), "uncertain content") {
		t.Fatal(string(raw))
	}
	if _, code, body := f.req("DELETE", row); code != 200 {
		t.Fatal(body)
	}
	if w := postContextClear(f.ch, f.saved.ID(), "clear-after-unknown", "/clear", ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	fresh := NewService(ServiceOptions{WorkspaceRoot: f.s.workspaceRoot})
	check(fresh, 1)
	ch := NewWebUIChannel(fresh, true)
	path := "/api/v1/webui/command-queue?view=attachment&source=handoff&session_id=" + f.saved.ID() + "&id=" + v.SubmissionID + "&index=0"
	w := httptest.NewRecorder()
	ch.handleCommandQueue(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != 200 || base64.StdEncoding.EncodeToString(w.Body.Bytes()) != img.DataBase64 {
		t.Fatal(w.Code)
	}
	records, err := f.saved.LoadMessages()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(records)
	if strings.Contains(string(raw), row.Text) {
		t.Fatal("unconfirmed text entered model history")
	}
}

func TestQueueHistoryFirstReadRecoversUnconfirmedHandoff(t *testing.T) {
	svc := NewService(ServiceOptions{WorkspaceRoot: t.TempDir()})
	s, err := svc.store.Create(session.CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	q := commandQueue{Owner: "exited", Entries: []queuedCommand{{ID: "draft", Kind: "message", State: "steering", SteerTurn: "parent", Text: "maybe delivered", TurnID: "card-unknown"}}}
	if err = q.save(s); err != nil {
		t.Fatal(err)
	}
	h, err := svc.ConversationHistory(s.ID(), 0)
	if err != nil || len(h.Messages) != 1 || h.Messages[0].Delivery == nil || h.Messages[0].Delivery.State != "unknown" {
		t.Fatal(h, err)
	}
	if svc.webTurns.queueLeases[s.ID()] != nil {
		t.Fatal("history reader retained a queue lease")
	}
}

func TestQueueHeldHandoffsRecoverInPressOrderAheadOfWaitingCards(t *testing.T) {
	q := commandQueue{Entries: []queuedCommand{
		{ID: "a", State: "waiting"}, {ID: "spent", State: "done"}, {ID: "b", State: "submitted", Priority: 3, TurnID: "B"},
		{ID: "c", State: "submitted", Priority: 2, TurnID: "C"}, {ID: "d", State: "waiting"},
	}}
	if !q.holdSubmitted() {
		t.Fatal("nothing held")
	}
	for i, id := range []string{"c", "spent", "b", "a", "d"} {
		if q.Entries[i].ID != id {
			t.Fatal(q.Entries)
		}
	}
	if q.Entries[0].BodyVersion != 1 || q.Entries[0].State != "waiting" || q.Entries[2].TurnID != "" || q.holdSubmitted() {
		t.Fatal(q.Entries)
	}
}

type queueCoreModel struct {
	chatModeRecordingClient
	once             sync.Once
	started, release chan struct{}
}

func (c *queueCoreModel) CreateChatCompletionStream(ctx context.Context, model string, msgs []map[string]any, defs []tools.Definition, chunk func(string)) (agent.AssistantMessage, error) {
	c.once.Do(func() {
		close(c.started)
		select {
		case <-ctx.Done():
		case <-c.release:
		}
	})
	return c.chatModeRecordingClient.CreateChatCompletionStream(ctx, model, msgs, defs, chunk)
}

func TestQueueSendNowBuiltinRunsTheSelectedImageInsideTheSameTurn(t *testing.T) {
	model := &queueCoreModel{started: make(chan struct{}), release: make(chan struct{})}
	s := NewService(ServiceOptions{WorkspaceRoot: t.TempDir(), Client: model, SystemPrompt: "base"})
	ch := NewWebUIChannel(s, true)
	saved, err := s.store.Create(session.CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	f := queueFixture{ch: ch, s: s, saved: saved}
	parent, ctx, _ := ch.registerWebTurn(s, TurnRequest{TurnID: "parent", SessionID: saved.ID()})
	t.Cleanup(func() { s.webTurns.expire(parent) })
	go ch.runWebTurn(ctx, s, WorkspaceInfo{}, TurnRequest{SessionID: saved.ID(), Message: "first", TurnID: "parent"}, parent)
	select {
	case <-model.started:
	case <-time.After(2 * time.Second):
		t.Fatal("model did not start")
	}
	m := f.submit("image", "bqagent", "")
	m.Kind = "message"
	img := queueTestImage(t)
	m.Images = []webUIImagePayload{img}
	postQueueChange(t, &f, m, 200)
	v := postQueueChange(t, &f, sendCard(&f, m.ID, "press", 0), 200)
	if v.Receipt.State != "steering" {
		t.Fatal(v)
	}
	// The registered built-in queue is invoked asynchronously after admission.
	reg := s.webTurns
	reg.queueMu.Lock()
	tail := parent.steerTail
	reg.queueMu.Unlock()
	<-tail
	close(model.release)
	f.end(t, parent.id, "done")
	last, code, body := f.req("GET", m)
	if code != 200 || last.Receipt.State != "done" || len(last.Items) != 0 {
		t.Fatal(body)
	}
	history, err := s.conversationHistory(saved.ID(), 0, true)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, row := range history.Messages {
		if row.Role == "user" && len(row.Images) > 0 {
			found++
		}
	}
	data, _ := json.Marshal(model.messages)
	if found != 1 || !strings.Contains(string(data), img.DataBase64) {
		t.Fatal(found, string(data))
	}
}

func TestQueueLateProofReplacesAnUnconfirmedHistoryNotice(t *testing.T) {
	sent := make(chan turn.Input, 1)
	f, hooks := newQueueSteerTest(t, func(i turn.Input) error { sent <- i; return errors.New("response lost") })
	request := f.submit("draft", "pi", "unique visible text")
	request.Kind = "message"
	request.Images = []webUIImagePayload{queueTestImage(t)}
	postQueueChange(t, f, request, 200)
	v := postQueueChange(t, f, sendCard(f, "draft", "press", 0), 200)
	var input turn.Input
	select {
	case input = <-sent:
	case <-time.After(time.Second):
		t.Fatal("no input")
	}
	waitCard(t, f, "draft", "unknown")
	if _, code, body := f.req("DELETE", request); code != 200 {
		t.Fatal(body)
	}
	hooks.Applied(input)
	waitCard(t, f, "draft", "done")
	h, err := f.s.conversationHistory(f.saved.ID(), 0, true)
	if err != nil || len(h.Messages) != 1 || h.Messages[0].Delivery != nil || len(h.Messages[0].Images) != 1 {
		t.Fatal(h, err)
	}
	q, err := readCommandQueue(f.saved, "test")
	if err != nil || q.History[v.SubmissionID].ID != "" {
		t.Fatal(q, err)
	}
}
