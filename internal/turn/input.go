package turn

import (
	"context"
	"errors"
	"sync"
)

// InputKind distinguishes corrections to ongoing work from work to do afterwards.
type InputKind string

const (
	Steering InputKind = "steering"
	FollowUp InputKind = "follow_up"
)

var ErrInputClosed = errors.New("turn no longer accepts input")

type Input struct {
	ID      string    `json:"id"`
	Kind    InputKind `json:"kind"`
	Text    string    `json:"text"`
	Content any       `json:"-"`
}

// InputQueue serializes input acceptance with natural turn completion. Accepted
// but unconsumed inputs remain available after cancellation for draft recovery.
type InputQueue struct {
	mu     sync.Mutex
	items  []Input
	closed bool
}

func (q *InputQueue) Add(input Input) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return ErrInputClosed
	}
	if input.Kind != Steering && input.Kind != FollowUp {
		return errors.New("invalid input kind")
	}
	if input.Text == "" {
		return errors.New("input text is required")
	}
	q.items = append(q.items, input)
	return nil
}

func (q *InputQueue) take(kind InputKind) []Input {
	var selected, remaining []Input
	for _, input := range q.items {
		if input.Kind == kind {
			selected = append(selected, input)
		} else {
			remaining = append(remaining, input)
		}
	}
	q.items = remaining
	return selected
}

func (q *InputQueue) TakeSteering() []Input {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.take(Steering)
}

// Finish takes corrections first, then one follow-up. If neither exists it
// closes acceptance under the same lock, preventing a lost final input.
func (q *InputQueue) Finish() []Input {
	q.mu.Lock()
	defer q.mu.Unlock()
	if inputs := q.take(Steering); len(inputs) > 0 {
		return inputs
	}
	if len(q.items) > 0 {
		input := q.items[0]
		q.items = q.items[1:]
		return []Input{input}
	}
	q.closed = true
	return nil
}

// Restore returns inputs that could not be persisted to the front of the queue.
// It also works after Close so error recovery can return them to the caller.
func (q *InputQueue) Restore(inputs []Input) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = append(append([]Input(nil), inputs...), q.items...)
}

func (q *InputQueue) Close() []Input {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	return append([]Input(nil), q.items...)
}

type inputQueueKey struct{}

func WithInputQueue(ctx context.Context, queue *InputQueue) context.Context {
	return context.WithValue(ctx, inputQueueKey{}, queue)
}

func Inputs(ctx context.Context) *InputQueue {
	queue, _ := ctx.Value(inputQueueKey{}).(*InputQueue)
	return queue
}
