package server

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"bqagent/internal/extagent"
)

func postQueueChange(t *testing.T, f *queueFixture, req commandQueueRequest, status int) commandQueueView {
	t.Helper()
	view, got, text := f.req("POST", req)
	if got != status {
		t.Fatalf("%s: status %d, response %s", req.Action, got, text)
	}
	return view
}

func queuedText(f *queueFixture, id, text string) commandQueueRequest {
	req := f.submit(id, extagent.AgentOpenCode, text)
	req.Kind, req.Model, req.Effort = "message", "chosen", "high"
	return req
}

func queueHold(f *queueFixture, id, token string, version uint64) commandQueueRequest {
	return commandQueueRequest{Action: "hold", Session: f.saved.ID(), ID: id, EditID: token, BodyVersion: &version}
}

func TestWebUIQueueEditHoldsHeadUntilLastEditorCommitsAndReplaysOriginalEnqueue(t *testing.T) {
	f := newQueueFixture(t)
	message := queuedText(f, "one", "原稿")
	postQueueChange(t, f, message, 200)
	postQueueChange(t, f, queuedText(f, "two", "第二条"), 200)
	first, second := queueHold(f, "one", "tab-a", 0), queueHold(f, "one", "tab-b", 0)
	postQueueChange(t, f, first, 200)
	postQueueChange(t, f, second, 200)
	close(f.provider.release)
	if done := f.end(t, "parent", "done"); done.NextTurnID != "" {
		t.Fatal(done)
	}
	first.Action = "release"
	if v := postQueueChange(t, f, first, 200); !v.Receipt.Editing || v.TurnID != "" {
		t.Fatal(v)
	}
	second.Action, second.Operation, second.Text = "edit", "save-b", "最终稿"
	v := postQueueChange(t, f, second, 200)
	if v.TurnID == "" || v.Receipt.Editing || v.Receipt.BodyVersion != 1 {
		t.Fatal(v)
	}
	done := f.end(t, v.TurnID, "done")
	if done.Reply != "opencode/chosen: 最终稿 (high)" || done.NextTurnID == "" {
		t.Fatal(done)
	}
	f.end(t, done.NextTurnID, "done")
	for _, replay := range []commandQueueRequest{message, second} {
		got := postQueueChange(t, f, replay, 200)
		if len(got.Items) != 0 || got.Receipt.State != "done" || got.Receipt.TurnID != v.TurnID {
			t.Fatal(got)
		}
	}
	h, err := f.s.ConversationHistory(f.saved.ID(), 0)
	if err != nil || len(h.Messages) != 6 || h.Messages[2].Content != "最终稿" || h.Messages[4].Content != "第二条" {
		t.Fatal(h, err)
	}
	changed := message
	changed.Text = second.Text
	postQueueChange(t, f, changed, 409)
}

func TestWebUIQueueEditProtectsConcurrentDraftsAndPreservesPause(t *testing.T) {
	f := newQueueFixture(t)
	original := queuedText(f, "one", "original")
	postQueueChange(t, f, original, 200)
	f.s.StopTurn("parent")
	f.end(t, "parent", "stopped")
	a, b := queueHold(f, "one", "a", 0), queueHold(f, "one", "b", 0)
	postQueueChange(t, f, a, 200)
	postQueueChange(t, f, b, 200)
	a.Action, a.Operation, a.Text = "edit", "save-a", "/pi 不应切换目标"
	postQueueChange(t, f, a, 400)
	a.Text = "first edit"
	if v := postQueueChange(t, f, a, 200); !v.Paused || v.TurnID != "" || v.Receipt.Editing || v.Receipt.BodyVersion != 1 || v.Receipt.Picks.Model != "chosen" {
		t.Fatal(v)
	}
	b.Action, b.Operation, b.Text = "edit", "save-b", "stale"
	postQueueChange(t, f, b, 409)
	b = queueHold(f, "one", "b-new", 1)
	postQueueChange(t, f, b, 200)
	b.Action, b.Operation, b.Text = "edit", "save-b-new", "latest"
	postQueueChange(t, f, b, 200)
	if v := postQueueChange(t, f, a, 200); v.Receipt.Text != "latest" || v.Receipt.BodyVersion != 2 {
		t.Fatal(v)
	}
	a.Text = "reuse-id"
	postQueueChange(t, f, a, 409)
	postQueueChange(t, f, original, 200)
	oldVersion := uint64(0)
	if _, code, _ := f.req("DELETE", commandQueueRequest{Session: f.saved.ID(), ID: original.ID, BodyVersion: &oldVersion}); code != 409 {
		t.Fatal("stale viewer withdrew a newer edit", code)
	}
	if _, code, _ := f.req("DELETE", original); code != 409 {
		t.Fatal("legacy viewer withdrew an edited row", code)
	}
	latestVersion := uint64(2)
	if v, code, _ := f.req("DELETE", commandQueueRequest{Session: f.saved.ID(), ID: original.ID, BodyVersion: &latestVersion}); code != 200 || v.Receipt.State != "withdrawn" {
		t.Fatal(v, code)
	}
}

func TestWebUIQueueEditsLegacyCompactWithoutLosingItsOriginalReceipt(t *testing.T) {
	f := newQueueFixture(t)
	close(f.provider.release)
	f.end(t, "parent", "done")
	file := filepath.Join(f.saved.Dir(), "webui-command-queue.json")
	// This is a real v1 record including the fields clients used before editing.
	legacy := `{"version":1,"owner":"old","revision":7,"paused":true,"entries":[{"id":"item","parent":"parent","agent":"pi","text":"/compact first","state":"waiting"}]}`
	if err := os.WriteFile(file, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	hold := queueHold(f, "item", "legacy-edit", 0)
	postQueueChange(t, f, hold, 200)
	save := hold
	save.Action, save.Operation, save.Text = "edit", "rewrite", "/clear"
	postQueueChange(t, f, save, 400)
	save.Text = "/compact keep the decisions"
	v := postQueueChange(t, f, save, 200)
	if !v.Paused || v.Receipt.Text != save.Text || v.Receipt.BodyVersion != 1 {
		t.Fatal(v)
	}
	postQueueChange(t, f, f.submit("item", extagent.AgentPi, "/compact first"), 200)
	q, err := readCommandQueue(f.saved, "")
	if err != nil || q.Version != queueJournalVersion {
		t.Fatal(q, err)
	}

	peerService := NewService(ServiceOptions{WorkspaceRoot: f.s.workspaceRoot, ExternalBroker: f.s.externalBroker})
	peer := &queueFixture{ch: NewWebUIChannel(peerService, true)}
	if v := postQueueChange(t, peer, save, 200); v.Receipt.Text != save.Text {
		t.Fatal(v)
	}
	save.Text = "/compact other"
	postQueueChange(t, peer, save, 409)
	resume := postQueueChange(t, f, commandQueueRequest{Action: "resume", Session: f.saved.ID()}, 200)
	if resume.TurnID == "" || f.end(t, resume.TurnID, "done").ReplyAgentCommand != "compact" {
		t.Fatal(resume)
	}
	h, err := f.s.ConversationHistory(f.saved.ID(), 0)
	if err != nil || h.Messages[len(h.Messages)-2].Content != "/pi /compact keep the decisions" {
		t.Fatal(h, err)
	}
}

func TestWebUIQueueMoveUsesWaitingAnchorsAndNeverRepeatsAnOldMove(t *testing.T) {
	f := newQueueFixture(t)
	for _, id := range []string{"first", "removed", "second", "third"} {
		postQueueChange(t, f, queuedText(f, id, id), 200)
	}
	if _, code, text := f.req("DELETE", commandQueueRequest{Session: f.saved.ID(), ID: "removed"}); code != 200 {
		t.Fatal(text)
	}
	hold := queueHold(f, "first", "editor", 0)
	postQueueChange(t, f, hold, 200)
	move := commandQueueRequest{Session: f.saved.ID(), Action: "move", ID: "third", Anchor: "first", Position: "before", Operation: "move-a"}
	postQueueChange(t, f, move, 200)
	later := move
	later.ID, later.Anchor, later.Operation = "second", "third", "move-b"
	postQueueChange(t, f, later, 200)
	v := postQueueChange(t, f, move, 200)
	var order []string
	for _, row := range v.Items {
		order = append(order, row.ID)
	}
	if !slices.Equal(order, []string{"second", "third", "first"}) || !v.Items[2].Editing {
		t.Fatal(v)
	}
	stale := move
	stale.Operation, stale.Anchor = "move-stale", "removed"
	postQueueChange(t, f, stale, 409)
	later.Position = "after"
	postQueueChange(t, f, later, 409)
	close(f.provider.release)
	first := f.end(t, "parent", "done").NextTurnID
	second := f.end(t, first, "done")
	if second.Reply != "opencode/chosen: second (high)" || second.NextTurnID == "" {
		t.Fatal(second)
	}
	third := f.end(t, second.NextTurnID, "done")
	if third.Reply != "opencode/chosen: third (high)" || third.NextTurnID != "" {
		t.Fatal(third)
	}
	hold.Action = "release"
	last := postQueueChange(t, f, hold, 200)
	if last.TurnID == "" || f.end(t, last.TurnID, "done").Reply != "opencode/chosen: first (high)" {
		t.Fatal(last)
	}
}

func TestWebUIQueueEditExpiryWakesAnIdleQueueAndDropsRestartedHolds(t *testing.T) {
	f := newQueueFixture(t)
	postQueueChange(t, f, queuedText(f, "item", "original"), 200)
	hold := queueHold(f, "item", "editor", 0)
	postQueueChange(t, f, hold, 200)
	close(f.provider.release)
	if done := f.end(t, "parent", "done"); done.NextTurnID != "" {
		t.Fatal(done)
	}
	hold.Action = "renew"
	postQueueChange(t, f, hold, 200)
	reg := f.s.webTurns
	reg.queueMu.Lock()
	l := reg.queueLeases[f.saved.ID()]
	editor := l.editors[hold.EditID]
	reg.queueMu.Unlock()
	// A callback that was queued before renewal must respect the new deadline.
	f.ch.expireQueueEdit(f.s, WorkspaceInfo{}, f.saved.ID(), l, hold.EditID, editor)
	if v, _, _ := f.req("GET", hold); !v.Receipt.Editing || v.TurnID != "" {
		t.Fatal(v)
	}
	reg.queueMu.Lock()
	editor.until = time.Now().Add(-time.Millisecond)
	editor.timer.Reset(0)
	reg.queueMu.Unlock()
	var child string
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		v, _, _ := f.req("GET", hold)
		child = v.Receipt.TurnID
		if child != "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if child == "" || f.end(t, child, "done").Reply != "opencode/chosen: original (high)" {
		t.Fatal(child)
	}
	postQueueChange(t, f, hold, 409)

	q := &commandQueue{Version: 1, Owner: "dead-owner", Entries: []queuedCommand{{ID: "abandoned", Kind: "message", State: "waiting", Editing: true, Text: "original"}}}
	if err := q.save(f.saved); err != nil {
		t.Fatal(err)
	}
	peer := NewService(ServiceOptions{WorkspaceRoot: f.s.workspaceRoot, ExternalBroker: f.s.externalBroker})
	ch := NewWebUIChannel(peer, true)
	life, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch.Start(life)
	w := httptest.NewRecorder()
	ch.handleCommandQueue(w, httptest.NewRequest("GET", "/api/v1/webui/command-queue?session_id="+f.saved.ID(), nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"paused":true`) || strings.Contains(w.Body.String(), `"editing":true`) {
		t.Fatal(w.Body.String())
	}
}
