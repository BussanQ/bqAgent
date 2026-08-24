package runtime

import (
	"testing"
	"time"

	"bqagent/internal/agent"
)

func TestConfigFromEnvParsesStreamIdleTimeout(t *testing.T) {
	if got := ConfigFromEnv(func(string) string { return "" }).StreamIdleTimeout; got != agent.DefaultStreamIdleTimeout {
		t.Fatalf("default StreamIdleTimeout = %s, want %s", got, agent.DefaultStreamIdleTimeout)
	}
	configured := ConfigFromEnv(func(key string) string {
		if key == "LLM_STREAM_IDLE_TIMEOUT" {
			return "45s"
		}
		return ""
	})
	if configured.StreamIdleTimeout != 45*time.Second {
		t.Fatalf("configured StreamIdleTimeout = %s, want 45s", configured.StreamIdleTimeout)
	}
	disabled := ConfigFromEnv(func(key string) string {
		if key == "LLM_STREAM_IDLE_TIMEOUT" {
			return "0"
		}
		return ""
	})
	if disabled.StreamIdleTimeout != 0 {
		t.Fatalf("disabled StreamIdleTimeout = %s, want 0", disabled.StreamIdleTimeout)
	}
	for _, value := range []string{"bad", "-1s"} {
		invalid := ConfigFromEnv(func(key string) string {
			if key == "LLM_STREAM_IDLE_TIMEOUT" {
				return value
			}
			return ""
		})
		if invalid.StreamIdleTimeout != agent.DefaultStreamIdleTimeout {
			t.Fatalf("StreamIdleTimeout(%q) = %s, want default", value, invalid.StreamIdleTimeout)
		}
	}
}

func TestConfigFromEnvUsesEffectiveDefaultModel(t *testing.T) {
	config := ConfigFromEnv(func(string) string { return "" })
	if config.Model != agent.DefaultModel {
		t.Fatalf("Model = %q, want %q", config.Model, agent.DefaultModel)
	}
	if config.APIType != agent.APITypeOpenAI {
		t.Fatalf("APIType = %q, want %q", config.APIType, agent.APITypeOpenAI)
	}
}

func TestConfigFromEnvUsesSelectedLLMAPIType(t *testing.T) {
	values := map[string]string{
		"LLM_API_TYPE":       "Anthropic",
		"ANTHROPIC_API_KEY":  "anthropic-key",
		"ANTHROPIC_BASE_URL": "https://anthropic.example/v1",
		"ANTHROPIC_MODEL":    "claude-test",
		"OPENAI_API_KEY":     "openai-key",
	}

	config := ConfigFromEnv(func(key string) string { return values[key] })
	if config.APIType != "anthropic" {
		t.Fatalf("APIType = %q, want anthropic", config.APIType)
	}
	if config.APIKey != "anthropic-key" || config.BaseURL != "https://anthropic.example/v1" || config.Model != "claude-test" {
		t.Fatalf("LLM config = %#v", config)
	}
}

func TestConfigFromEnvParsesConfiguredModels(t *testing.T) {
	values := map[string]string{
		"LLM_MODELS": " k3, , MiniMax-M2.5, fast=gpt-5.1 ",
	}

	config := ConfigFromEnv(func(key string) string { return values[key] })
	want := []string{"k3", "MiniMax-M2.5", "fast=gpt-5.1"}
	if len(config.Models) != len(want) {
		t.Fatalf("Models = %#v, want %#v", config.Models, want)
	}
	for index := range want {
		if config.Models[index] != want[index] {
			t.Fatalf("Models = %#v, want %#v", config.Models, want)
		}
	}
}

func TestConfigFromEnvSupportsGenericLLMOverrides(t *testing.T) {
	values := map[string]string{
		"OPENAI_API_TYPE": "OpenAI-Response",
		"LLM_API_KEY":     "generic-key",
		"LLM_BASE_URL":    "https://responses.example/v1",
		"LLM_MODEL":       "gpt-test",
		"OPENAI_API_KEY":  "openai-key",
	}

	config := ConfigFromEnv(func(key string) string { return values[key] })
	if config.APIType != "openai-response" {
		t.Fatalf("APIType = %q, want openai-response", config.APIType)
	}
	if config.APIKey != "generic-key" || config.BaseURL != "https://responses.example/v1" || config.Model != "gpt-test" {
		t.Fatalf("LLM config = %#v", config)
	}
}

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
