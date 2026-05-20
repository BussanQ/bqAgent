package runtime

import "testing"

func TestConfigFromEnvPrefersTavilySearchConfig(t *testing.T) {
	values := map[string]string{
		"SEARCH_API_KEY":     "tavily-key",
		"FIRECRAWL_API_KEY":  "firecrawl-key",
		"SEARCH_BASE_URL":    "https://tavily.example",
		"FIRECRAWL_BASE_URL": "https://firecrawl.example/v2",
	}

	config := ConfigFromEnv(func(key string) string {
		return values[key]
	})

	if config.SearchProvider != "tavily" {
		t.Fatalf("SearchProvider = %q, want tavily", config.SearchProvider)
	}
	if config.SearchAPIKey != "tavily-key" {
		t.Fatalf("SearchAPIKey = %q, want tavily-key", config.SearchAPIKey)
	}
	if config.SearchBaseURL != "https://tavily.example" {
		t.Fatalf("SearchBaseURL = %q, want https://tavily.example", config.SearchBaseURL)
	}
}

func TestConfigFromEnvFallsBackToFirecrawlSearchConfig(t *testing.T) {
	values := map[string]string{
		"FIRECRAWL_API_KEY":  "firecrawl-key",
		"FIRECRAWL_BASE_URL": "https://firecrawl.example/v2",
	}

	config := ConfigFromEnv(func(key string) string {
		return values[key]
	})

	if config.SearchProvider != "firecrawl" {
		t.Fatalf("SearchProvider = %q, want firecrawl", config.SearchProvider)
	}
	if config.SearchAPIKey != "firecrawl-key" {
		t.Fatalf("SearchAPIKey = %q, want firecrawl-key", config.SearchAPIKey)
	}
	if config.SearchBaseURL != "https://firecrawl.example/v2" {
		t.Fatalf("SearchBaseURL = %q, want https://firecrawl.example/v2", config.SearchBaseURL)
	}
}

func TestConfigFromEnvDefaultsToTavilySearchProvider(t *testing.T) {
	config := ConfigFromEnv(func(string) string { return "" })

	if config.SearchProvider != "tavily" {
		t.Fatalf("SearchProvider = %q, want tavily", config.SearchProvider)
	}
}
