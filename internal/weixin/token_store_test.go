package weixin

import (
	"path/filepath"
	"testing"
)

func TestTokenStorePersistsState(t *testing.T) {
	agentDir := t.TempDir()
	store := NewTokenStore(agentDir)
	if store.path != filepath.Join(agentDir, "server", "weixin", "token.json") {
		t.Fatalf("path = %q, want global agent server path", store.path)
	}
	want := TokenState{BotToken: "token-1", BaseURL: "https://example.com", AccountID: "account-1", UserID: "user-1"}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got.BotToken != want.BotToken {
		t.Fatalf("BotToken = %q, want %q", got.BotToken, want.BotToken)
	}
	if got.BaseURL != want.BaseURL {
		t.Fatalf("BaseURL = %q, want %q", got.BaseURL, want.BaseURL)
	}
	if got.AccountID != want.AccountID {
		t.Fatalf("AccountID = %q, want %q", got.AccountID, want.AccountID)
	}
	if got.UserID != want.UserID {
		t.Fatalf("UserID = %q, want %q", got.UserID, want.UserID)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt is zero, want persisted timestamp")
	}
}
