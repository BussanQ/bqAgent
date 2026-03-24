package weixin

import "testing"

func TestPollerStateStorePersistsState(t *testing.T) {
	store := NewPollerStateStore(t.TempDir())
	want := PollerState{GetUpdatesBuf: "cursor-1", LastError: "temporary failure"}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got.GetUpdatesBuf != want.GetUpdatesBuf {
		t.Fatalf("GetUpdatesBuf = %q, want %q", got.GetUpdatesBuf, want.GetUpdatesBuf)
	}
	if got.LastError != want.LastError {
		t.Fatalf("LastError = %q, want %q", got.LastError, want.LastError)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt is zero, want persisted timestamp")
	}
}
