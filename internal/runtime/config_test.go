package runtime

import "testing"

func TestConfigFromEnvPrefersFirecrawlSearchConfig(t *testing.T) {
	values := map[string]string{
		"FIRECRAWL_API_KEY":  "firecrawl-key",
		"SEARCH_API_KEY":     "legacy-key",
		"FIRECRAWL_BASE_URL": "https://firecrawl.example/v2",
		"SEARCH_BASE_URL":    "https://legacy.example",
	}

	config := ConfigFromEnv(func(key string) string {
		return values[key]
	})

	if config.SearchAPIKey != "firecrawl-key" {
		t.Fatalf("SearchAPIKey = %q, want firecrawl-key", config.SearchAPIKey)
	}
	if config.SearchBaseURL != "https://firecrawl.example/v2" {
		t.Fatalf("SearchBaseURL = %q, want https://firecrawl.example/v2", config.SearchBaseURL)
	}
}

func TestConfigFromEnvFallsBackToLegacySearchConfig(t *testing.T) {
	values := map[string]string{
		"SEARCH_API_KEY":  "legacy-key",
		"SEARCH_BASE_URL": "https://legacy.example",
	}

	config := ConfigFromEnv(func(key string) string {
		return values[key]
	})

	if config.SearchAPIKey != "legacy-key" {
		t.Fatalf("SearchAPIKey = %q, want legacy-key", config.SearchAPIKey)
	}
	if config.SearchBaseURL != "https://legacy.example" {
		t.Fatalf("SearchBaseURL = %q, want https://legacy.example", config.SearchBaseURL)
	}
}
