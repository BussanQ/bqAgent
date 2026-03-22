package serverchan

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

const defaultBotAPIBaseURL = "https://bot-go.apijia.cn"

type BotClient struct {
	httpClient *http.Client
	token      string
	baseURL    string
}

type BotSendMessageRequest struct {
	ChatID int64  `json:"chat_id"`
	Text   string `json:"text"`
}

type BotSendMessageResponse struct {
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Result struct {
		MessageID int64  `json:"message_id,omitempty"`
		ChatID    int64  `json:"chat_id,omitempty"`
		Text      string `json:"text,omitempty"`
	} `json:"result,omitempty"`
}

func NewBotClient(token string, httpClient *http.Client) *BotClient {
	return NewBotClientWithBaseURL(token, defaultBotAPIBaseURL, httpClient)
}

func NewBotClientWithBaseURL(token, baseURL string, httpClient *http.Client) *BotClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultBotAPIBaseURL
	}
	return &BotClient{
		httpClient: httpClient,
		token:      strings.TrimSpace(token),
		baseURL:    strings.TrimRight(baseURL, "/"),
	}
}

func (client *BotClient) Configured() bool {
	return client != nil && client.token != ""
}

func (client *BotClient) SendMessage(ctx context.Context, chatID int64, text string) (BotSendMessageResponse, error) {
	if client == nil || strings.TrimSpace(client.token) == "" {
		return BotSendMessageResponse{}, fmt.Errorf("serverchan bot token is required")
	}
	if chatID <= 0 {
		return BotSendMessageResponse{}, fmt.Errorf("chat_id is required")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return BotSendMessageResponse{}, fmt.Errorf("text is required")
	}

	body, err := json.Marshal(BotSendMessageRequest{ChatID: chatID, Text: text})
	if err != nil {
		return BotSendMessageResponse{}, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+"/bot"+client.token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return BotSendMessageResponse{}, err
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := client.httpClient.Do(request)
	if err != nil {
		return BotSendMessageResponse{}, err
	}
	defer response.Body.Close()

	payload, err := io.ReadAll(response.Body)
	if err != nil {
		return BotSendMessageResponse{}, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return BotSendMessageResponse{}, fmt.Errorf("serverchan bot send failed: %s", strings.TrimSpace(string(payload)))
	}

	var decoded BotSendMessageResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return BotSendMessageResponse{}, err
	}
	if !decoded.OK {
		message := strings.TrimSpace(decoded.Error)
		if message == "" {
			message = "serverchan bot send failed"
		}
		return decoded, fmt.Errorf(message)
	}
	return decoded, nil
}
