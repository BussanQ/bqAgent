package session

import (
	"errors"
	"testing"
)

func TestIndependentForkPublicationIsIdempotentAndDeletionIsFinal(t *testing.T) {
	store := NewStore(t.TempDir())
	source, err := store.Create(CreateOptions{Chat: true, Task: "source"})
	if err != nil {
		t.Fatal(err)
	}
	f := ForkOrigin{Session: source.ID(), Epoch: 0, Target: "response", Operation: "op"}
	failed := errors.New("not ready")
	if _, err := store.CreateFork(f, source.Meta(), func(child *Session, _ string) error {
		if chats, err := store.List(0); err != nil || len(chats) != 1 {
			t.Fatal(chats, err)
		}
		if _, err := store.CreateFork(f, source.Meta(), func(*Session, string) error { t.Fatal("duplicate prepared"); return nil }); !errors.Is(err, ErrContextBusy) {
			t.Fatal(err)
		}
		return failed
	}); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	if child, err := store.ExistingFork(f); err != nil || child != nil {
		t.Fatal(child, err)
	}
	child, err := store.CreateFork(f, source.Meta(), func(child *Session, _ string) error {
		return child.RewriteMessages([]map[string]any{{"role": "assistant", "content": "kept"}})
	})
	if err != nil || child.ID() == source.ID() {
		t.Fatal(child, err)
	}
	if replay, err := store.CreateFork(f, source.Meta(), func(*Session, string) error { t.Fatal("duplicate native creation"); return nil }); err != nil || replay.ID() != child.ID() {
		t.Fatal(replay, err)
	}
	if err := store.Delete(child.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFork(f, source.Meta(), func(*Session, string) error { t.Fatal("resurrected deleted fork"); return nil }); !errors.Is(err, ErrForkDeleted) {
		t.Fatal(err)
	}
}
