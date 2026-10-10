package session

import (
	"os"
	"path/filepath"
	"testing"

	"bqagent/internal/taskstate"
)

func TestSessionTaskStatePersistsIndependentlyOfTranscript(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	saved, err := store.Create(CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	state, err := saved.LoadTaskState()
	if err != nil || state != nil {
		t.Fatalf("new: %v %v", state, err)
	}
	task := taskstate.State{Goal: "implement fast", Status: "active", Criteria: []taskstate.Criterion{{Description: "switch works"}}, Remaining: []string{"tests"}}
	if err = saved.SaveTaskState(task); err != nil {
		t.Fatal(err)
	}
	if err = saved.SaveWorkingContext([]map[string]any{{"role": "user", "content": "继续"}}); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore(root).Open(saved.ID())
	if err != nil {
		t.Fatal(err)
	}
	state, err = reopened.LoadTaskState()
	if err != nil || state.Goal != task.Goal || len(state.Remaining) != 1 || state.UpdatedAt.IsZero() {
		t.Fatalf("resume: %+v %v", state, err)
	}
	invalid := task
	invalid.Status = "completed"
	if err = reopened.SaveTaskState(invalid); err == nil {
		t.Fatal("unfinished completion persisted")
	}
	state, _ = reopened.LoadTaskState()
	if state.Status != "active" {
		t.Fatal("failed write changed state")
	}
	if err = os.WriteFile(filepath.Join(reopened.dir, "task.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.LoadTaskState(); err == nil {
		t.Fatal("corrupt task silently discarded")
	}
}
