package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnvParsesCommonSyntax(t *testing.T) {
	root := t.TempDir()
	content := "" +
		"# comment\n" +
		"OPENAI_API_KEY=test-key\n" +
		" OPENAI_BASE_URL = https://example.test/v1 \n" +
		"export OPENAI_MODEL=\"gpt-test\"\n" +
		"SEARCH_API_KEY='search-key'\n" +
		"PLAIN=value # trailing comment\n"

	if err := os.WriteFile(filepath.Join(root, ".env"), []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write .env: %v", err)
	}

	values := LoadDotEnv(root)
	if values["OPENAI_API_KEY"] != "test-key" {
		t.Fatalf("OPENAI_API_KEY = %q, want %q", values["OPENAI_API_KEY"], "test-key")
	}
	if values["OPENAI_BASE_URL"] != "https://example.test/v1" {
		t.Fatalf("OPENAI_BASE_URL = %q, want trimmed value", values["OPENAI_BASE_URL"])
	}
	if values["OPENAI_MODEL"] != "gpt-test" {
		t.Fatalf("OPENAI_MODEL = %q, want %q", values["OPENAI_MODEL"], "gpt-test")
	}
	if values["SEARCH_API_KEY"] != "search-key" {
		t.Fatalf("SEARCH_API_KEY = %q, want %q", values["SEARCH_API_KEY"], "search-key")
	}
	if values["PLAIN"] != "value" {
		t.Fatalf("PLAIN = %q, want %q", values["PLAIN"], "value")
	}
}

func TestMergeEnvPrefersProcessEnvironment(t *testing.T) {
	getenv := func(key string) string {
		if key == "OPENAI_API_KEY" {
			return "from-env"
		}
		return ""
	}

	merged := MergeEnv(getenv, map[string]string{
		"OPENAI_API_KEY": "from-dotenv",
		"OPENAI_MODEL":   "dotenv-model",
	})

	if got := merged("OPENAI_API_KEY"); got != "from-env" {
		t.Fatalf("OPENAI_API_KEY = %q, want env value", got)
	}
	if got := merged("OPENAI_MODEL"); got != "dotenv-model" {
		t.Fatalf("OPENAI_MODEL = %q, want dotenv fallback", got)
	}
}
