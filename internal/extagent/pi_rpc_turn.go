package extagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"time"

	"bqagent/internal/turn"
)

// The RPC reader supplies frames in order. The turn loop owns submissions,
// activity, and the distinction between queued and consumed input.
type piRPCTurn struct {
	rpc                          piRPC
	hooks                        *TurnSteering
	sink                         ActivitySink
	output                       piOutput
	requests                     <-chan nativeInputRequest
	pending                      []*nativeInputRequest
	queue                        map[turn.InputKind][]string
	dequeued                     []*nativeInputRequest
	initialSeen, modern, working bool
	probeID                      string
	probeSeq                     uint64
	probeAfter                   <-chan time.Time
	probeTimeout                 <-chan time.Time
	settling, compacting         bool
	settledState                 *piState
}

func (r *piRPCTurn) run(ctx context.Context) (string, error) {
	ctx, closeInput := context.WithCancel(ctx)
	defer closeInput()
	defer r.releasePending()
	for {
		select {
		case err := <-r.rpc.dialogErrors():
			return "", err
		case <-ctx.Done():
			r.abort()
			return "", ctx.Err()
		case req := <-r.requests:
			if !r.working {
				req.result <- fmt.Errorf("%w: %w", turn.ErrInputClosed, turn.ErrInputRejected)
				continue
			}
			behavior := "steer"
			if req.input.Kind == turn.FollowUp {
				behavior = "followUp"
			}
			frame := map[string]any{"id": "input-" + req.input.ID, "type": "prompt", "streamingBehavior": behavior, "message": req.input.Text}
			addPiImages(frame, req.input.Images)
			err := r.rpc.write(frame)
			if err != nil {
				req.result <- err
				return "", err
			}
			r.pending = append(r.pending, &req)
		case <-r.probeAfter:
			if err := r.probeIdle(); err != nil {
				return "", err
			}
		case <-r.probeTimeout:
			return "", fmt.Errorf("Pi 未响应回合结束状态查询")
		case frame, ok := <-r.rpc.frames:
			if !ok {
				return "", fmt.Errorf("Pi 在确认回合结束前退出：%w", io.EOF)
			}
			if frame.err != nil {
				return "", frame.err
			}
			done, err := r.receive(ctx, frame.event)
			if err != nil {
				return "", err
			}
			if done {
				reply, _, err := r.output.final()
				return reply, err
			}
		}
	}
}

func (r *piRPCTurn) receive(ctx context.Context, e piEvent) (bool, error) {
	if e.Type == "response" {
		return r.response(e)
	}
	if e.Type == "extension_ui_request" {
		return false, r.rpc.dialog(e)
	}
	if r.rpc.usage != nil {
		r.rpc.usage.observe(e)
	}
	echo, err := r.output.observe(e, r.sink)
	if err != nil {
		return false, err
	}
	switch e.Type {
	case "queue_update":
		r.updateQueue(turn.Steering, e.Steering)
		r.updateQueue(turn.FollowUp, e.FollowUp)
	case "agent_start", "turn_start", "message_start", "message_update", "message_end", "tool_execution_start", "tool_execution_update", "tool_execution_end":
		// Any new native activity invalidates an earlier idle probe.
		r.working, r.settling = true, false
		r.invalidateProbe()
	case "auto_retry_start":
		r.working, r.settling = false, false
		r.invalidateProbe()
	case "compaction_start", "auto_compaction_start":
		r.compacting = true
		r.invalidateProbe()
	case "compaction_end", "auto_compaction_end":
		r.compacting = false
		if e.WillRetry {
			r.settling = false
		}
		if r.settling {
			return false, r.probeIdle()
		}
	case "agent_end":
		r.working = false
		// Pi versions that report prompt disposition also report agent_settled
		// after retries/compaction. Older versions finish with agent_end.
		if !r.modern {
			r.settling = true
			if !r.compacting {
				return false, r.probeIdle()
			}
		}
	case "agent_settled":
		r.working, r.settling = false, true
		if !r.compacting {
			return false, r.probeIdle()
		}
	}
	if echo != nil {
		if !r.initialSeen {
			r.initialSeen = true
			r.requests = nativeInputRequests(ctx, r.hooks, turn.Steering, turn.FollowUp)
		} else {
			r.consume(*echo)
		}
	}
	return false, nil
}

func (r *piRPCTurn) probeIdle() error {
	r.probeAfter = nil
	r.probeSeq++
	r.probeID = fmt.Sprintf("idle-%d", r.probeSeq)
	r.probeTimeout = time.After(12 * time.Second)
	return r.rpc.write(map[string]any{"id": r.probeID, "type": "get_state"})
}

func (r *piRPCTurn) invalidateProbe() {
	r.probeID, r.probeAfter, r.probeTimeout = "", nil, nil
}

func (r *piRPCTurn) response(e piEvent) (bool, error) {
	switch {
	case e.Command == "get_state" && r.probeID != "" && e.ID.text == r.probeID:
		r.invalidateProbe()
		if !e.Success {
			return false, piRPCError(e)
		}
		var state piState
		if json.Unmarshal(e.Data, &state) != nil || state.SessionID != r.output.sessionID {
			return false, fmt.Errorf("Pi 未确认当前会话的结束状态")
		}
		if state.idle() {
			r.settledState = &state
			return true, nil
		}
		r.probeAfter = time.After(150 * time.Millisecond)
	case e.Command == "prompt" && e.ID.text == "initial":
		if !e.Success {
			return false, piRPCError(e)
		}
		var data struct {
			Disposition  string
			AgentInvoked *bool
		}
		_ = json.Unmarshal(e.Data, &data)
		r.modern = data.Disposition != ""
		if data.Disposition == "handled" || data.AgentInvoked != nil && !*data.AgentInvoked {
			r.settling = true
			r.output.reply = "Pi 已处理该命令。"
			return false, r.probeIdle()
		}
	case e.Command == "prompt":
		for i, req := range r.pending {
			if e.ID.text != "input-"+req.input.ID {
				continue
			}
			if req.result == nil {
				break
			}
			if !e.Success {
				req.result <- fmt.Errorf("%w: %w", turn.ErrInputRejected, piRPCError(e))
				r.removeInput(i)
			} else {
				req.result <- nil
				req.result = nil // Pi accepted it; wait for the user echo.
				var data struct {
					Disposition  string
					AgentInvoked *bool
				}
				_ = json.Unmarshal(e.Data, &data)
				if data.Disposition == "handled" || data.AgentInvoked != nil && !*data.AgentInvoked {
					r.hooks.Applied(req.input)
					r.removeInput(i)
				}
			}
			break
		}
	}
	return false, nil
}

func (r *piRPCTurn) updateQueue(kind turn.InputKind, current []string) {
	if r.queue == nil {
		r.queue = make(map[turn.InputKind][]string)
	}
	remaining := make(map[string]int, len(current))
	for _, text := range current {
		remaining[text]++
	}
	for _, text := range r.queue[kind] {
		if remaining[text] > 0 {
			remaining[text]--
			continue
		}
		for _, req := range r.pending {
			if req.input.Kind == kind && req.input.Text == text && !slices.Contains(r.dequeued, req) {
				r.dequeued = append(r.dequeued, req)
				break
			}
		}
	}
	r.queue[kind] = current
}

func (r *piRPCTurn) removeInput(i int) {
	if j := slices.Index(r.dequeued, r.pending[i]); j >= 0 {
		r.dequeued = slices.Delete(r.dequeued, j, j+1)
	}
	r.pending = slices.Delete(r.pending, i, i+1)
}

func (r *piRPCTurn) consume(text string) {
	// Echoes do not carry request IDs. Prefer the item Pi removed from its
	// native queue. Legacy providers omit queue_update and drain steering first.
	for _, req := range r.dequeued {
		if req.input.Text == text {
			r.applyInput(slices.Index(r.pending, req))
			return
		}
	}
	for _, kind := range []turn.InputKind{turn.Steering, turn.FollowUp} {
		for i, req := range r.pending {
			if req.input.Kind != kind || req.input.Text != text {
				continue
			}
			r.applyInput(i)
			return
		}
	}
}

func (r *piRPCTurn) applyInput(i int) {
	req := r.pending[i]
	if req.result != nil {
		req.result <- nil
	}
	r.hooks.Applied(req.input)
	r.output.reply, r.output.failure = "", ""
	r.removeInput(i)
}

func (r *piRPCTurn) releasePending() {
	var unconsumed []turn.Input
	for _, req := range r.pending {
		if req.result == nil {
			unconsumed = append(unconsumed, req.input)
		} else {
			req.result <- turn.ErrInputClosed
		}
	}
	if len(unconsumed) > 0 && r.hooks.Unconsumed != nil {
		r.hooks.Unconsumed(unconsumed)
	}
}

func (r *piRPCTurn) abort() {
	if r.rpc.write(map[string]any{"type": "abort", "id": "stop"}) != nil {
		return
	}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		select {
		case <-deadline.C:
			return
		case frame, ok := <-r.rpc.frames:
			if !ok || frame.err != nil {
				return
			}
			e := frame.event
			if e.Type == "queue_update" {
				r.updateQueue(turn.Steering, e.Steering)
				r.updateQueue(turn.FollowUp, e.FollowUp)
			}
			if e.Type == "response" {
				if e.Command == "abort" && e.ID.text == "stop" {
					return
				}
				if e.Command == "prompt" {
					_, _ = r.response(e)
				}
				continue
			}
			echo, _ := r.output.observe(e, r.sink)
			if r.initialSeen && echo != nil {
				r.consume(*echo)
			}
			if e.Type == "agent_settled" {
				return
			}
		}
	}
}
