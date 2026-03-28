package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultSearchBaseURL = "https://api.tavily.com"

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

func WebSearch(args map[string]any) (string, error) {
	return WebSearchWithConfig("", "")(args)
}

func WebSearchWithConfig(apiKey, baseURL string) Function {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultSearchBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")

	return func(args map[string]any) (string, error) {
		query, err := requireStringAlias(args, "query", "search")
		if err != nil {
			return "", err
		}

		if strings.TrimSpace(apiKey) == "" {
			return "", fmt.Errorf("SEARCH_API_KEY is not set; web search requires a Tavily API key")
		}

		body, err := json.Marshal(searchRequest{
			APIKey:     apiKey,
			Query:      query,
			MaxResults: 5,
		})
		if err != nil {
			return "", err
		}

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Post(baseURL+"/search", "application/json", bytes.NewReader(body))
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
			fmt.Fprintf(&sb, "**%s**\n%s\n%s", r.Title, r.URL, r.Content)
		}
		return sb.String(), nil
	}
}
