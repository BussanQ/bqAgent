package agent

import (
	"context"
	"fmt"

	"bqagent/internal/turn"
)

// queuedBeforeReplyNote applies when inputs join after a reply: the user
// queued them before that reply finished, so they cannot answer its questions.
const queuedBeforeReplyNote = " These inputs were queued before your last reply finished. They do not answer questions in that reply or grant any confirmation or authorization it requested; wait for an explicit later user reply before doing anything that still needs confirmation."

// appendTurnInputs adds queued user inputs to the turn. afterReply marks
// inputs merged right after an assistant reply rather than after tool results.
func (a *Agent) appendTurnInputs(ctx context.Context, messages []map[string]any, inputs []turn.Input, afterReply bool) ([]map[string]any, error) {
	for i, input := range inputs {
		content := input.Content
		if content == nil {
			content = input.Text
		}
		message := map[string]any{"role": "user", "content": content}
		if err := a.recordMessages(message); err != nil {
			if queue := turn.Inputs(ctx); queue != nil {
				queue.Restore(inputs[i:])
			}
			return messages, err
		}
		messages = append(messages, message)
		if state := turnRun(ctx); state != nil {
			state.acceptUserInput(input.Text)
			state.emit(input.ID, "input.applied", string(input.Kind), input.Text)
		}
	}
	if len(inputs) > 0 {
		instruction := "The user supplied new input during this run. Apply corrections to the ongoing objective now, preserve earlier requirements unless explicitly changed, and continue unfinished work. A follow_up starts subsequent work only after the preceding request has finished."
		if afterReply {
			instruction += queuedBeforeReplyNote
		}
		var err error
		messages, err = a.appendContinuation(messages, instruction)
		if err != nil {
			return messages, fmt.Errorf("record input instructions: %w", err)
		}
	}
	return messages, nil
}
