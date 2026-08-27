package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"bqagent/internal/tools"
)

const defaultBaseURL = "https://api.openai.com/v1"

type APIType string

const (
	APITypeOpenAI         APIType = "openai"
	APITypeOpenAIResponse APIType = "openai-response"
	APITypeAnthropic      APIType = "anthropic"
)

type ChatCompletionClient interface {
	CreateChatCompletion(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition) (AssistantMessage, error)
	CreateChatCompletionStream(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, onChunk func(string)) (AssistantMessage, error)
}

// InputTokenCounter is an optional provider capability for counting the exact
// model-visible input before generation. Providers without a count endpoint do
// not need to implement it; the agent falls back to its local estimate.
type InputTokenCounter interface {
	CountInputTokens(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions) (InputTokenCount, error)
}

type InputTokenCount struct {
	Tokens int
	Exact  bool
}

var ErrInputTokenCountUnsupported = errors.New("input token counting is not supported by this provider")

type ReasoningEffort string

const (
	ReasoningEffortAuto   ReasoningEffort = ""
	ReasoningEffortLow    ReasoningEffort = "low"
	ReasoningEffortMedium ReasoningEffort = "medium"
	ReasoningEffortHigh   ReasoningEffort = "high"
)

func ParseReasoningEffort(raw string) (ReasoningEffort, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "auto":
		return ReasoningEffortAuto, nil
	case string(ReasoningEffortLow):
		return ReasoningEffortLow, nil
	case string(ReasoningEffortMedium):
		return ReasoningEffortMedium, nil
	case string(ReasoningEffortHigh):
		return ReasoningEffortHigh, nil
	default:
		return ReasoningEffortAuto, fmt.Errorf("unsupported reasoning effort %q; expected auto, low, medium, or high", strings.TrimSpace(raw))
	}
}

type ChatCompletionOptions struct {
	ResponseFormat  map[string]any
	ReasoningEffort ReasoningEffort
}

type chatCompletionStreamOptionsClient interface {
	CreateChatCompletionStreamWithOptions(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions, onChunk func(string)) (AssistantMessage, error)
}

// IncompleteStreamError means an upstream stream ended without the terminal
// event required by that provider's protocol. Callers can use errors.As to
// distinguish it from transport and JSON protocol errors.
type IncompleteStreamError struct {
	Provider string
	Reason   string
}

func (err *IncompleteStreamError) Error() string {
	if err == nil {
		return "incomplete stream"
	}
	if err.Reason == "" {
		return fmt.Sprintf("%s stream ended before completion", err.Provider)
	}
	return fmt.Sprintf("%s stream incomplete: %s", err.Provider, err.Reason)
}

type Client struct {
	httpClient        *http.Client
	apiKey            string
	baseURL           string
	apiType           APIType
	streamIdleTimeout time.Duration
	retrySleep        func(context.Context, time.Duration) error
	retryJitter       func(time.Duration) time.Duration
}

type CompletionState struct {
	StopReason       string `json:"-"`
	OutputTruncated  bool   `json:"-"`
	IncompleteReason string `json:"-"`
}

type AssistantMessage struct {
	Role       string                  `json:"role"`
	Content    any                     `json:"content"`
	ToolCalls  []ToolCall              `json:"tool_calls,omitempty"`
	Completion CompletionState         `json:"-"`
	Usage      TokenUsage              `json:"-"`
	Request    ProviderRequestMetadata `json:"-"`
}

type TokenUsage struct {
	PromptTokens        int  `json:"prompt_tokens,omitempty"`
	CachedPromptTokens  int  `json:"cached_prompt_tokens,omitempty"`
	CompletionTokens    int  `json:"completion_tokens,omitempty"`
	ReasoningTokens     int  `json:"reasoning_tokens,omitempty"`
	TotalTokens         int  `json:"total_tokens,omitempty"`
	CacheUsageAvailable bool `json:"cache_usage_available,omitempty"`
	Estimated           bool `json:"estimated,omitempty"`
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
	Model           string             `json:"model"`
	Messages        []map[string]any   `json:"messages"`
	Tools           []tools.Definition `json:"tools,omitempty"`
	ResponseFormat  map[string]any     `json:"response_format,omitempty"`
	ReasoningEffort ReasoningEffort    `json:"reasoning_effort,omitempty"`
}

type chatCompletionStreamRequest struct {
	Model           string             `json:"model"`
	Messages        []map[string]any   `json:"messages"`
	Tools           []tools.Definition `json:"tools,omitempty"`
	Stream          bool               `json:"stream"`
	StreamOptions   map[string]any     `json:"stream_options,omitempty"`
	ReasoningEffort ReasoningEffort    `json:"reasoning_effort,omitempty"`
}

type chatCompletionUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func (usage chatCompletionUsage) tokenUsage() TokenUsage {
	totalTokens := usage.TotalTokens
	if totalTokens == 0 {
		totalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	result := TokenUsage{
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		ReasoningTokens:  usage.CompletionTokensDetails.ReasoningTokens,
		TotalTokens:      totalTokens,
	}
	if usage.PromptTokensDetails != nil {
		result.CachedPromptTokens = usage.PromptTokensDetails.CachedTokens
		result.CacheUsageAvailable = true
	}
	return result
}

type chatCompletionResponse struct {
	Choices []struct {
		Message      AssistantMessage `json:"message"`
		FinishReason string           `json:"finish_reason"`
	} `json:"choices"`
	Usage chatCompletionUsage `json:"usage"`
}

type inlineToolCallPayload struct {
	Name       string         `json:"name"`
	Parameters map[string]any `json:"parameters"`
}

var inlineToolArgumentPattern = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_-]*)\s*=\s*"([^"]*)"`)

type streamDelta struct {
	Role             string `json:"role"`
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content"`
	Reasoning        string `json:"reasoning"`
	ToolCalls        []struct {
		Index    int    `json:"index"`
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

type streamChoice struct {
	Delta        streamDelta `json:"delta"`
	FinishReason string      `json:"finish_reason"`
}

type streamChunk struct {
	Choices []streamChoice      `json:"choices"`
	Usage   chatCompletionUsage `json:"usage,omitempty"`
	Error   struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

func completionFromStopReason(reason string) CompletionState {
	reason = strings.TrimSpace(reason)
	return CompletionState{
		StopReason:      reason,
		OutputTruncated: strings.EqualFold(reason, "length") || strings.EqualFold(reason, "max_tokens") || strings.EqualFold(reason, "max_output_tokens"),
	}
}

func NewClient(apiKey, baseURL string, httpClient *http.Client) *Client {
	return NewClientWithAPIType(apiKey, baseURL, APITypeOpenAI, httpClient)
}

func NewClientWithAPIType(apiKey, baseURL string, apiType APIType, httpClient *http.Client) *Client {
	return NewClientWithOptions(apiKey, baseURL, apiType, httpClient, ClientOptions{StreamIdleTimeout: DefaultStreamIdleTimeout})
}

func NewClientWithOptions(apiKey, baseURL string, apiType APIType, httpClient *http.Client, options ClientOptions) *Client {
	apiType = NormalizeAPIType(string(apiType))
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultBaseURLForAPIType(apiType)
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 2 * time.Minute}
	}
	return &Client{
		httpClient:        httpClient,
		apiKey:            apiKey,
		baseURL:           strings.TrimRight(baseURL, "/"),
		apiType:           apiType,
		streamIdleTimeout: options.StreamIdleTimeout,
		retryJitter:       defaultProviderRetryJitter,
	}
}

func (c *Client) doStreamingRequest(request *http.Request) (*http.Response, error) {
	streamingClient := *c.httpClient
	streamingClient.Timeout = 0
	if c.streamIdleTimeout <= 0 {
		return streamingClient.Do(request)
	}
	watchdogContext, watchdog := newStreamWatchdog(request.Context(), c.streamIdleTimeout)
	response, err := streamingClient.Do(request.Clone(watchdogContext))
	if err != nil {
		watchdog.stop()
		if request.Context().Err() != nil {
			return nil, request.Context().Err()
		}
		if cause := context.Cause(watchdogContext); cause != nil {
			return nil, cause
		}
		return nil, err
	}
	watchdog.touch()
	response.Body = &watchdogResponseBody{inner: response.Body, watchdog: watchdog}
	return response, nil
}

func NormalizeAPIType(raw string) APIType {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	switch normalized {
	case "openai-response", "openai-responses", "response", "responses":
		return APITypeOpenAIResponse
	case "anthropic", "claude":
		return APITypeAnthropic
	default:
		return APITypeOpenAI
	}
}

func defaultBaseURLForAPIType(apiType APIType) string {
	if apiType == APITypeAnthropic {
		return "https://api.anthropic.com/v1"
	}
	return defaultBaseURL
}

func (c *Client) CreateChatCompletion(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition) (AssistantMessage, error) {
	return c.CreateChatCompletionWithOptions(ctx, model, messages, definitions, ChatCompletionOptions{})
}

func (c *Client) CreateChatCompletionWithOptions(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions) (AssistantMessage, error) {
	return c.executeWithResilience(ctx, model, options, false, func(attemptOptions ChatCompletionOptions, _ *streamAttemptState) (AssistantMessage, error) {
		switch c.apiType {
		case APITypeOpenAIResponse:
			return c.createOpenAIResponse(ctx, model, messages, definitions, attemptOptions)
		case APITypeAnthropic:
			return c.createAnthropicMessage(ctx, model, messages, definitions, attemptOptions)
		default:
			return c.createChatCompletion(ctx, model, messages, definitions, attemptOptions)
		}
	})
}

func (c *Client) createChatCompletion(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions) (AssistantMessage, error) {

	body, err := json.Marshal(chatCompletionRequest{
		Model:           model,
		Messages:        messages,
		Tools:           definitions,
		ResponseFormat:  options.ResponseFormat,
		ReasoningEffort: options.ReasoningEffort,
	})
	if err != nil {
		return AssistantMessage{}, &ProviderError{Category: ProviderErrorRequest, Message: "encode chat completions request", Cause: err}
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return AssistantMessage{}, &ProviderError{Category: ProviderErrorRequest, Message: "build chat completions request", Cause: err}
	}
	request.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return AssistantMessage{}, providerTransportError(ctx, err)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		payload, readErr := io.ReadAll(response.Body)
		if readErr != nil {
			return AssistantMessage{}, providerHTTPError(response, response.Status)
		}
		return AssistantMessage{}, providerHTTPError(response, strings.TrimSpace(string(payload)))
	}

	var decoded chatCompletionResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return AssistantMessage{}, providerTransportError(ctx, err)
		}
		return AssistantMessage{}, providerProtocolError("decode chat completions response", err)
	}
	if len(decoded.Choices) == 0 {
		return AssistantMessage{}, providerProtocolError("chat completions response contained no choices", nil)
	}

	choice := decoded.Choices[0]
	message := choice.Message
	message.Completion = completionFromStopReason(choice.FinishReason)
	message.Usage = decoded.Usage.tokenUsage()
	if message.Role == "" {
		message.Role = "assistant"
	}
	message.normalizeInlineToolCalls()
	return message, nil
}

func (c *Client) CreateChatCompletionStream(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, onChunk func(string)) (AssistantMessage, error) {
	return c.CreateChatCompletionStreamWithOptions(ctx, model, messages, definitions, ChatCompletionOptions{}, onChunk)
}

func (c *Client) CreateChatCompletionStreamWithOptions(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions, onChunk func(string)) (AssistantMessage, error) {
	return c.executeWithResilience(ctx, model, options, true, func(attemptOptions ChatCompletionOptions, state *streamAttemptState) (AssistantMessage, error) {
		switch c.apiType {
		case APITypeOpenAIResponse:
			return c.createOpenAIResponseStream(ctx, model, messages, definitions, attemptOptions, state, onChunk)
		case APITypeAnthropic:
			return c.createAnthropicMessageStream(ctx, model, messages, definitions, attemptOptions, state, onChunk)
		default:
			return c.createChatCompletionStream(ctx, model, messages, definitions, attemptOptions, state, onChunk)
		}
	})
}

func (c *Client) createChatCompletionStream(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions, state *streamAttemptState, onChunk func(string)) (AssistantMessage, error) {

	body, err := json.Marshal(chatCompletionStreamRequest{
		Model:           model,
		Messages:        messages,
		Tools:           definitions,
		Stream:          true,
		StreamOptions:   map[string]any{"include_usage": true},
		ReasoningEffort: options.ReasoningEffort,
	})
	if err != nil {
		return AssistantMessage{}, &ProviderError{Category: ProviderErrorRequest, Message: "encode chat completions stream request", Cause: err}
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return AssistantMessage{}, &ProviderError{Category: ProviderErrorRequest, Message: "build chat completions stream request", Cause: err}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	if c.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	response, err := c.doStreamingRequest(request)
	if err != nil {
		return AssistantMessage{}, providerTransportError(ctx, err)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		payload, readErr := io.ReadAll(response.Body)
		if readErr != nil {
			return AssistantMessage{}, providerHTTPError(response, response.Status)
		}
		return AssistantMessage{}, providerHTTPError(response, strings.TrimSpace(string(payload)))
	}

	type partialToolCall struct {
		id        string
		callType  string
		name      string
		arguments strings.Builder
	}

	var (
		contentBuilder strings.Builder
		role           string
		finishReason   string
		toolCallMap    = map[int]*partialToolCall{}
		usage          TokenUsage
		sawDone        bool
	)

	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			sawDone = true
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return AssistantMessage{}, providerProtocolError("invalid chat completions stream event", err)
		}
		if chunk.Error.Message != "" || chunk.Error.Type != "" || chunk.Error.Code != "" {
			return AssistantMessage{}, providerStreamEventError(chunk.Error.Message, firstNonBlank(chunk.Error.Code, chunk.Error.Type))
		}
		if chunk.Usage.TotalTokens > 0 || chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
			usage = chunk.Usage.tokenUsage()
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		if choice.FinishReason != "" {
			finishReason = choice.FinishReason
		}
		delta := choice.Delta
		if delta.Role != "" {
			role = delta.Role
		}
		if delta.Content != "" {
			state.markSemantic()
			contentBuilder.WriteString(delta.Content)
			if onChunk != nil {
				onChunk(delta.Content)
			}
		}
		if delta.ReasoningContent != "" || delta.Reasoning != "" {
			state.markSemantic()
		}
		if len(delta.ToolCalls) > 0 {
			state.markSemantic()
		}
		for _, tc := range delta.ToolCalls {
			ptc, ok := toolCallMap[tc.Index]
			if !ok {
				ptc = &partialToolCall{}
				toolCallMap[tc.Index] = ptc
			}
			if tc.ID != "" {
				ptc.id = tc.ID
			}
			if tc.Type != "" {
				ptc.callType = tc.Type
			}
			if tc.Function.Name != "" {
				ptc.name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				ptc.arguments.WriteString(tc.Function.Arguments)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return AssistantMessage{}, providerTransportError(ctx, err)
	}
	if !sawDone {
		incomplete := &IncompleteStreamError{Provider: "chat completions", Reason: "missing [DONE] marker"}
		return AssistantMessage{}, providerProtocolError(incomplete.Error(), incomplete)
	}

	if role == "" {
		role = "assistant"
	}

	message := AssistantMessage{
		Role:       role,
		Content:    contentBuilder.String(),
		Completion: completionFromStopReason(finishReason),
		Usage:      usage,
	}
	indices := make([]int, 0, len(toolCallMap))
	for index := range toolCallMap {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		ptc := toolCallMap[index]
		message.ToolCalls = append(message.ToolCalls, ToolCall{
			ID:   ptc.id,
			Type: ptc.callType,
			Function: FunctionCall{
				Name:      ptc.name,
				Arguments: ptc.arguments.String(),
			},
		})
	}
	message.normalizeInlineToolCalls()
	return message, nil
}

func (m *AssistantMessage) normalizeInlineToolCalls() {
	if m == nil || len(m.ToolCalls) > 0 {
		return
	}
	text, ok := m.Content.(string)
	if !ok {
		return
	}
	m.ToolCalls = extractInlineToolCalls(text)
}

func stripInlineToolCallMarkup(content string) string {
	if !strings.Contains(content, "<tool_call>") {
		return content
	}

	var cleaned strings.Builder
	remaining := content
	for {
		start := strings.Index(remaining, "<tool_call>")
		if start < 0 {
			cleaned.WriteString(remaining)
			break
		}

		cleaned.WriteString(remaining[:start])
		afterStart := remaining[start+len("<tool_call>"):]
		end := strings.Index(afterStart, "</tool_call>")
		if end < 0 {
			break
		}
		remaining = afterStart[end+len("</tool_call>"):]
	}

	return strings.TrimSpace(cleaned.String())
}

func extractInlineToolCalls(content string) []ToolCall {
	toolCalls := make([]ToolCall, 0)
	remaining := content
	for index := 0; ; index++ {
		start := strings.Index(remaining, "<tool_call>")
		if start < 0 {
			break
		}
		afterStart := remaining[start+len("<tool_call>"):]
		end := strings.Index(afterStart, "</tool_call>")
		if end < 0 {
			break
		}

		payloadText := strings.TrimSpace(afterStart[:end])
		remaining = afterStart[end+len("</tool_call>"):]
		if payloadText == "" {
			continue
		}

		payload, ok := parseInlineToolCallPayload(payloadText)
		if !ok {
			continue
		}
		if strings.TrimSpace(payload.Name) == "" {
			continue
		}

		arguments, err := json.Marshal(payload.Parameters)
		if err != nil {
			continue
		}
		toolCalls = append(toolCalls, ToolCall{
			ID:   fmt.Sprintf("inline-tool-%d", index+1),
			Type: "function",
			Function: FunctionCall{
				Name:      payload.Name,
				Arguments: string(arguments),
			},
		})
	}
	return toolCalls
}

func parseInlineToolCallPayload(payloadText string) (inlineToolCallPayload, bool) {
	var payload inlineToolCallPayload
	if err := json.Unmarshal([]byte(payloadText), &payload); err == nil {
		if payload.Parameters == nil {
			payload.Parameters = map[string]any{}
		}
		return payload, strings.TrimSpace(payload.Name) != ""
	}

	fields := strings.Fields(payloadText)
	if len(fields) == 0 {
		return inlineToolCallPayload{}, false
	}

	payload.Name = strings.TrimSpace(fields[0])
	payload.Parameters = map[string]any{}
	for _, match := range inlineToolArgumentPattern.FindAllStringSubmatch(payloadText, -1) {
		if len(match) != 3 {
			continue
		}
		payload.Parameters[match[1]] = match[2]
	}
	if strings.TrimSpace(payload.Name) == "" {
		return inlineToolCallPayload{}, false
	}
	return payload, true
}

func (m AssistantMessage) RequestMessage() map[string]any {
	role := m.Role
	if strings.TrimSpace(role) == "" {
		role = "assistant"
	}

	content := m.Content
	if len(m.ToolCalls) > 0 {
		if text, ok := content.(string); ok {
			cleaned := stripInlineToolCallMarkup(text)
			if strings.TrimSpace(cleaned) == "" {
				content = nil
			} else {
				// Providers that support tool calls often expect assistant tool-call
				// messages to carry structured calls only, not extra reasoning text.
				content = nil
			}
		}
	}

	message := map[string]any{
		"role":    role,
		"content": content,
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
