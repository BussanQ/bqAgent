package qq

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestChatStateFilesArePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	agentDir := t.TempDir()
	store := NewStateStore(agentDir)
	if err := store.Save(ChatState{PeerKey: "peer", PendingReply: "secret reply"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.statePath("peer"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("chat state mode = %04o, want 0600", mode)
	}
	dirInfo, err := os.Stat(filepath.Dir(store.statePath("peer")))
	if err != nil {
		t.Fatal(err)
	}
	if mode := dirInfo.Mode().Perm(); mode != 0o700 {
		t.Fatalf("chat state dir mode = %04o, want 0700", mode)
	}
	loaded, err := store.Load("peer")
	if err != nil || loaded.PendingReply != "secret reply" {
		t.Fatalf("Load = %#v, %v", loaded, err)
	}
}
