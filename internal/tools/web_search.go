package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	defaultSearchProvider   = "tavily"
	firecrawlSearchProvider = "firecrawl"
	defaultTavilyBaseURL    = "https://api.tavily.com"
	defaultFirecrawlBaseURL = "https://api.firecrawl.dev/v2"
)

type searchRequest struct {
	APIKey     string `json:"api_key"`
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

type searchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

type searchResponse struct {
	Results []searchResult `json:"results"`
}

type firecrawlSearchRequest struct {
	Query         string                 `json:"query"`
	Limit         int                    `json:"limit"`
	Sources       []string               `json:"sources,omitempty"`
	ScrapeOptions firecrawlScrapeOptions `json:"scrapeOptions"`
}

type firecrawlScrapeOptions struct {
	Formats []string `json:"formats"`
}

type firecrawlSearchResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Markdown    string `json:"markdown"`
}

type firecrawlSearchResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
	Data    struct {
		Web []firecrawlSearchResult `json:"web"`
	} `json:"data"`
}

func WebSearch(ctx context.Context, args map[string]any) (string, error) {
	return WebSearchWithProviderConfig(searchProviderFromEnv(), searchAPIKeyFromEnv(), searchBaseURLFromEnv())(ctx, args)
}

func WebSearchWithConfig(apiKey, baseURL string) Function {
	return WebSearchWithProviderConfig(defaultSearchProvider, apiKey, baseURL)
}

func WebSearchWithProviderConfig(provider, apiKey, baseURL string) Function {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = defaultSearchProvider
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultBaseURLForProvider(provider)
	}
	baseURL = strings.TrimRight(baseURL, "/")

	return func(ctx context.Context, args map[string]any) (string, error) {
		query, err := requireStringAlias(args, "query", "search")
		if err != nil {
			return "", err
		}

		if strings.TrimSpace(apiKey) == "" {
			return "", missingSearchAPIKeyError(provider)
		}

		switch provider {
		case firecrawlSearchProvider:
			return firecrawlSearch(ctx, apiKey, baseURL, query)
		case defaultSearchProvider:
			return tavilySearch(ctx, apiKey, baseURL, query)
		default:
			return "", fmt.Errorf("unsupported search provider %q", provider)
		}
	}
}

func searchProviderFromEnv() string {
	if firstConfigured(os.Getenv("SEARCH_API_KEY"), os.Getenv("SEARCH_BASE_URL")) != "" {
		return defaultSearchProvider
	}
	if firstConfigured(os.Getenv("FIRECRAWL_API_KEY"), os.Getenv("FIRECRAWL_BASE_URL")) != "" {
		return firecrawlSearchProvider
	}
	return defaultSearchProvider
}

func searchAPIKeyFromEnv() string {
	return firstConfigured(os.Getenv("SEARCH_API_KEY"), os.Getenv("FIRECRAWL_API_KEY"))
}

func searchBaseURLFromEnv() string {
	return firstConfigured(os.Getenv("SEARCH_BASE_URL"), os.Getenv("FIRECRAWL_BASE_URL"))
}

func defaultBaseURLForProvider(provider string) string {
	if provider == firecrawlSearchProvider {
		return defaultFirecrawlBaseURL
	}
	return defaultTavilyBaseURL
}

func missingSearchAPIKeyError(provider string) error {
	if provider == firecrawlSearchProvider {
		return fmt.Errorf("FIRECRAWL_API_KEY is not set; web search requires a Firecrawl API key")
	}
	return fmt.Errorf("SEARCH_API_KEY is not set; web search requires a Tavily API key")
}

func tavilySearch(ctx context.Context, apiKey, baseURL, query string) (string, error) {
	body, err := json.Marshal(searchRequest{
		APIKey:     apiKey,
		Query:      query,
		MaxResults: 5,
	})
	if err != nil {
		return "", err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/search", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")

	resp, err := searchHTTPClient().Do(request)
	if err != nil {
		return "", fmt.Errorf("web search request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("web search failed: %s: %s", resp.Status, strings.TrimSpace(string(payload)))
	}

	var decoded searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return "", fmt.Errorf("failed to parse search response: %w", err)
	}

	if len(decoded.Results) == 0 {
		return "No results found.", nil
	}

	var sb strings.Builder
	for i, r := range decoded.Results {
		if i > 0 {
			sb.WriteString("\n---\n")
		}
		title := r.Title
		if strings.TrimSpace(title) == "" {
			title = r.URL
		}
		fmt.Fprintf(&sb, "**%s**\n%s\n%s", title, r.URL, r.Content)
	}
	return sb.String(), nil
}

func firecrawlSearch(ctx context.Context, apiKey, baseURL, query string) (string, error) {
	body, err := json.Marshal(firecrawlSearchRequest{
		Query:   query,
		Limit:   5,
		Sources: []string{"web"},
		ScrapeOptions: firecrawlScrapeOptions{
			Formats: []string{"markdown"},
		},
	})
	if err != nil {
		return "", err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/search", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")

	resp, err := searchHTTPClient().Do(request)
	if err != nil {
		return "", fmt.Errorf("web search request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("web search failed: %s: %s", resp.Status, strings.TrimSpace(string(payload)))
	}

	var decoded firecrawlSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return "", fmt.Errorf("failed to parse search response: %w", err)
	}
	if !decoded.Success && strings.TrimSpace(decoded.Error) != "" {
		return "", fmt.Errorf("web search failed: %s", decoded.Error)
	}

	if len(decoded.Data.Web) == 0 {
		return "No results found.", nil
	}

	var sb strings.Builder
	for i, r := range decoded.Data.Web {
		if i > 0 {
			sb.WriteString("\n---\n")
		}
		title := r.Title
		if strings.TrimSpace(title) == "" {
			title = r.URL
		}
		content := r.Markdown
		if strings.TrimSpace(content) == "" {
			content = r.Description
		}
		fmt.Fprintf(&sb, "**%s**\n%s\n%s", title, r.URL, content)
	}
	return sb.String(), nil
}

func searchHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}
