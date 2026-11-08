package extagent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateStoreSavesToSharedPath(t *testing.T) {
	root := t.TempDir()
	store := NewStateStore(root)

	if err := store.Save(SessionState{
		BQSessionID:       "session-1",
		Agent:             AgentClaude,
		ExternalSessionID: "claude-session-1",
	}); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, ".agent", "external-agents", "sessions", "session-1.json")); err != nil {
		t.Fatalf("shared state path stat error = %v, want saved file", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".agent", "server", "external-agents", "sessions", "session-1.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy state path stat error = %v, want not exist", err)
	}
}

func TestStateStoreLoadsLegacyServerPath(t *testing.T) {
	root := t.TempDir()
	legacyDir := filepath.Join(root, ".agent", "server", "external-agents", "sessions")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	content := []byte("{\n  \"bq_session_id\": \"session-legacy\",\n  \"agent\": \"codex\",\n  \"external_session_id\": \"external-1\"\n}\n")
	if err := os.WriteFile(filepath.Join(legacyDir, "session-legacy.json"), content, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	store := NewStateStore(root)
	state, err := store.Load("session-legacy")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if state.Agent != AgentCodex {
		t.Fatalf("agent = %q, want %q", state.Agent, AgentCodex)
	}
	if state.ExternalSessionID != "external-1" {
		t.Fatalf("external session id = %q, want %q", state.ExternalSessionID, "external-1")
	}
}

func TestStateStoreKeepsGroupParticipantsIndependent(t *testing.T) {
	store := NewStateStore(t.TempDir())
	if err := store.SaveGroup(SessionState{BQSessionID: "group-session", Agent: AgentCodex, ExternalSessionID: "codex-thread"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveGroup(SessionState{BQSessionID: "group-session", Agent: AgentOpenCode, ExternalSessionID: "opencode-thread"}); err != nil {
		t.Fatal(err)
	}
	codex, err := store.LoadGroup("group-session", AgentCodex)
	if err != nil {
		t.Fatal(err)
	}
	opencode, err := store.LoadGroup("group-session", AgentOpenCode)
	if err != nil {
		t.Fatal(err)
	}
	if codex.ExternalSessionID != "codex-thread" || opencode.ExternalSessionID != "opencode-thread" {
		t.Fatalf("group states = codex:%q opencode:%q", codex.ExternalSessionID, opencode.ExternalSessionID)
	}
	if err := store.ClearGroup("group-session"); err != nil {
		t.Fatal(err)
	}
	cleared, err := store.LoadGroup("group-session", AgentCodex)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.ExternalSessionID != "" {
		t.Fatalf("cleared group state = %#v", cleared)
	}
}

func TestSingleChatSwitchKeepsIndependentResumeIDsUntilExplicitReset(t *testing.T) {
	root := t.TempDir()
	store := NewStateStore(root)
	broker := NewBroker(store, nil, nil)
	defer broker.Close()
	codex := SessionState{BQSessionID: "chat", Agent: AgentCodex, Transport: TransportAppServer, ExternalSessionID: "thread-1"}
	pi := SessionState{BQSessionID: "chat", Agent: AgentPi, Transport: TransportCLI, ExternalSessionID: "pi-2"}
	for _, state := range []SessionState{codex, pi} {
		if err := store.Save(state); err != nil {
			t.Fatal(err)
		}
	}
	if err := broker.UseBuiltin("chat"); err != nil {
		t.Fatal(err)
	}
	if active, err := broker.ActiveAgent("chat"); err != nil || active != "" {
		t.Fatalf("expected built-in, got %q: %v", active, err)
	}
	reopened := NewStateStore(root)
	for _, want := range []SessionState{pi, codex} {
		if got, err := reopened.stateForAgent("chat", want.Agent); err != nil || got != want {
			t.Fatalf("resuming %s: state=%+v, error=%v", want.Agent, got, err)
		}
	}
	if err := broker.Clear("chat"); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []AgentName{AgentPi, AgentCodex} {
		if got, err := reopened.stateForAgent("chat", agent); err != nil || got.ExternalSessionID != "" {
			t.Fatalf("reset left resume state for %s: %+v %v", agent, got, err)
		}
	}
}
