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
	"sort"
	"strings"

	"bqagent/internal/tools"
)

const anthropicDefaultMaxTokens = 8192

type anthropicRequest struct {
	Model        string                 `json:"model"`
	System       []anthropicSystemBlock `json:"system,omitempty"`
	Messages     []anthropicMessage     `json:"messages"`
	Tools        []anthropicTool        `json:"tools,omitempty"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
	MaxTokens    int                    `json:"max_tokens"`
	Thinking     *anthropicThinking     `json:"thinking,omitempty"`
	OutputConfig *anthropicOutputConfig `json:"output_config,omitempty"`
	Stream       bool                   `json:"stream,omitempty"`
}

type anthropicCacheControl struct {
	Type string `json:"type"`
}

type anthropicSystemBlock struct {
	Type         string                 `json:"type"`
	Text         string                 `json:"text"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicThinking struct {
	Type string `json:"type"`
}

type anthropicOutputConfig struct {
	Effort ReasoningEffort `json:"effort"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content []any  `json:"content"`
}

type anthropicTool struct {
	Name         string                 `json:"name"`
	Description  string                 `json:"description,omitempty"`
	InputSchema  json.RawMessage        `json:"input_schema"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicUsage struct {
	InputTokens              int  `json:"input_tokens"`
	OutputTokens             int  `json:"output_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
}

func (usage anthropicUsage) tokenUsage() TokenUsage {
	promptTokens := usage.InputTokens
	result := TokenUsage{CompletionTokens: usage.OutputTokens}
	if usage.CacheCreationInputTokens != nil {
		promptTokens += *usage.CacheCreationInputTokens
		result.CacheWritePromptTokens = *usage.CacheCreationInputTokens
		result.CacheUsageAvailable = true
	}
	if usage.CacheReadInputTokens != nil {
		promptTokens += *usage.CacheReadInputTokens
		result.CachedPromptTokens = *usage.CacheReadInputTokens
		result.CacheUsageAvailable = true
	}
	result.PromptTokens = promptTokens
	result.TotalTokens = promptTokens + usage.OutputTokens
	return result
}

type anthropicContentBlock struct {
	Type  string         `json:"type"`
	Text  string         `json:"text"`
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

type anthropicResponse struct {
	Role       string                  `json:"role"`
	Content    []anthropicContentBlock `json:"content"`
	StopReason string                  `json:"stop_reason"`
	Usage      anthropicUsage          `json:"usage"`
}

func (c *Client) createAnthropicMessage(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions) (AssistantMessage, error) {
	payload, err := buildAnthropicRequest(model, messages, definitions, options, false)
	if err != nil {
		return AssistantMessage{}, &ProviderError{Category: ProviderErrorRequest, Message: "build anthropic messages request", Cause: err}
	}
	response, err := c.doAnthropicRequest(ctx, payload, false)
	if err != nil {
		return AssistantMessage{}, err
	}
	defer response.Body.Close()

	var decoded anthropicResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return AssistantMessage{}, providerTransportError(ctx, err)
		}
		return AssistantMessage{}, providerProtocolError("decode anthropic messages response", err)
	}
	return assistantFromAnthropic(decoded), nil
}

func (c *Client) createAnthropicMessageStream(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions, state *streamAttemptState, onChunk func(string)) (AssistantMessage, error) {
	payload, err := buildAnthropicRequest(model, messages, definitions, options, true)
	if err != nil {
		return AssistantMessage{}, &ProviderError{Category: ProviderErrorRequest, Message: "build anthropic messages stream request", Cause: err}
	}
	response, err := c.doAnthropicRequest(ctx, payload, true)
	if err != nil {
		return AssistantMessage{}, err
	}
	defer response.Body.Close()

	type partialCall struct {
		id        string
		name      string
		arguments strings.Builder
	}
	content := strings.Builder{}
	calls := map[int]*partialCall{}
	usage := TokenUsage{}
	stopReason := ""
	sawMessageStop := false

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
		var event struct {
			Type    string `json:"type"`
			Index   int    `json:"index"`
			Message struct {
				Usage anthropicUsage `json:"usage"`
			} `json:"message"`
			ContentBlock anthropicContentBlock `json:"content_block"`
			Delta        struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Usage anthropicUsage `json:"usage"`
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Code    string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return AssistantMessage{}, providerProtocolError("invalid anthropic stream event", err)
		}
		if event.Type == "error" {
			message := strings.TrimSpace(event.Error.Message)
			if message == "" {
				message = "upstream reported an error event"
			}
			return AssistantMessage{}, providerStreamEventError(message, firstNonBlank(event.Error.Code, event.Error.Type))
		}
		switch event.Type {
		case "message_start":
			usage = event.Message.Usage.tokenUsage()
		case "content_block_start":
			if event.ContentBlock.Type == "tool_use" {
				state.markSemantic()
				calls[event.Index] = &partialCall{id: event.ContentBlock.ID, name: event.ContentBlock.Name}
			}
		case "content_block_delta":
			switch event.Delta.Type {
			case "text_delta":
				if event.Delta.Text != "" {
					state.markSemantic()
				}
				content.WriteString(event.Delta.Text)
				if onChunk != nil {
					onChunk(event.Delta.Text)
				}
			case "input_json_delta":
				if event.Delta.PartialJSON != "" {
					state.markSemantic()
				}
				call := calls[event.Index]
				if call == nil {
					call = &partialCall{}
					calls[event.Index] = call
				}
				call.arguments.WriteString(event.Delta.PartialJSON)
			case "thinking_delta", "signature_delta":
				if event.Delta.Thinking != "" || event.Delta.Signature != "" || event.Delta.Text != "" {
					state.markSemantic()
				}
			}
		case "message_delta":
			if event.Usage.OutputTokens > 0 {
				usage.CompletionTokens = event.Usage.OutputTokens
			}
			if event.Delta.StopReason != "" {
				stopReason = event.Delta.StopReason
			}
		case "message_stop":
			sawMessageStop = true
		}
	}
	if err := scanner.Err(); err != nil {
		return AssistantMessage{}, providerTransportError(ctx, err)
	}
	if !sawMessageStop {
		incomplete := &IncompleteStreamError{Provider: "anthropic", Reason: "missing message_stop event"}
		return AssistantMessage{}, providerProtocolError(incomplete.Error(), incomplete)
	}
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	message := AssistantMessage{Role: "assistant", Content: content.String(), Completion: completionFromStopReason(stopReason), Usage: usage}
	indexes := make([]int, 0, len(calls))
	for index := range calls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		call := calls[index]
		arguments := call.arguments.String()
		message.ToolCalls = append(message.ToolCalls, ToolCall{
			ID: call.id, Type: "function",
			Function: FunctionCall{Name: call.name, Arguments: arguments},
		})
	}
	message.normalizeInlineToolCalls()
	return message, nil
}

func buildAnthropicRequest(model string, messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions, stream bool) (anthropicRequest, error) {
	system, converted := anthropicMessages(messages, options.PromptCache)
	if responseType, _ := options.ResponseFormat["type"].(string); responseType == "json_object" {
		system = append(system, anthropicSystemBlock{Type: "text", Text: "Return the response as a valid JSON object."})
	}
	request := anthropicRequest{
		Model: model, System: system, Messages: converted,
		MaxTokens: anthropicDefaultMaxTokens, Stream: stream,
	}
	if options.PromptCache.Enabled {
		request.CacheControl = &anthropicCacheControl{Type: "ephemeral"}
	}
	if options.ReasoningEffort != ReasoningEffortAuto {
		request.Thinking = &anthropicThinking{Type: "adaptive"}
		request.OutputConfig = &anthropicOutputConfig{Effort: options.ReasoningEffort}
	}
	for _, definition := range definitions {
		schema, err := toolSchema(definition)
		if err != nil {
			return anthropicRequest{}, err
		}
		request.Tools = append(request.Tools, anthropicTool{
			Name: definition.Function.Name, Description: definition.Function.Description, InputSchema: schema,
		})
	}
	if options.PromptCache.Enabled && len(request.Tools) > 0 {
		request.Tools[len(request.Tools)-1].CacheControl = &anthropicCacheControl{Type: "ephemeral"}
	}
	return request, nil
}

func anthropicMessages(messages []map[string]any, cache PromptCacheOptions) ([]anthropicSystemBlock, []anthropicMessage) {
	systemParts := make([]anthropicSystemBlock, 0, 2)
	converted := make([]anthropicMessage, 0, len(messages))
	appendMessage := func(role string, blocks []any) {
		if len(blocks) == 0 {
			return
		}
		if len(converted) > 0 && converted[len(converted)-1].Role == role {
			converted[len(converted)-1].Content = append(converted[len(converted)-1].Content, blocks...)
			return
		}
		converted = append(converted, anthropicMessage{Role: role, Content: blocks})
	}

	for index, message := range messages {
		role, _ := message["role"].(string)
		switch role {
		case "system", "developer":
			if text := strings.TrimSpace(contentText(message["content"])); text != "" {
				block := anthropicSystemBlock{Type: "text", Text: text}
				if cache.Enabled && cache.ExplicitBreakpoint && index == cache.StableMessageCount-1 {
					block.CacheControl = &anthropicCacheControl{Type: "ephemeral"}
				}
				systemParts = append(systemParts, block)
			}
		case "tool":
			callID, _ := message["tool_call_id"].(string)
			appendMessage("user", []any{map[string]any{
				"type": "tool_result", "tool_use_id": callID, "content": contentText(message["content"]),
			}})
		case "assistant":
			blocks := anthropicContent(message["content"])
			for _, call := range messageToolCalls(message) {
				blocks = append(blocks, map[string]any{
					"type": "tool_use", "id": call.ID, "name": call.Function.Name,
					"input": parseToolArguments(call.Function.Arguments),
				})
			}
			appendMessage("assistant", blocks)
		case "user":
			appendMessage("user", anthropicContent(message["content"]))
		}
	}
	return systemParts, converted
}

func anthropicContent(content any) []any {
	if text, ok := content.(string); ok {
		return []any{map[string]any{"type": "text", "text": text}}
	}
	parts, ok := content.([]any)
	if !ok {
		if content == nil {
			return nil
		}
		return []any{map[string]any{"type": "text", "text": fmt.Sprint(content)}}
	}
	converted := make([]any, 0, len(parts))
	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok {
			continue
		}
		switch partType, _ := part["type"].(string); partType {
		case "text", "input_text":
			converted = append(converted, map[string]any{"type": "text", "text": contentText([]any{part})})
		case "image_url", "input_image":
			url := imageURLFromPart(part)
			if url == "" {
				url, _ = part["image_url"].(string)
			}
			if mediaType, data, ok := dataURI(url); ok {
				converted = append(converted, map[string]any{
					"type": "image", "source": map[string]any{
						"type": "base64", "media_type": mediaType, "data": data,
					},
				})
			} else if url != "" {
				converted = append(converted, map[string]any{
					"type": "image", "source": map[string]any{"type": "url", "url": url},
				})
			}
		}
	}
	return converted
}

func assistantFromAnthropic(response anthropicResponse) AssistantMessage {
	message := AssistantMessage{Role: firstNonBlank(response.Role, "assistant"), Completion: completionFromStopReason(response.StopReason)}
	var content strings.Builder
	for _, block := range response.Content {
		switch block.Type {
		case "text":
			content.WriteString(block.Text)
		case "tool_use":
			arguments, err := json.Marshal(block.Input)
			if err != nil {
				arguments = []byte("{}")
			}
			message.ToolCalls = append(message.ToolCalls, ToolCall{
				ID: block.ID, Type: "function",
				Function: FunctionCall{Name: block.Name, Arguments: string(arguments)},
			})
		}
	}
	message.Content = content.String()
	message.Usage = response.Usage.tokenUsage()
	message.normalizeInlineToolCalls()
	return message
}

func (c *Client) doAnthropicRequest(ctx context.Context, payload anthropicRequest, stream bool) (*http.Response, error) {
	return c.doAnthropicJSONRequest(ctx, c.baseURL+"/messages", payload, stream, "anthropic messages request")
}

func (c *Client) doAnthropicJSONRequest(ctx context.Context, url string, payload any, stream bool, label string) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, &ProviderError{Category: ProviderErrorRequest, Message: "encode " + label, Cause: err}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, &ProviderError{Category: ProviderErrorRequest, Message: "build " + label, Cause: err}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("anthropic-version", "2023-06-01")
	if stream {
		request.Header.Set("Accept", "text/event-stream")
	}
	if c.apiKey != "" {
		request.Header.Set("x-api-key", c.apiKey)
	}
	var response *http.Response
	if stream {
		response, err = c.doStreamingRequest(request)
	} else {
		response, err = c.httpClient.Do(request)
	}
	if err != nil {
		return nil, providerTransportError(ctx, err)
	}
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return response, nil
	}
	defer response.Body.Close()
	errorPayload, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		return nil, providerHTTPError(response, response.Status)
	}
	return nil, providerHTTPError(response, strings.TrimSpace(string(errorPayload)))
}
