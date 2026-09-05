package extagent

import (
	"testing"
	"time"
)

func TestDetectionSnapshotDoesNotWaitOrExposeCommands(t *testing.T) {
	broker := newBroker(nil, nil)
	done := make(chan bool, 1)
	go func() { _, complete := broker.DetectionSnapshot(); done <- complete }()
	select {
	case complete := <-done:
		if complete {
			t.Fatal("premature completion")
		}
	case <-time.After(time.Second):
		t.Fatal("snapshot blocked")
	}
	broker.publishDetections(map[AgentName]DetectionResult{AgentClaude: {Agent: AgentClaude, Preferred: &AgentTransport{Kind: TransportCLI, Command: CommandSpec{Command: "secret-command"}}, CLIFallback: true, StartupError: "secret-error"}})
	statuses, complete := broker.DetectionSnapshot()
	if !complete || len(statuses) == 0 || !statuses[0].Available || !statuses[0].Fallback || statuses[0].CheckedAt.IsZero() {
		t.Fatalf("%+v", statuses)
	}
}
