package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"bqagent/internal/tools"
)

const anthropicDefaultMaxTokens = 8192

type anthropicRequest struct {
	Model     string             `json:"model"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
	MaxTokens int                `json:"max_tokens"`
	Stream    bool               `json:"stream,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content []any  `json:"content"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
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
		return AssistantMessage{}, err
	}
	response, err := c.doAnthropicRequest(ctx, payload, false)
	if err != nil {
		return AssistantMessage{}, err
	}
	defer response.Body.Close()

	var decoded anthropicResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return AssistantMessage{}, err
	}
	return assistantFromAnthropic(decoded), nil
}

func (c *Client) createAnthropicMessageStream(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, onChunk func(string)) (AssistantMessage, error) {
	payload, err := buildAnthropicRequest(model, messages, definitions, ChatCompletionOptions{}, true)
	if err != nil {
		return AssistantMessage{}, err
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
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Usage anthropicUsage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}
		switch event.Type {
		case "message_start":
			usage.PromptTokens = event.Message.Usage.InputTokens
			usage.CompletionTokens = event.Message.Usage.OutputTokens
		case "content_block_start":
			if event.ContentBlock.Type == "tool_use" {
				calls[event.Index] = &partialCall{id: event.ContentBlock.ID, name: event.ContentBlock.Name}
			}
		case "content_block_delta":
			switch event.Delta.Type {
			case "text_delta":
				content.WriteString(event.Delta.Text)
				if onChunk != nil {
					onChunk(event.Delta.Text)
				}
			case "input_json_delta":
				call := calls[event.Index]
				if call == nil {
					call = &partialCall{}
					calls[event.Index] = call
				}
				call.arguments.WriteString(event.Delta.PartialJSON)
			}
		case "message_delta":
			if event.Usage.OutputTokens > 0 {
				usage.CompletionTokens = event.Usage.OutputTokens
			}
			if event.Delta.StopReason != "" {
				stopReason = event.Delta.StopReason
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return AssistantMessage{}, fmt.Errorf("reading anthropic stream: %w", err)
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
	system, converted := anthropicMessages(messages)
	if responseType, _ := options.ResponseFormat["type"].(string); responseType == "json_object" {
		system = strings.TrimSpace(system + "\n\nReturn the response as a valid JSON object.")
	}
	request := anthropicRequest{
		Model: model, System: system, Messages: converted,
		MaxTokens: anthropicDefaultMaxTokens, Stream: stream,
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
	return request, nil
}

func anthropicMessages(messages []map[string]any) (string, []anthropicMessage) {
	systemParts := make([]string, 0, 1)
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

	for _, message := range messages {
		role, _ := message["role"].(string)
		switch role {
		case "system", "developer":
			if text := strings.TrimSpace(contentText(message["content"])); text != "" {
				systemParts = append(systemParts, text)
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
	return strings.Join(systemParts, "\n\n"), converted
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
	message.Usage = TokenUsage{
		PromptTokens: response.Usage.InputTokens, CompletionTokens: response.Usage.OutputTokens,
		TotalTokens: response.Usage.InputTokens + response.Usage.OutputTokens,
	}
	message.normalizeInlineToolCalls()
	return message
}

func (c *Client) doAnthropicRequest(ctx context.Context, payload anthropicRequest, stream bool) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("anthropic-version", "2023-06-01")
	if stream {
		request.Header.Set("Accept", "text/event-stream")
	}
	if c.apiKey != "" {
		request.Header.Set("x-api-key", c.apiKey)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return response, nil
	}
	defer response.Body.Close()
	errorPayload, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		return nil, fmt.Errorf("anthropic messages request failed: %s", response.Status)
	}
	return nil, fmt.Errorf("anthropic messages request failed: %s: %s", response.Status, strings.TrimSpace(string(errorPayload)))
}
