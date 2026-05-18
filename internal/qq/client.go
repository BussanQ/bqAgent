package qq

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const DefaultAPIBaseURL = "https://api.sgroup.qq.com"

type TokenSource interface {
	Token(context.Context) (string, error)
	Configured() bool
}

type Client struct {
	httpClient *http.Client
	baseURL    string
	tokens     TokenSource
}

type SendTarget struct {
	Kind        UpdateKind
	UserOpenID  string
	GroupOpenID string
	MsgID       string
	MsgSeq      int
}

type SendMessageRequest struct {
	Content string `json:"content"`
	MsgType int    `json:"msg_type"`
	MsgSeq  int    `json:"msg_seq"`
	MsgID   string `json:"msg_id,omitempty"`
}

type SendMessageResponse struct {
	ID        string `json:"id,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

func NewClient(tokenSource TokenSource, apiBaseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	if strings.TrimSpace(apiBaseURL) == "" {
		apiBaseURL = DefaultAPIBaseURL
	}
	return &Client{
		httpClient: httpClient,
		baseURL:    strings.TrimRight(strings.TrimSpace(apiBaseURL), "/"),
		tokens:     tokenSource,
	}
}

func (client *Client) Configured() bool {
	return client != nil && client.tokens != nil && client.tokens.Configured()
}

func (client *Client) SendText(ctx context.Context, target SendTarget, text string) (SendMessageResponse, error) {
	if !client.Configured() {
		return SendMessageResponse{}, fmt.Errorf("qq bot client is not configured")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return SendMessageResponse{}, fmt.Errorf("text is required")
	}
	path, err := sendPath(target)
	if err != nil {
		return SendMessageResponse{}, err
	}
	token, err := client.tokens.Token(ctx)
	if err != nil {
		return SendMessageResponse{}, err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return SendMessageResponse{}, fmt.Errorf("qq access token is required")
	}
	body, err := json.Marshal(buildSendMessageRequest(target.MsgID, target.MsgSeq, text))
	if err != nil {
		return SendMessageResponse{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return SendMessageResponse{}, err
	}
	request.Header.Set("Authorization", "QQBot "+token)
	request.Header.Set("Content-Type", "application/json")

	response, err := client.httpClient.Do(request)
	if err != nil {
		return SendMessageResponse{}, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		return SendMessageResponse{}, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return SendMessageResponse{}, fmt.Errorf("qq send message failed: %s", strings.TrimSpace(string(payload)))
	}
	if len(strings.TrimSpace(string(payload))) == 0 {
		return SendMessageResponse{}, nil
	}
	var decoded SendMessageResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return SendMessageResponse{}, err
	}
	return decoded, nil
}

func sendPath(target SendTarget) (string, error) {
	switch target.Kind {
	case UpdateKindC2C:
		openid := strings.TrimSpace(target.UserOpenID)
		if openid == "" {
			return "", fmt.Errorf("user_openid is required")
		}
		return "/v2/users/" + url.PathEscape(openid) + "/messages", nil
	case UpdateKindGroup:
		groupOpenID := strings.TrimSpace(target.GroupOpenID)
		if groupOpenID == "" {
			return "", fmt.Errorf("group_openid is required")
		}
		return "/v2/groups/" + url.PathEscape(groupOpenID) + "/messages", nil
	default:
		return "", fmt.Errorf("unsupported qq send target")
	}
}

func buildSendMessageRequest(messageID string, msgSeq int, text string) SendMessageRequest {
	if msgSeq <= 0 {
		msgSeq = 1
	}
	return SendMessageRequest{
		Content: strings.TrimSpace(text),
		MsgType: 0,
		MsgSeq:  msgSeq,
		MsgID:   strings.TrimSpace(messageID),
	}
}
