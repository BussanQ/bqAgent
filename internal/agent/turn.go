package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"bqagent/internal/turn"
)

type TurnResult struct {
	TurnID     string
	Reply      string
	Outcome    turn.Outcome
	Messages   []map[string]any
	Generation TurnGenerationMetrics
}

type runStateKey struct{}
type runState struct {
	id          string
	sink        turn.Sink
	seq         uint64
	items       int
	finalItem   string
	outcome     turn.Outcome
	scopeLocked bool
}

func turnRun(ctx context.Context) *runState {
	state, _ := ctx.Value(runStateKey{}).(*runState)
	return state
}

func (s *runState) newItem() string {
	if s == nil {
		return ""
	}
	s.items++
	return fmt.Sprintf("%s/message-%d", s.id, s.items)
}

func (s *runState) emit(id, kind, phase, text string) {
	if s == nil || s.sink == nil {
		return
	}
	s.seq++
	s.sink(turn.Event{TurnID: s.id, ItemID: id, Seq: s.seq, Kind: kind, Phase: phase, Text: text})
}

func setTurnStop(ctx context.Context, status turn.Status, reason string) {
	if state := turnRun(ctx); state != nil {
		state.outcome.Set(status, reason)
	}
}

// RunTurn is the authoritative result consumed by all channels. Legacy entry
// points below retain their signatures for non-interactive callers.
func (a *Agent) RunTurn(ctx context.Context, messages []map[string]any, maxIterations int) (TurnResult, error) {
	state := &runState{id: a.turnID, sink: a.messageSink, outcome: turn.Outcome{Status: turn.Completed, TaskRelation: a.taskRelation}}
	state.scopeLocked = a.taskRelation != ""
	if state.id == "" {
		state.id = turn.NewID()
	}
	if state.outcome.TaskRelation == "" {
		state.outcome.TaskRelation = turn.ContinueTask
	}
	if !state.outcome.TaskRelation.Valid() {
		return TurnResult{}, fmt.Errorf("invalid task relation %q", a.taskRelation)
	}
	ctx = context.WithValue(ctx, runStateKey{}, state)
	metricsCtx, collector := withTurnGenerationCollector(ctx)
	reply, history, err := a.runConversation(metricsCtx, duplicateMessages(messages), maxIterations)
	if err != nil {
		status := turn.Failed
		if errors.Is(err, context.Canceled) {
			status = turn.Interrupted
		}
		state.outcome.Set(status, err.Error())
	}
	if a.task != nil {
		state.outcome.TaskStatus = a.task.Status
		if err == nil && state.outcome.Status == turn.Completed && state.outcome.TaskRelation != turn.SideQuestion {
			switch a.task.Status {
			case "blocked":
				state.outcome.Set(turn.Blocked, a.task.Reason)
			case "awaiting_input":
				state.outcome.Set(turn.AwaitingInput, a.task.Reason)
			case "cancelled":
				state.outcome.Set(turn.Interrupted, a.task.Reason)
			}
		}
	}
	if err == nil && strings.TrimSpace(reply) != "" {
		if state.finalItem == "" {
			state.finalItem = state.newItem()
		}
		state.emit(state.finalItem, "message.completed", "final_answer", reply)
	}
	generation := TurnGenerationMetrics{}
	if err == nil {
		generation = collector.metricsFor(reply)
	}
	return TurnResult{TurnID: state.id, Reply: reply, Outcome: state.outcome, Messages: history, Generation: generation}, err
}
