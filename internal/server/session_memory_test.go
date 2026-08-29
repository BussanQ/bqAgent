package server

import (
	"strings"
	"testing"

	appmemory "bqagent/internal/memory"
)

func TestCurrentSessionContextIncludesGlobalPreferences(t *testing.T) {
	workspaceStore := appmemory.NewStore(t.TempDir())
	globalStore := appmemory.NewStore(t.TempDir())
	if _, err := globalStore.Add(appmemory.KindUserPreference, "用户在北京", "", .9, "normal", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := workspaceStore.Add(appmemory.KindUserPreference, "偏好中文回复", "", .8, "normal", nil); err != nil {
		t.Fatal(err)
	}

	service := NewService(ServiceOptions{
		WorkspaceRoot:     t.TempDir(),
		MemoryStore:       workspaceStore,
		GlobalMemoryStore: globalStore,
	})
	snapshot, err := service.currentSessionContext("北京", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snapshot, "用户在北京") {
		t.Fatalf("snapshot missing global preference: %s", snapshot)
	}
	if !strings.Contains(snapshot, "偏好中文回复") {
		t.Fatalf("snapshot missing workspace preference: %s", snapshot)
	}
}
