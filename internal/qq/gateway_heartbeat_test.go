package qq

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSendHeartbeatsTearsDownAfterConsecutiveFailures(t *testing.T) {
	teardown := make(chan struct{})
	done := make(chan struct{})
	writes := 0
	go func() {
		defer close(done)
		sendHeartbeats(context.Background(), time.Millisecond, func() error {
			writes++
			return errors.New("write failed")
		}, func() {
			close(teardown)
		})
	}()

	select {
	case <-teardown:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for heartbeat teardown")
	}
	<-done
	if writes != maxHeartbeatFailures {
		t.Fatalf("writes = %d, want %d", writes, maxHeartbeatFailures)
	}
}

func TestSendHeartbeatsRecoversFromIntermittentFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	teardownCalled := make(chan struct{}, 1)
	done := make(chan struct{})
	writes := 0
	go func() {
		defer close(done)
		sendHeartbeats(ctx, time.Millisecond, func() error {
			writes++
			if writes%2 == 1 {
				return errors.New("write failed")
			}
			if writes >= 10 {
				cancel()
			}
			return nil
		}, func() {
			teardownCalled <- struct{}{}
		})
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for heartbeat loop to stop")
	}
	select {
	case <-teardownCalled:
		t.Fatal("teardown called despite failures never being consecutive")
	default:
	}
}
