package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bqagent/internal/extagent"
	"bqagent/internal/session"
)

type queueFixture struct {
	ch       *WebUIChannel
	s        *Service
	saved    *session.Session
	parent   *webTurn
	provider *replayTestAgent
	shutdown context.CancelFunc
}

func newQueueFixture(t *testing.T) *queueFixture {
	return newQueueFixtureWithImages(t, nil)
}

func newQueueFixtureWithImages(t *testing.T, imageClient *imagePromptClient) *queueFixture {
	t.Helper()
	root := t.TempDir()
	p := &replayTestAgent{groupFakeACP: groupFakeACP{name: "cursor"}, started: make(chan struct{}), release: make(chan struct{})}
	b := extagent.NewBroker(extagent.NewStateStore(root), map[extagent.AgentName]extagent.DetectionResult{
		extagent.AgentCursor:   {Preferred: &extagent.AgentTransport{Agent: extagent.AgentCursor, Kind: extagent.TransportACP}},
		extagent.AgentOpenCode: {Preferred: &extagent.AgentTransport{Agent: extagent.AgentOpenCode, Kind: extagent.TransportACP, ACPImages: imageClient != nil, Command: extagent.CommandSpec{Command: "opencode"}}},
		extagent.AgentCodex:    {Preferred: &extagent.AgentTransport{Agent: extagent.AgentCodex, Kind: extagent.TransportAppServer}},
		extagent.AgentPi:       {Preferred: &extagent.AgentTransport{Agent: extagent.AgentPi, Kind: extagent.TransportCLI}},
	}, func(_ context.Context, spec extagent.CommandSpec, _ string) (extagent.ACPClient, error) {
		if spec.Command == "opencode" {
			if imageClient != nil {
				return imageClient, nil
			}
			return &singleModelClient{groupFakeACP{name: "opencode", log: &groupACPLog{prompts: map[string][]string{}}}}, nil
		}
		return p, nil
	})
	s := NewService(ServiceOptions{WorkspaceRoot: root, ExternalBroker: b, Client: &chatModeRecordingClient{}})
	saved, err := s.store.Create(session.CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	ch := NewWebUIChannel(s, true)
	ctx, shutdown := context.WithCancel(context.Background())
	ch.Start(ctx)
	j, run, _ := s.webTurns.start(ctx, "parent", saved.ID())
	go ch.runWebTurn(run, s, WorkspaceInfo{}, TurnRequest{SessionID: saved.ID(), TurnID: "parent", Message: "examine", Agent: extagent.AgentCursor}, j)
	select {
	case <-p.started:
	case <-time.After(2 * time.Second):
		t.Fatal("parent did not start")
	}
	t.Cleanup(func() { shutdown(); b.Close() })
	return &queueFixture{ch, s, saved, j, p, shutdown}
}
func (f *queueFixture) req(method string, body commandQueueRequest) (commandQueueView, int, string) {
	var r *http.Request
	if method == http.MethodPost {
		data, _ := json.Marshal(body)
		r = httptest.NewRequest(method, "/api/v1/webui/command-queue", strings.NewReader(string(data)))
	} else {
		q := url.Values{"workspace_id": {body.Workspace}, "session_id": {body.Session}, "id": {body.ID}}
		if body.BodyVersion != nil {
			q.Set("body_version", fmt.Sprint(*body.BodyVersion))
		}
		r = httptest.NewRequest(method, "/api/v1/webui/command-queue?"+q.Encode(), nil)
	}
	w := httptest.NewRecorder()
	f.ch.handleCommandQueue(w, r)
	var view commandQueueView
	_ = json.Unmarshal(w.Body.Bytes(), &view)
	return view, w.Code, w.Body.String()
}
func (f *queueFixture) submit(id string, agent extagent.AgentName, text string) commandQueueRequest {
	return commandQueueRequest{Action: "enqueue", Parent: "parent", Session: f.saved.ID(), ID: id, Agent: agent, Text: text}
}
func (f *queueFixture) end(t *testing.T, id, kind string) webUIDoneEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	w := httptest.NewRecorder()
	f.ch.handleWebTurn(w, httptest.NewRequest(http.MethodGet, "/api/v1/webui/turns?compact=1&turn_id="+id, nil).WithContext(ctx))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "event: "+kind+"\n") {
		t.Fatal(id, w.Code, w.Body.String())
	}
	var done webUIDoneEvent
	if kind == "done" || kind == "stopped" {
		_, tail, _ := strings.Cut(w.Body.String(), "event: "+kind+"\ndata: ")
		if err := json.Unmarshal([]byte(tail), &done); err != nil {
			t.Fatal(err)
		}
	}
	return done
}

func TestWebUIQueuedTextCapturesAgentModelAndEffortBeforeFIFOHandOff(t *testing.T) {
	f := newQueueFixture(t)
	store := extagent.NewStateStore(f.s.workspaceRoot)
	if err := store.Save(extagent.SessionState{BQSessionID: f.saved.ID(), Agent: extagent.AgentOpenCode,
		Transport: extagent.TransportACP, ExternalSessionID: "original", Model: "saved-model", Effort: "high"}); err != nil {
		t.Fatal(err)
	}
	message := f.submit("question", extagent.AgentOpenCode, "解释测试失败的原因")
	message.Kind, message.Model, message.Effort = "message", "chosen-model", "medium"
	saved := f.submit("follow", extagent.AgentOpenCode, "/opencode 给出修复步骤")
	saved.Kind = "message" // Capture the pending target's saved settings, not the previous card's.
	compact := f.submit("compact", extagent.AgentPi, "/compact keep decisions")
	for _, req := range []commandQueueRequest{message, compact, saved, message} {
		if v, code, text := f.req("POST", req); code != 200 || v.Receipt == nil || v.Receipt.State != "waiting" {
			t.Fatal(code, text)
		}
	}
	conflict := message
	conflict.Model = "wrong"
	if _, code, text := f.req("POST", conflict); code != 409 {
		t.Fatal(code, text)
	}
	for _, invalid := range []string{"/compact", "/ask", "/pi other", " "} {
		bad := message
		bad.ID, bad.Text = "bad", invalid
		if _, code, text := f.req("POST", bad); code != 400 {
			t.Fatal(code, text)
		}
	}
	close(f.provider.release)
	p := f.end(t, f.parent.id, "done")
	first := f.end(t, p.NextTurnID, "done")
	second := f.end(t, first.NextTurnID, "done")
	last := f.end(t, second.NextTurnID, "done")
	if first.Reply != "opencode/chosen-model: "+message.Text+" (medium)" || second.ReplyAgentCommand != "compact" ||
		last.Reply != "opencode/saved-model: 给出修复步骤 (high)" || last.NextTurnID != "" {
		t.Fatal(first, second, last)
	}
	v, code, text := f.req("POST", message)
	if code != 200 || len(v.Items) != 0 || v.Receipt == nil || v.Receipt.TurnID != p.NextTurnID || f.provider.calls.Load() != 1 {
		t.Fatal(text)
	}
	journal, err := readCommandQueue(f.saved, "")
	if err != nil || journal.find(message.ID).Text != "" || journal.find(message.ID).TextDigest == "" || len(journal.Bindings) != 0 {
		t.Fatal("settled receipt retained the prompt or binding", err)
	}
	conflict = message
	conflict.Text = "different question"
	if _, code, _ := f.req("POST", conflict); code != 409 {
		t.Fatal("accepted a conflicting retry", code)
	}
	history, err := f.s.ConversationHistory(f.saved.ID(), 0)
	if err != nil || len(history.Messages) != 8 || history.Messages[2].Content != message.Text || history.Messages[6].Content != saved.Text {
		t.Fatal(history, err)
	}
}

func TestWebUIQueuedCodexServiceTierSurvivesReplayAndRequiresMatchingRunningTurn(t *testing.T) {
	f := newQueueFixture(t)
	card := f.submit("tier", extagent.AgentCodex, "review")
	card.Kind, card.Model, card.ServiceTier = "message", "quick", "priority"
	for range 2 {
		view, code, body := f.req("POST", card)
		if code != 200 || view.Receipt == nil || view.Receipt.Picks == nil || view.Receipt.Picks.ServiceTier != "priority" {
			t.Fatal(code, body)
		}
		running := extagent.SessionState{Agent: extagent.AgentCodex, Model: "quick", ServiceTier: "default"}
		if queueTargetMatches(running, view.Receipt, "") {
			t.Fatal("different tier accepted as steering")
		}
		running.ServiceTier = "priority"
		if queueTargetMatches(running, view.Receipt, "") {
			t.Fatal("steering would lose the explicit tier preference")
		}
		running.PreferredServiceTier = "priority"
		if !queueTargetMatches(running, view.Receipt, "") {
			t.Fatal("matching tier rejected")
		}
	}
	conflict := card
	conflict.ServiceTier = "default"
	if _, code, body := f.req("POST", conflict); code != 409 {
		t.Fatal(code, body)
	}
	inherited := card
	inherited.ID, inherited.TierInherited = "inherited", true
	view, code, body := f.req("POST", inherited)
	if code != 200 || view.Receipt == nil || view.Receipt.Picks == nil || !view.Receipt.Picks.TierInherited ||
		!queueTargetMatches(extagent.SessionState{Agent: extagent.AgentCodex, Model: "quick", ServiceTier: "priority"}, view.Receipt, "") {
		t.Fatal(code, body)
	}
	if _, code, body := f.req("DELETE", inherited); code != 200 {
		t.Fatal(code, body)
	}
	for _, target := range []extagent.AgentName{extagent.AgentPi, builtinChatAgent} {
		rejected := card
		rejected.ID, rejected.Agent = string(target), target
		if _, code, body := f.req("POST", rejected); code != 400 {
			t.Fatal(code, body)
		}
	}
	if _, code, body := f.req("DELETE", card); code != 200 {
		t.Fatal(code, body)
	}
	close(f.provider.release)
	f.end(t, "parent", "done")
}

func TestWebUIQueuedMessageRefusesReplacedNativeSessionAndPausesRemainingCards(t *testing.T) {
	f := newQueueFixture(t)
	store := extagent.NewStateStore(f.s.workspaceRoot)
	state := extagent.SessionState{BQSessionID: f.saved.ID(), Agent: extagent.AgentOpenCode,
		Transport: extagent.TransportACP, ExternalSessionID: "original", Model: "known"}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	first := f.submit("stale", extagent.AgentOpenCode, "检查原任务")
	first.Kind = "message"
	last := f.submit("wait", extagent.AgentPi, "/compact later")
	for _, req := range []commandQueueRequest{first, last} {
		if _, code, text := f.req("POST", req); code != 200 {
			t.Fatal(text)
		}
	}
	state.ExternalSessionID = "replacement"
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	close(f.provider.release)
	next := f.end(t, "parent", "done").NextTurnID
	f.end(t, next, "error")
	v, code, text := f.req("GET", first)
	if code != 200 || !v.Paused || len(v.Items) != 2 || v.Items[0].State != "failed" ||
		!strings.Contains(v.Items[0].Error, "已") || !strings.Contains(v.Items[0].Error, "更换") || v.Items[1].State != "waiting" {
		t.Fatal(text)
	}
	v, _, _ = f.req("POST", first)
	if v.TurnID != "" || v.Receipt.TurnID != next || v.Receipt.State != "failed" {
		t.Fatal("replayed the refused handoff", v)
	}
}

func TestWebUIQueuedMessageToBuiltinUsesPlainTextAndPreservesExternalBinding(t *testing.T) {
	f := newQueueFixture(t)
	req := f.submit("internal", builtinChatAgent, "解释上一条结论")
	req.Kind, req.Effort = "message", "HIGH"
	v, code, raw := f.req("POST", req)
	if code != 200 || v.Receipt == nil || v.Receipt.Picks.Effort != "high" {
		t.Fatal(raw)
	}
	bad := req
	bad.ID, bad.Text = "bad", "/default"
	if _, code, _ = f.req("POST", bad); code != 400 {
		t.Fatal(code)
	}
	close(f.provider.release)
	id := f.end(t, "parent", "done").NextTurnID
	done := f.end(t, id, "done")
	if done.Reply != "只读结果" || done.ActiveAgent != "" || done.ReplyAgent != "" {
		t.Fatal(done)
	}
	h, err := f.s.ConversationHistory(f.saved.ID(), 0)
	if err != nil || len(h.Messages) != 4 || h.Messages[2].Content != req.Text {
		t.Fatal(h, err)
	}
	// Choosing bqagent leaves the external agent's original thread available.
	state, err := f.s.externalBroker.QueuedMessageState(t.Context(), extagent.AgentCursor, f.saved.ID(), "", "", "", false)
	if err != nil || state.ExternalSessionID != "cursor-session" {
		t.Fatal(state, err)
	}
}

func TestWebUIQueueStopAndResumeBeforeParentSettles(t *testing.T) {
	f := newQueueFixture(t)
	close(f.provider.release)
	f.end(t, "parent", "done")
	parent, _, _ := f.s.webTurns.start(f.ch.lifetime, "settling", f.saved.ID())
	f.ch.bindWebTurn(f.s, parent, f.saved.ID())
	t.Cleanup(func() { f.s.webTurns.expire(parent) })
	req := f.submit("next", extagent.AgentPi, "/compact")
	req.Parent = parent.id
	if _, code, text := f.req("POST", req); code != 200 {
		t.Fatal(text)
	}
	if !f.s.StopTurn(parent.id) {
		t.Fatal("failed to stop")
	}
	if v, _, _ := f.req("GET", req); !v.Paused || v.TurnID != "" {
		t.Fatal(v)
	}
	if v, code, text := f.req("POST", commandQueueRequest{Action: "resume", Session: f.saved.ID()}); code != 200 || v.Paused || v.TurnID != "" {
		t.Fatal(text)
	}
	f.ch.finishWebTurn(f.s, WorkspaceInfo{}, parent, "stopped", map[string]string{"turn_id": parent.id})
	stop := f.end(t, parent.id, "stopped")
	if stop.NextTurnID == "" || f.end(t, stop.NextTurnID, "done").ReplyAgentCommand != "compact" {
		t.Fatal(stop)
	}
}

func TestWebUIQueueOwnershipAndDeletionAcrossServices(t *testing.T) {
	f := newQueueFixture(t)
	req := f.submit("remaining", extagent.AgentPi, "/compact kept")
	if _, code, text := f.req("POST", req); code != 200 {
		t.Fatal(text)
	}
	second := NewService(ServiceOptions{WorkspaceRoot: f.s.workspaceRoot, ExternalBroker: f.s.externalBroker})
	peer := queueFixture{ch: NewWebUIChannel(second, true)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	peer.ch.Start(ctx)
	if v, code, text := peer.req("GET", req); code != 200 || v.Paused || v.Receipt == nil || v.Receipt.State != "waiting" {
		t.Fatal(text)
	}
	if _, code, text := peer.req("POST", req); code != 200 {
		t.Fatal("matching retry", text)
	}
	resume := commandQueueRequest{Action: "resume", Session: f.saved.ID()}
	if _, code, text := peer.req("POST", resume); code != 409 {
		t.Fatal("concurrent resume", text)
	}
	w := httptest.NewRecorder()
	(&handler{service: second}).deleteConversation(w, second, f.saved.ID())
	if w.Code != 409 {
		t.Fatal("concurrent delete", w.Code)
	}
	f.s.StopTurn("parent")
	f.end(t, "parent", "stopped")
	f.shutdown()
	// Wait for the lifetime callback to release the OS lease.
	deadline := time.Now().Add(2 * time.Second)
	var v commandQueueView
	for time.Now().Before(deadline) {
		var code int
		v, code, _ = peer.req("POST", resume)
		if code == 200 {
			break
		}
		if code != 409 {
			t.Fatal(code)
		}
		time.Sleep(time.Millisecond)
	}
	if v.TurnID == "" {
		t.Fatal("peer did not acquire the queue")
	}
	peer.end(t, v.TurnID, "done")
	history, err := second.ConversationHistory(f.saved.ID(), 0)
	if err != nil || history.Messages[len(history.Messages)-2].Content != "/pi /compact kept" {
		t.Fatal(history, err)
	}
	w = httptest.NewRecorder()
	(&handler{service: second}).deleteConversation(w, second, f.saved.ID())
	lock := filepath.Join(filepath.Dir(f.saved.Dir()), queueLockName(f.saved))
	if _, err = os.Stat(lock); w.Code != 200 || !os.IsNotExist(err) {
		t.Fatal(w.Code, err)
	}
	if _, code, _ := peer.req("POST", req); code != 404 {
		t.Fatal("request recreated deleted session", code)
	}
}

func TestWebUIQueueFreshSendCannotOverrideANewerStopOrResumeInAskMode(t *testing.T) {
	f := newQueueFixture(t)
	check := f.submit("waiting", extagent.AgentPi, "/compact")
	if _, code, text := f.req("POST", check); code != 200 {
		t.Fatal(text)
	}
	f.s.StopTurn("parent")
	f.end(t, "parent", "stopped")
	admit := func(id string) *webTurn {
		j, _, created := f.ch.registerWebTurn(f.s, TurnRequest{SessionID: f.saved.ID(), TurnID: id})
		if !created || !j.freshAfterPause {
			t.Fatal("send did not observe the pause", id)
		}
		t.Cleanup(func() { f.s.webTurns.expire(j) })
		return j
	}
	old := admit("early-user")
	newer := admit("another-user")
	if !f.s.StopTurn(newer.id) {
		t.Fatal("new Stop did not apply")
	}
	f.ch.bindWebTurn(f.s, old, f.saved.ID())
	f.ch.finishWebTurn(f.s, WorkspaceInfo{}, old, "done", webUIDoneEvent{})
	f.ch.finishWebTurn(f.s, WorkspaceInfo{}, newer, "stopped", map[string]string{})
	if v, _, _ := f.req("GET", check); !v.Paused || v.TurnID != "" {
		t.Fatal(v)
	}
	ask := admit("ask")
	if err := f.saved.SetCurrentMode("ask"); err != nil {
		t.Fatal(err)
	}
	f.ch.bindWebTurn(f.s, ask, f.saved.ID())
	f.ch.finishWebTurn(f.s, WorkspaceInfo{}, ask, "done", webUIDoneEvent{})
	if v, _, _ := f.req("GET", check); !v.Paused || v.TurnID != "" {
		t.Fatal(v)
	}
	run := admit("later-user")
	if err := f.saved.SetCurrentMode("run"); err != nil {
		t.Fatal(err)
	}
	f.ch.bindWebTurn(f.s, run, f.saved.ID())
	if v, _, _ := f.req("GET", check); v.Paused || v.TurnID != "" {
		t.Fatal(v)
	}
	f.ch.finishWebTurn(f.s, WorkspaceInfo{}, run, "done", webUIDoneEvent{})
	next := f.end(t, run.id, "done").NextTurnID
	if next == "" || f.end(t, next, "done").ReplyAgent != extagent.AgentPi {
		t.Fatal(next)
	}
}

func TestWebUIDeletingPausedQueueDropsWaitingMessagesAndReplay(t *testing.T) {
	f := newQueueFixture(t)
	message := f.submit("drop", extagent.AgentOpenCode, "不应发送")
	message.Kind = "message"
	if _, code, text := f.req("POST", message); code != 200 {
		t.Fatal(text)
	}
	f.s.StopTurn("parent")
	f.end(t, "parent", "stopped")
	w := httptest.NewRecorder()
	(&handler{service: f.s}).deleteConversation(w, f.s, f.saved.ID())
	if w.Code != 200 || f.s.webTurns.get("parent") != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, code, _ := f.req("POST", message); code != 404 {
		t.Fatal(code)
	}
	for _, path := range []string{f.saved.Dir(), filepath.Join(filepath.Dir(f.saved.Dir()), queueLockName(f.saved))} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal(path, err)
		}
	}
}

func TestWebUIQueuedCompactsDrainInOrderAcrossDisconnectAndDuplicatePost(t *testing.T) {
	f := newQueueFixture(t)
	first := f.submit("once", extagent.AgentCodex, "/compact")
	first.Session = "" // first browser has not yet read turn_session
	var g sync.WaitGroup
	for range 3 {
		g.Go(func() {
			v, code, text := f.req("POST", first)
			if code != 200 || len(v.Items) != 1 || v.Items[0].State != "waiting" || v.Receipt == nil {
				t.Errorf("%d %s", code, text)
			}
		})
	}
	g.Wait()
	discard := f.submit("discard", extagent.AgentPi, "/compact discard")
	if _, code, text := f.req("POST", discard); code != 200 {
		t.Fatal(text)
	}
	last := f.submit("last", extagent.AgentPi, "/compact keep decisions")
	if _, code, text := f.req("POST", last); code != 200 {
		t.Fatal(text)
	}
	v, code, text := f.req("DELETE", discard)
	if code != 200 || len(v.Items) != 2 || v.Receipt.State != "withdrawn" {
		t.Fatal(text)
	}
	conflict := first
	conflict.Text = "/compact different"
	if _, code, _ := f.req("POST", conflict); code != 409 {
		t.Fatal(code)
	}
	if f.s.webTurns.active(f.saved.ID()) != "parent" {
		t.Fatal("started during parent")
	}
	close(f.provider.release)
	parent := f.end(t, "parent", "done")
	if parent.NextTurnID == "" {
		t.Fatal(parent)
	}
	firstDone := f.end(t, parent.NextTurnID, "done")
	if firstDone.ReplyAgentCommand != "compact" || firstDone.ReplyAgent != extagent.AgentCodex || firstDone.NextTurnID == "" {
		t.Fatal(firstDone)
	}
	final := f.end(t, firstDone.NextTurnID, "done")
	if final.ReplyAgent != extagent.AgentPi || final.NextTurnID != "" {
		t.Fatal(final)
	}
	v, code, text = f.req("GET", last)
	if code != 200 || len(v.Items) != 0 || v.Receipt.State != "done" || v.Paused {
		t.Fatal(text)
	}
	f.s.webTurns.expire(f.parent)
	first.Session = f.saved.ID()
	if v, code, text = f.req("POST", first); code != 200 || v.Receipt.State != "done" {
		t.Fatal(text)
	}
	h, err := f.s.ConversationHistory(f.saved.ID(), 0)
	if err != nil || len(h.Messages) != 6 || h.Messages[2].Content != "/codex /compact" || h.Messages[4].Content != "/pi /compact keep decisions" || f.provider.calls.Load() != 1 {
		t.Fatal(h.Messages, err)
	}
}

func TestWebUIQueuedCompactsPauseOnStopAndResumeOnlyWaitingWork(t *testing.T) {
	f := newQueueFixture(t)
	for _, id := range []string{"first", "second"} {
		if _, code, text := f.req("POST", f.submit(id, extagent.AgentPi, "/compact "+id)); code != 200 {
			t.Fatal(text)
		}
	}
	if !f.s.StopTurn("parent") {
		t.Fatal("unable to stop parent")
	}
	f.end(t, "parent", "stopped")
	v, code, text := f.req("GET", commandQueueRequest{Session: f.saved.ID()})
	if code != 200 || !v.Paused || len(v.Items) != 2 || f.s.webTurns.active(f.saved.ID()) != "" {
		t.Fatal(text)
	}
	if v, code, text = f.req("DELETE", f.submit("first", extagent.AgentPi, "")); code != 200 || len(v.Items) != 1 || !v.Paused {
		t.Fatal(text)
	}
	v, code, text = f.req("POST", commandQueueRequest{Session: f.saved.ID(), Action: "resume"})
	if code != 200 || v.TurnID == "" || v.Paused {
		t.Fatal(text)
	}
	done := f.end(t, v.TurnID, "done")
	if done.ReplyAgent != extagent.AgentPi || done.NextTurnID != "" {
		t.Fatal(done)
	}
	h, err := f.s.ConversationHistory(f.saved.ID(), 0)
	if err != nil || h.Messages[len(h.Messages)-2].Content != "/pi /compact second" {
		t.Fatal(h.Messages, err)
	}
}

func TestWebUIQueueRestartHoldsWaitingAndDoesNotReplayUnknownDispatch(t *testing.T) {
	f := newQueueFixture(t)
	close(f.provider.release)
	f.end(t, "parent", "done")
	q := &commandQueue{Version: 1, Owner: "previous-server", Revision: 7, Entries: []queuedCommand{
		{ID: "ambiguous", Parent: "old", Agent: extagent.AgentPi, Text: "/compact never resend", State: "running", TurnID: "previous"},
		{ID: "waiting", Parent: "old", Agent: extagent.AgentPi, Text: "/compact later", State: "waiting"},
	}}
	if err := q.save(f.saved); err != nil {
		t.Fatal(err)
	}
	check := commandQueueRequest{Session: f.saved.ID(), ID: "ambiguous"}
	view, code, text := f.req("GET", check)
	if code != 200 || !view.Paused || len(view.Items) != 2 || view.Receipt.State != "unknown" || view.TurnID != "" {
		t.Fatal(text)
	}
	view, _, _ = f.req("POST", commandQueueRequest{Session: f.saved.ID(), Action: "resume"})
	if view.TurnID == "" {
		t.Fatal("waiting work not resumed")
	}
	f.end(t, view.TurnID, "done")
	view, _, _ = f.req("GET", check)
	if len(view.Items) != 1 || view.Items[0].State != "unknown" {
		t.Fatal(view)
	}
	h, err := f.s.ConversationHistory(f.saved.ID(), 0)
	if err != nil || len(h.Messages) != 4 || h.Messages[2].Content != "/pi /compact later" {
		t.Fatal(h.Messages, err)
	}
}

func TestWebUICommandQueueChecksSessionAndTargetBeforeAdmission(t *testing.T) {
	f := newQueueFixture(t)
	defer func() { close(f.provider.release); f.end(t, "parent", "done") }()
	other, err := f.s.store.Create(session.CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	req := f.submit("guard", extagent.AgentPi, "/compact")
	for _, run := range []struct {
		mutate func(*commandQueueRequest)
		status int
	}{
		{func(r *commandQueueRequest) { r.Session = other.ID() }, 409},
		{func(r *commandQueueRequest) { r.Workspace = "foreign" }, 404},
		{func(r *commandQueueRequest) { r.Text = "/model change" }, 400},
		{func(r *commandQueueRequest) { r.Agent = extagent.AgentCursor }, 400},
		{func(r *commandQueueRequest) { r.Agent = extagent.AgentCodex; r.Text = "/compact ignored" }, 400},
	} {
		r := req
		run.mutate(&r)
		if _, code, text := f.req("POST", r); code != run.status {
			t.Fatal(code, text)
		}
	}
	if entries, err := os.ReadDir(f.saved.Dir()); err != nil || strings.Contains(fmt.Sprint(entries), "webui-command-queue.json") {
		t.Fatal(entries, err)
	}
}
