package qq

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const DefaultTokenBaseURL = "https://bots.qq.com"

type AccessToken struct {
	Token     string
	ExpiresAt time.Time
}

type TokenClient struct {
	httpClient   *http.Client
	baseURL      string
	appID        string
	clientSecret string
}

type tokenRequest struct {
	AppID        string `json:"appId"`
	ClientSecret string `json:"clientSecret"`
}

type tokenResponse struct {
	AccessToken string          `json:"access_token"`
	ExpiresIn   json.RawMessage `json:"expires_in"`
}

type CachedTokenSource struct {
	client *TokenClient
	mu     sync.Mutex
	token  AccessToken
}

func NewTokenClient(appID, clientSecret, baseURL string, httpClient *http.Client) *TokenClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultTokenBaseURL
	}
	return &TokenClient{
		httpClient:   httpClient,
		baseURL:      strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		appID:        strings.TrimSpace(appID),
		clientSecret: strings.TrimSpace(clientSecret),
	}
}

func (client *TokenClient) Configured() bool {
	return client != nil && client.appID != "" && client.clientSecret != ""
}

func (client *TokenClient) GetAccessToken(ctx context.Context) (AccessToken, error) {
	if !client.Configured() {
		return AccessToken{}, fmt.Errorf("qq bot app id and client secret are required")
	}
	body, err := json.Marshal(tokenRequest{AppID: client.appID, ClientSecret: client.clientSecret})
	if err != nil {
		return AccessToken{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+"/app/getAppAccessToken", bytes.NewReader(body))
	if err != nil {
		return AccessToken{}, err
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := client.httpClient.Do(request)
	if err != nil {
		return AccessToken{}, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		return AccessToken{}, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return AccessToken{}, fmt.Errorf("qq token request failed: %s", strings.TrimSpace(string(payload)))
	}
	var decoded tokenResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return AccessToken{}, err
	}
	token := strings.TrimSpace(decoded.AccessToken)
	if token == "" {
		return AccessToken{}, fmt.Errorf("qq token response missing access_token")
	}
	expiresIn, err := parseExpiresIn(decoded.ExpiresIn)
	if err != nil {
		return AccessToken{}, err
	}
	if expiresIn > time.Minute {
		expiresIn -= time.Minute
	}
	return AccessToken{Token: token, ExpiresAt: time.Now().UTC().Add(expiresIn)}, nil
}

func NewCachedTokenSource(client *TokenClient) *CachedTokenSource {
	return &CachedTokenSource{client: client}
}

func (source *CachedTokenSource) Configured() bool {
	return source != nil && source.client != nil && source.client.Configured()
}

func (source *CachedTokenSource) Token(ctx context.Context) (string, error) {
	if source == nil || source.client == nil {
		return "", fmt.Errorf("qq token client is required")
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if strings.TrimSpace(source.token.Token) != "" && time.Now().UTC().Before(source.token.ExpiresAt) {
		return source.token.Token, nil
	}
	token, err := source.client.GetAccessToken(ctx)
	if err != nil {
		return "", err
	}
	source.token = token
	return token.Token, nil
}

func parseExpiresIn(raw json.RawMessage) (time.Duration, error) {
	if len(raw) == 0 {
		return 0, fmt.Errorf("qq token response missing expires_in")
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		seconds, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		if err != nil || seconds <= 0 {
			return 0, fmt.Errorf("invalid qq token expires_in")
		}
		return time.Duration(seconds) * time.Second, nil
	}
	var number float64
	if err := json.Unmarshal(raw, &number); err == nil && number > 0 {
		return time.Duration(number) * time.Second, nil
	}
	return 0, fmt.Errorf("invalid qq token expires_in")
}
