package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultSearchBaseURL = "https://api.firecrawl.dev/v2"

type searchRequest struct {
	Query         string              `json:"query"`
	Limit         int                 `json:"limit"`
	Sources       []string            `json:"sources,omitempty"`
	ScrapeOptions searchScrapeOptions `json:"scrapeOptions"`
}

type searchScrapeOptions struct {
	Formats []string `json:"formats"`
}

type searchResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Markdown    string `json:"markdown"`
}

type searchResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
	Data    struct {
		Web []searchResult `json:"web"`
	} `json:"data"`
}

func WebSearch(ctx context.Context, args map[string]any) (string, error) {
	return WebSearchWithConfig("", "")(ctx, args)
}

func WebSearchWithConfig(apiKey, baseURL string) Function {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultSearchBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")

	return func(ctx context.Context, args map[string]any) (string, error) {
		query, err := requireStringAlias(args, "query", "search")
		if err != nil {
			return "", err
		}

		if strings.TrimSpace(apiKey) == "" {
			return "", fmt.Errorf("SEARCH_API_KEY or FIRECRAWL_API_KEY is not set; web search requires a Firecrawl API key")
		}

		body, err := json.Marshal(searchRequest{
			Query:   query,
			Limit:   5,
			Sources: []string{"web"},
			ScrapeOptions: searchScrapeOptions{
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

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(request)
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
}
