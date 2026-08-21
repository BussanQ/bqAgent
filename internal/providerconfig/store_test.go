package providerconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreEncryptsAPIKeyWithRandomSalt(t *testing.T) {
	agentDir := filepath.Join(t.TempDir(), ".agent")
	store := NewStore(agentDir)
	first, err := store.EncryptAPIKey("secret-token")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.EncryptAPIKey("secret-token")
	if err != nil {
		t.Fatal(err)
	}
	if first.Salt == second.Salt || first.Ciphertext == second.Ciphertext {
		t.Fatal("encrypting the same key should use a fresh salt and nonce")
	}
	config := Config{ActiveProvider: "openai", Providers: []Provider{{ID: "openai", Name: "OpenAI", APIType: "openai-responses", Models: []string{"gpt-test"}, DefaultModel: "gpt-test", APIKey: first}}}
	if err := store.Save(config); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(agentDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret-token") {
		t.Fatal("config.json contains the plaintext API key")
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := store.DecryptAPIKey(loaded.Providers[0].APIKey)
	if err != nil || plaintext != "secret-token" {
		t.Fatalf("decrypted key = %q, err = %v", plaintext, err)
	}
}
