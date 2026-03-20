package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"bqagent/internal/tools"
)

const defaultBaseURL = "https://api.openai.com/v1"

type ChatCompletionClient interface {
	CreateChatCompletion(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition) (AssistantMessage, error)
}

type ChatCompletionOptions struct {
	ResponseFormat map[string]any
}

type Client struct {
	httpClient *http.Client
	apiKey     string
	baseURL    string
}

type AssistantMessage struct {
	Role      string     `json:"role"`
	Content   any        `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type,omitempty"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatCompletionRequest struct {
	Model          string             `json:"model"`
	Messages       []map[string]any   `json:"messages"`
	Tools          []tools.Definition `json:"tools,omitempty"`
	ResponseFormat map[string]any     `json:"response_format,omitempty"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message AssistantMessage `json:"message"`
	} `json:"choices"`
}

func NewClient(apiKey, baseURL string, httpClient *http.Client) *Client {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 2 * time.Minute}
	}
	return &Client{
		httpClient: httpClient,
		apiKey:     apiKey,
		baseURL:    strings.TrimRight(baseURL, "/"),
	}
}

func (c *Client) CreateChatCompletion(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition) (AssistantMessage, error) {
	return c.CreateChatCompletionWithOptions(ctx, model, messages, definitions, ChatCompletionOptions{})
}

func (c *Client) CreateChatCompletionWithOptions(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions) (AssistantMessage, error) {
	body, err := json.Marshal(chatCompletionRequest{
		Model:          model,
		Messages:       messages,
		Tools:          definitions,
		ResponseFormat: options.ResponseFormat,
	})
	if err != nil {
		return AssistantMessage{}, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return AssistantMessage{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return AssistantMessage{}, err
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		payload, readErr := io.ReadAll(response.Body)
		if readErr != nil {
			return AssistantMessage{}, fmt.Errorf("chat completions request failed: %s", response.Status)
		}
		return AssistantMessage{}, fmt.Errorf("chat completions request failed: %s: %s", response.Status, strings.TrimSpace(string(payload)))
	}

	var decoded chatCompletionResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return AssistantMessage{}, err
	}
	if len(decoded.Choices) == 0 {
		return AssistantMessage{}, fmt.Errorf("chat completions response contained no choices")
	}

	message := decoded.Choices[0].Message
	if message.Role == "" {
		message.Role = "assistant"
	}
	return message, nil
}

func (m AssistantMessage) RequestMessage() map[string]any {
	message := map[string]any{
		"role":    m.Role,
		"content": m.Content,
	}
	if len(m.ToolCalls) > 0 {
		message["tool_calls"] = m.ToolCalls
	}
	return message
}

func (m AssistantMessage) DisplayContent() string {
	if m.Content == nil {
		return "None"
	}
	if text, ok := m.Content.(string); ok {
		return text
	}
	payload, err := json.Marshal(m.Content)
	if err == nil {
		return string(payload)
	}
	return fmt.Sprint(m.Content)
}

func (m AssistantMessage) FinalContent() string {
	if m.Content == nil {
		return "None"
	}
	if text, ok := m.Content.(string); ok {
		return text
	}
	return fmt.Sprint(m.Content)
}
