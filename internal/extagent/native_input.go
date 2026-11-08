package extagent

import (
	"context"
	"fmt"
	"slices"

	"bqagent/internal/turn"
)

type nativeInputRequest struct {
	input  turn.Input
	result chan error
}

func nativeInputRequests(ctx context.Context, hooks *TurnSteering, kinds ...turn.InputKind) <-chan nativeInputRequest {
	if hooks == nil || hooks.Ready == nil || hooks.Applied == nil {
		return nil
	}
	requests := make(chan nativeInputRequest)
	hooks.Ready(func(input turn.Input) error {
		if !slices.Contains(kinds, input.Kind) {
			return fmt.Errorf("当前回合不支持此类输入")
		}
		req := nativeInputRequest{input: input, result: make(chan error, 1)}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %w", turn.ErrInputClosed, turn.ErrInputRejected)
		case requests <- req:
			// After receiving the request the native prompt owns its response,
			// including cleanup on cancellation and natural completion.
			return <-req.result
		}
	}, kinds...)
	return requests
}
