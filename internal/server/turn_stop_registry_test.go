package server

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestServiceStopTurnCancelsChannelIndependentTurn(t *testing.T) {
	root := t.TempDir()
	client := &cancelAwareTurnClient{started: make(chan struct{}), canceled: make(chan struct{})}
	service := newTestService(root, "http://example.invalid")
	service.client = client

	errCh := make(chan error, 1)
	go func() {
		_, err := service.HandleTurnWithOptions(context.Background(), TurnRequest{
			Message: "wait",
			TurnID:  "generic-turn-1",
		}, TurnOptions{Stream: true})
		errCh <- err
	}()

	select {
	case <-client.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for turn to start")
	}
	if !service.StopTurn("generic-turn-1") {
		t.Fatal("StopTurn reported no active turn")
	}
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("turn error = %v, want context canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for canceled turn")
	}
	if service.StopTurn("generic-turn-1") {
		t.Fatal("completed turn still reported as active")
	}
}
