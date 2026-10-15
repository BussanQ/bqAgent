package tools

import (
	"context"
	"testing"
)

func TestWebSearchAvailabilityFollowsConfiguredKey(t *testing.T) {
	for _, test := range []struct {
		name, searchKey, firecrawlKey, searchURL, firecrawlURL, optionKey string
		wantDefault, wantCatalog                                          bool
	}{
		{name: "no keys"},
		{name: "blank keys", searchKey: " \t", firecrawlKey: "\n", optionKey: " "},
		{name: "Tavily URL only", searchURL: "https://search.example"},
		{name: "Firecrawl URL only", firecrawlURL: "https://search.example/v2"},
		{name: "Tavily key", searchKey: "tavily-key", wantDefault: true, wantCatalog: true},
		{name: "Firecrawl key", firecrawlKey: "firecrawl-key", wantDefault: true, wantCatalog: true},
		{name: "blank Tavily key falls back", searchKey: " ", firecrawlKey: "firecrawl-key", wantDefault: true, wantCatalog: true},
		{name: "explicit key", optionKey: "configured-key", wantCatalog: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("SEARCH_API_KEY", test.searchKey)
			t.Setenv("FIRECRAWL_API_KEY", test.firecrawlKey)
			t.Setenv("SEARCH_BASE_URL", test.searchURL)
			t.Setenv("FIRECRAWL_BASE_URL", test.firecrawlURL)
			assertSearchAvailability(t, Definitions(), Registry(), test.wantDefault)
			options := Options{SearchAPIKey: test.optionKey}
			catalog := NewCatalog(options)
			assertSearchAvailability(t, catalog.Definitions(), catalog.Registry(), test.wantCatalog)
			assertSearchAvailability(t, catalog.Definitions(), RegistryWithOptions(options), test.wantCatalog)

		})
	}
}

func TestDisabledWebSearchCannotBeReintroducedByExtraTools(t *testing.T) {
	t.Setenv("SEARCH_API_KEY", "")
	t.Setenv("FIRECRAWL_API_KEY", "")
	noop := func(context.Context, map[string]any) (string, error) { return "unused", nil }
	catalog := NewCatalog(Options{
		ExtraDefinitions: []Definition{
			{Type: "function", Function: FunctionDefinition{Name: "web_search"}},
			{Type: "function", Function: FunctionDefinition{Name: "mcp_search"}},
		},
		ExtraFunctions: map[string]Function{"web_search": noop, "mcp_search": noop},
	})
	assertSearchAvailability(t, catalog.Definitions(), catalog.Registry(), false)
	foundExtra := false
	for _, definition := range catalog.Definitions() {
		foundExtra = foundExtra || definition.Function.Name == "mcp_search"
	}
	if !foundExtra || catalog.Registry()["mcp_search"] == nil {
		t.Fatal("disabling builtin search must not disable independent MCP tools")
	}
}

func assertSearchAvailability(t *testing.T, definitions []Definition, registry map[string]Function, want bool) {
	t.Helper()
	names := make(map[string]bool)
	for _, definition := range definitions {
		names[definition.Function.Name] = true
	}
	if names["web_search"] != want || (registry["web_search"] != nil) != want {
		t.Fatalf("web_search definition=%v executable=%v, want %v", names["web_search"], registry["web_search"] != nil, want)
	}
	for _, definition := range builtinDefinitions() {
		name := definition.Function.Name
		if name != "web_search" && (!names[name] || registry[name] == nil) {
			t.Errorf("unrelated builtin %q was removed", name)
		}
	}
}
