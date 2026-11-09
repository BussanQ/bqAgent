package extagent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestPiDialogReturnsNumericIDOnTheOriginalWireType(t *testing.T) {
	var event piEvent
	if err := json.Unmarshal([]byte(`{"type":"extension_ui_request","method":"select","id":0,"options":["One"]}`), &event); err != nil {
		t.Fatal(err)
	}
	w := newPiTestWire()
	rpc := w.rpc()
	if err := rpc.dialog(event); err != nil {
		t.Fatal(err)
	}
	reply := w.next(t, "extension_ui_response")
	if reply["id"] != json.Number("0") || reply["cancelled"] != true {
		t.Fatal(reply)
	}
}

func TestPiDialogsAcceptOnlyOfferedRepliesAndPreserveEditorText(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewBroker(NewStateStore(t.TempDir()), nil, nil)
	defer b.Close()
	sink := &waitingAppServerPermissionSink{requests: make(chan ACPPermissionRequest, 12)}
	w := newPiTestWire()
	rpc := w.rpc()
	rpc.sessionID = "native"
	rpc.dialogs = &piDialogs{ctx: ctx, failed: make(chan error, 1), ask: func(c context.Context, p acpPermissionParams) acpPermissionOutcome {
		return b.awaitPermission(c, "web-session", AgentPi, p, sink, 0)
	}}
	get := func() ACPPermissionRequest {
		t.Helper()
		select {
		case p := <-sink.requests:
			return p
		case <-time.After(2 * time.Second):
			t.Fatal("no dialog")
			return ACPPermissionRequest{}
		}
	}
	for _, tc := range []struct {
		method, choice, key string
		value               any
	}{
		{"select", "choice-1", "value", "<option B>"}, {"confirm", "no", "confirmed", false}, {"editor", "submit", "value", "  original\n第二行\n"}, {"input", "submit", "value", ""}, {"confirm", "cancel", "cancelled", true},
	} {
		e := piEvent{Type: "extension_ui_request", ID: piRequestID{text: "q"}, Title: "title", Method: tc.method, Options: json.RawMessage(`["A","<option B>"]`), Prefill: "initial"}
		if err := rpc.dialog(e); err != nil {
			t.Fatal(err)
		}
		request := get()
		if !b.HasPendingPrompt("web-session") || b.HasPendingPrompt("other") {
			t.Fatal("prompt must gate only its owning session")
		}
		if request.Dialog.Method != tc.method || request.BQSessionID != "web-session" || request.ExternalSessionID != "native" {
			t.Fatal(request)
		}
		if err := b.RespondPermission(request.RequestID, "invented"); err == nil {
			t.Fatal("accepted unoffered choice")
		}
		var text *string
		if tc.choice == "submit" {
			if err := b.RespondPrompt(request.RequestID, "submit", nil); err == nil {
				t.Fatal("missing explicit text allowed")
			}
			s := tc.value.(string)
			text = &s
		}
		if err := b.RespondPrompt(request.RequestID, tc.choice, text); err != nil {
			t.Fatal(err)
		}
		reply := w.next(t, "extension_ui_response")
		if reply["id"] != "q" || reply[tc.key] != tc.value {
			t.Fatalf("unexpected dialog response: %+v", reply)
		}
		rpc.dialogs.wait.Wait()
		if err := b.RespondPrompt(request.RequestID, tc.choice, text); !errors.Is(err, ErrPermissionNotFound) {
			t.Fatal(err)
		}
	}
}

func TestPiDialogExpiryWithdrawsPromptAndCancelsNativeCallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewBroker(NewStateStore(t.TempDir()), nil, nil)
	defer b.Close()
	sink := &waitingAppServerPermissionSink{requests: make(chan ACPPermissionRequest, 2)}
	w := newPiTestWire()
	rpc := w.rpc()
	var endDialog context.CancelFunc
	var lock sync.Mutex
	rpc.dialogs = &piDialogs{ctx: ctx, failed: make(chan error, 1), ask: func(c context.Context, p acpPermissionParams) acpPermissionOutcome {
		c, stop := context.WithCancel(c)
		lock.Lock()
		endDialog = stop
		lock.Unlock()
		defer stop()
		return b.awaitPermission(c, "s", AgentPi, p, sink, 0)
	}}
	if err := rpc.dialog(piEvent{ID: piRequestID{text: "expired"}, Method: "confirm", Title: "continue?"}); err != nil {
		t.Fatal(err)
	}
	first := <-sink.requests
	lock.Lock()
	endDialog()
	lock.Unlock()
	expired := <-sink.requests
	if expired.RequestID != first.RequestID || !expired.Expired {
		t.Fatal(expired)
	}
	reply := w.next(t, "extension_ui_response")
	if reply["cancelled"] != true {
		t.Fatal(reply)
	}
	rpc.dialogs.wait.Wait()
	if err := b.RespondPermission(first.RequestID, "yes"); !errors.Is(err, ErrPermissionNotFound) {
		t.Fatal(err)
	}
}

type permissionReplaySink struct {
	start chan ACPPermissionRequest
	end   chan ACPPermissionRequest
}

func (s permissionReplaySink) EmitACPPermissionRequest(r ACPPermissionRequest)    { s.start <- r }
func (s permissionReplaySink) EmitACPPermissionResolution(r ACPPermissionRequest) { s.end <- r }

func TestReplayedPermissionEndsWithActualChoiceOrExpiry(t *testing.T) {
	b := NewBroker(NewStateStore(t.TempDir()), nil, nil)
	defer b.Close()
	for _, respond := range []bool{true, false} {
		sink := permissionReplaySink{make(chan ACPPermissionRequest, 1), make(chan ACPPermissionRequest, 1)}
		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan acpPermissionOutcome, 1)
		go func() {
			finished <- b.awaitPermission(ctx, "chat", AgentCodex, acpPermissionParams{SessionID: "native", Options: []ACPPermissionOption{{OptionID: "no", Name: "拒绝"}}}, sink, 0)
		}()
		start := <-sink.start
		if respond {
			if err := b.RespondPermission(start.RequestID, "no"); err != nil {
				t.Fatal(err)
			}
		} else {
			cancel()
		}
		outcome := <-finished
		end := <-sink.end
		cancel()
		if end.RequestID != start.RequestID || end.Expired == respond || end.Selected != outcome.OptionID || (respond && end.Selected != "no") {
			t.Fatalf("request=%+v resolution=%+v", start, end)
		}
	}
}
