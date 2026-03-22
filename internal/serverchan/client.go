package serverchan

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Client struct {
	httpClient *http.Client
}

func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{httpClient: httpClient}
}

func (client *Client) Send(ctx context.Context, key, text, desp string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("sendkey is required")
	}

	apiURL, err := resolveSendURL(key)
	if err != nil {
		return "", err
	}

	data := url.Values{}
	data.Set("text", strings.TrimSpace(text))
	data.Set("desp", strings.TrimSpace(desp))

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := client.httpClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}
	result := strings.TrimSpace(string(body))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		if result == "" {
			result = response.Status
		}
		return "", fmt.Errorf("serverchan send failed: %s", result)
	}
	return result, nil
}

func resolveSendURL(key string) (string, error) {
	if strings.HasPrefix(key, "sctp") {
		re := regexp.MustCompile(`sctp(\d+)t`)
		matches := re.FindStringSubmatch(key)
		if len(matches) < 2 {
			return "", fmt.Errorf("invalid sendkey format for sctp")
		}
		return fmt.Sprintf("https://%s.push.ft07.com/send/%s.send", matches[1], key), nil
	}
	return fmt.Sprintf("https://sctapi.ftqq.com/%s.send", key), nil
}
