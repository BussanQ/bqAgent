package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"bqagent/internal/taskstate"
)

func TestContextBoundaryPublishesPreparedFilesAndReceiptTogether(t *testing.T) {
	store := NewStore(t.TempDir())
	s, err := store.Create(CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordMessage(map[string]any{"role": "user", "content": "original"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTaskState(taskstate.State{Goal: "old goal", Status: "active", Criteria: []taskstate.Criterion{{Description: "old task"}}}); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("failed to stage provider")
	stage := func(next *Session) error {
		before, err := store.Open(s.ID())
		if err != nil || before.ContextNumber() != 0 {
			t.Fatalf("partially visible: %v %v", before, err)
		}
		return next.RecordMessage(map[string]any{"role": "user", "content": "fresh"})
	}
	if err = s.StartContext("one", "pi", func(next *Session) error {
		if err := stage(next); err != nil {
			return err
		}
		return failure
	}); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if files, err := os.ReadDir(filepath.Join(s.Dir(), ".contexts")); err != nil || len(files) != 0 {
		t.Fatal(files, err)
	}
	if err = s.StartContext("one", "pi", stage); err != nil {
		t.Fatal(err)
	}
	if err = s.StartContext("one", "pi", func(*Session) error { t.Fatal("retried operation prepared twice"); return nil }); err != nil {
		t.Fatal(err)
	}
	current, err := store.Open(s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if b, ok := current.FindClear("one"); !ok || b.Agent != "pi" {
		t.Fatal(b, ok)
	}
	for number, want := range []string{"original", "fresh"} {
		reader, ok := current.ContextAt(number)
		if !ok {
			t.Fatal(number)
		}
		task, err := reader.LoadTaskState()
		if err != nil || ((number == 0) != (task != nil)) {
			t.Fatal(number, task, err)
		}
		rows, err := reader.LoadMessages()
		if err != nil || len(rows) != 1 || rows[0]["content"] != want {
			t.Fatal(rows, err)
		}
	}
}
