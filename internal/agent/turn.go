package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

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

	// ledger holds this turn's real tool executions for completion evidence.
	ledger execLedger

	mu              sync.Mutex
	latestUser      string
	explicitScope   bool
	inputClock      uint64 // ledger time of the latest user input
	taskUpdated     bool
	completionSaved bool
	completedClock  uint64
	declaration     *outcomeDeclaration // current request only; never restored from task history
}

// acceptUserInput must be called for every user message that joins the turn
// (the initial request and any steering/follow-up). It grounds goal-change
// quotes in the latest user message and invalidates older verification evidence.
func (s *runState) acceptUserInput(text string) {
	if s == nil || strings.TrimSpace(text) == "" {
		return
	}
	s.mu.Lock()
	s.latestUser = text
	s.taskUpdated = false
	s.declaration = nil
	s.mu.Unlock()
	if !s.explicitScope {
		// A model-inferred side_question applied to the previous request only.
		// An explicit caller-supplied relation stays fixed.
		s.outcome.TaskRelation, s.outcome.TaskRelationReason = turn.ContinueTask, ""
		s.scopeLocked = false
	}
	baseline := s.ledger.barrier()
	s.mu.Lock()
	s.inputClock = baseline
	s.mu.Unlock()
}

func (s *runState) quotedByUser(quote string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return quoteGrounded(quote, s.latestUser)
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
	state.explicitScope = a.taskRelation != ""
	if state.id == "" {
		state.id = turn.NewID()
	}
	if state.outcome.TaskRelation == "" {
		state.outcome.TaskRelation = turn.ContinueTask
	}
	if !state.outcome.TaskRelation.Valid() {
		return TurnResult{}, fmt.Errorf("invalid task relation %q", a.taskRelation)
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i]["role"] == "user" {
			state.latestUser = contentText(messages[i]["content"])
			break
		}
	}
	ctx = context.WithValue(ctx, runStateKey{}, state)
	metricsCtx, collector := withTurnGenerationCollector(ctx)
	reply, history, err := a.runConversation(metricsCtx, duplicateMessages(messages), maxIterations)
	a.reopenStaleCompletion(state)
	if err != nil {
		status := turn.Failed
		if errors.Is(err, context.Canceled) {
			status = turn.Interrupted
		}
		state.outcome.Set(status, err.Error())
	}
	state.applyDeclaredOutcome()
	// Historical progress snapshots do not determine the current turn result.
	if a.task != nil {
		state.outcome.TaskStatus = a.task.Status
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

// hasMutationSinceInput ignores workspace changes made for earlier user input,
// so a new independent question is not blocked by previous work in the run.
func (s *runState) hasMutationSinceInput() bool {
	s.mu.Lock()
	since := s.inputClock
	s.mu.Unlock()
	return s.ledger.hasMutation(since)
}
