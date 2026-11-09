package extagent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"time"
)

type piDialogHandler func(context.Context, acpPermissionParams) acpPermissionOutcome

type piDialogs struct {
	ctx    context.Context
	ask    piDialogHandler
	mu     sync.Mutex
	open   map[piRequestID]bool
	wait   sync.WaitGroup
	failed chan error
}

func (rpc piRPC) dialogErrors() <-chan error {
	if rpc.dialogs == nil {
		return nil
	}
	return rpc.dialogs.failed
}

func (rpc piRPC) dialog(e piEvent) error {
	dialog := &AgentDialog{Method: e.Method, Title: e.Title, Placeholder: e.Placeholder, Prefill: e.Prefill}
	_ = json.Unmarshal(e.Message, &dialog.Message)
	params := acpPermissionParams{SessionID: rpc.sessionID, Dialog: dialog}
	var choices []string
	switch e.Method {
	case "select":
		if err := json.Unmarshal(e.Options, &choices); err != nil {
			return fmt.Errorf("invalid Pi select choices: %w", err)
		}
		for i, label := range choices {
			params.Options = append(params.Options, ACPPermissionOption{OptionID: fmt.Sprintf("choice-%d", i), Name: label})
		}
	case "confirm":
		params.Options = []ACPPermissionOption{{OptionID: "yes", Name: "确认"}, {OptionID: "no", Name: "否"}}
	case "input", "editor":
		params.Options = []ACPPermissionOption{{OptionID: "submit", Name: "提交"}}
	default:
		return nil // notify and status events have no response.
	}
	if e.ID == (piRequestID{}) {
		return fmt.Errorf("Pi dialog has no request ID")
	}
	params.Options = append(params.Options, ACPPermissionOption{OptionID: "cancel", Name: "取消", Kind: "reject_once"})
	reply := map[string]any{"type": "extension_ui_response", "id": e.ID.value()}
	d := rpc.dialogs
	if d == nil || d.ask == nil {
		reply["cancelled"] = true
		return rpc.write(reply)
	}
	d.mu.Lock()
	if d.open[e.ID] {
		d.mu.Unlock()
		return nil
	}
	if d.open == nil {
		d.open = make(map[piRequestID]bool)
	}
	d.open[e.ID] = true
	d.mu.Unlock()
	d.wait.Add(1)
	go func() {
		defer d.wait.Done()
		defer func() { d.mu.Lock(); delete(d.open, e.ID); d.mu.Unlock() }()
		ctx := d.ctx
		if e.Timeout > 0 && e.Timeout < float64(math.MaxInt64)/float64(time.Millisecond) {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, time.Duration(e.Timeout*float64(time.Millisecond)))
			defer cancel()
		}
		answer := d.ask(ctx, params)
		if d.ctx.Err() != nil {
			return
		}
		switch {
		case ctx.Err() != nil || answer.Outcome != "selected" || answer.OptionID == "cancel":
			reply["cancelled"] = true
		case e.Method == "confirm":
			reply["confirmed"] = answer.OptionID == "yes"
		case e.Method == "select":
			reply["cancelled"] = true
			for i, choice := range choices {
				if params.Options[i].OptionID == answer.OptionID {
					delete(reply, "cancelled")
					reply["value"] = choice
					break
				}
			}
		case answer.Value != nil:
			reply["value"] = *answer.Value
		default:
			reply["cancelled"] = true
		}
		if err := rpc.write(reply); err != nil {
			select {
			case d.failed <- err:
			default:
			}
		}
	}()
	return nil
}
