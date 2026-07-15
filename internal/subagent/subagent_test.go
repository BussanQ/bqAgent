package subagent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"bqagent/internal/extagent"
)

type fakeACP struct{}

func (fakeACP) Initialize(context.Context) error                            { return nil }
func (fakeACP) LoadSessionSupported() bool                                  { return true }
func (fakeACP) NewSession(context.Context, string) (string, error)          { return "external-1", nil }
func (fakeACP) LoadSession(context.Context, string, string) (string, error) { return "external-1", nil }
func (fakeACP) Prompt(context.Context, string, string) (string, error)      { return "subagent result", nil }
func (fakeACP) Close() error                                                { return nil }

func TestManagerSpawnsPersistedWorktreeTask(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "README.md")
	runGit(t, root, "commit", "-m", "init")
	spec := extagent.CommandSpec{Command: "fake"}
	broker := extagent.NewBroker(extagent.NewStateStore(root), map[extagent.AgentName]extagent.DetectionResult{extagent.AgentClaude: {Agent: extagent.AgentClaude, Preferred: &extagent.AgentTransport{Agent: extagent.AgentClaude, Kind: extagent.TransportACP, Command: spec}}}, func(extagent.CommandSpec, string) (extagent.ACPClient, error) { return fakeACP{}, nil })
	defer broker.Close()
	manager := NewManager(root, broker, false)
	task, err := manager.Spawn(SpawnOptions{ParentSessionID: "session-1", Agent: extagent.AgentClaude, Prompt: "inspect", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done, err := manager.Wait(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != StatusCompleted || done.Result != "subagent result" || done.WorktreePath == "" {
		t.Fatalf("task=%+v", done)
	}
	if _, err := os.Stat(filepath.Join(root, ".agent", "subagents", task.ID, "meta.json")); err != nil {
		t.Fatal(err)
	}
}
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
