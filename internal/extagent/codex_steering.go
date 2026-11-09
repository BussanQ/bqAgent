package extagent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"bqagent/internal/media"
	"bqagent/internal/turn"
)

func (c *codexAppServerClient) steer(run *appServerRun, input turn.Input, sink ActivitySink) error {
	c.mu.Lock()
	progress := run.turns[run.turnID]
	closed := run.ctx.Err() != nil || progress != nil && progress.completed != nil
	var earlier []Activity
	if !closed && progress != nil {
		earlier, progress.activity = progress.activity, nil
	}
	c.mu.Unlock()
	if closed {
		return fmt.Errorf("%w: %w", turn.ErrInputClosed, turn.ErrInputRejected)
	}
	if sink != nil {
		for _, event := range earlier {
			sink(event)
		}
	}
	ctx, cancel := context.WithTimeout(run.ctx, 15*time.Second)
	defer cancel()
	parts := codexUserInput(input.Text, input.Images)
	if skill := c.pickedSkill(ctx, input.Text); skill != nil {
		parts = append(parts, skill)
	}
	reply, err := c.request(ctx, "turn/steer", map[string]any{
		"threadId": run.threadID, "expectedTurnId": run.turnID, "clientUserMessageId": input.ID,
		"input": parts,
	})
	if err != nil {
		if reply.Error != nil {
			return fmt.Errorf("%w: %v", turn.ErrInputRejected, err)
		}
		return fmt.Errorf("Codex 纠偏未获确认：%w", err)
	}
	var ack struct{ TurnID string }
	if json.Unmarshal(reply.Result, &ack) != nil || ack.TurnID != run.turnID {
		return fmt.Errorf("Codex 未确认当前回合的纠偏")
	}
	return nil
}

func codexUserInput(prompt string, images []media.Image) []map[string]any {
	input := make([]map[string]any, 0, len(images)+1)
	if prompt != "" || len(images) == 0 {
		input = append(input, map[string]any{"type": "text", "text": prompt, "text_elements": []any{}})
	}
	for _, image := range images {
		input = append(input, map[string]any{"type": "image", "url": image.DataURL()})
	}
	return input
}
