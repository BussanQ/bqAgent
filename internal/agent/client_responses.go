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

type openAIResponseRequest struct {
	Model  string               `json:"model"`
	Input  []any                `json:"input"`
	Tools  []openAIResponseTool `json:"tools,omitempty"`
	Text   map[string]any       `json:"text,omitempty"`
	Stream bool                 `json:"stream,omitempty"`
}

type openAIResponseTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type openAIResponseUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type openAIResponseOutput struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Role      string `json:"role"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

type openAIResponseEnvelope struct {
	Status            string `json:"status"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details,omitempty"`
	Output []openAIResponseOutput `json:"output"`
	Usage  openAIResponseUsage    `json:"usage"`
}

func completionFromOpenAIResponse(response openAIResponseEnvelope) CompletionState {
	state := CompletionState{StopReason: strings.TrimSpace(response.Status)}
	if response.IncompleteDetails != nil {
		state.IncompleteReason = strings.TrimSpace(response.IncompleteDetails.Reason)
	}
	state.OutputTruncated = strings.EqualFold(state.StopReason, "incomplete") &&
		(strings.EqualFold(state.IncompleteReason, "max_output_tokens") || strings.EqualFold(state.IncompleteReason, "max_tokens"))
	return state
}

func (c *Client) createOpenAIResponse(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions) (AssistantMessage, error) {
	payload, err := buildOpenAIResponseRequest(model, messages, definitions, options, false)
	if err != nil {
		return AssistantMessage{}, err
	}
	response, err := c.doJSONRequest(ctx, c.baseURL+"/responses", payload, false, "responses request")
	if err != nil {
		return AssistantMessage{}, err
	}
	defer response.Body.Close()

	var decoded openAIResponseEnvelope
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return AssistantMessage{}, err
	}
	return assistantFromOpenAIResponse(decoded), nil
}

func (c *Client) createOpenAIResponseStream(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, onChunk func(string)) (AssistantMessage, error) {
	payload, err := buildOpenAIResponseRequest(model, messages, definitions, ChatCompletionOptions{}, true)
	if err != nil {
		return AssistantMessage{}, err
	}
	response, err := c.doJSONRequest(ctx, c.baseURL+"/responses", payload, true, "responses stream request")
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
	completion := CompletionState{}

	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var event struct {
			Type        string                 `json:"type"`
			Delta       string                 `json:"delta"`
			OutputIndex int                    `json:"output_index"`
			Item        openAIResponseOutput   `json:"item"`
			Response    openAIResponseEnvelope `json:"response"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}
		switch event.Type {
		case "response.output_text.delta":
			content.WriteString(event.Delta)
			if onChunk != nil {
				onChunk(event.Delta)
			}
		case "response.output_item.added":
			if event.Item.Type == "function_call" {
				calls[event.OutputIndex] = &partialCall{id: firstNonBlank(event.Item.CallID, event.Item.ID), name: event.Item.Name}
			}
		case "response.function_call_arguments.delta":
			call := calls[event.OutputIndex]
			if call == nil {
				call = &partialCall{}
				calls[event.OutputIndex] = call
			}
			call.arguments.WriteString(event.Delta)
		case "response.output_item.done":
			if event.Item.Type == "function_call" {
				call := calls[event.OutputIndex]
				if call == nil {
					call = &partialCall{}
					calls[event.OutputIndex] = call
				}
				call.id = firstNonBlank(event.Item.CallID, event.Item.ID, call.id)
				call.name = firstNonBlank(event.Item.Name, call.name)
				if event.Item.Arguments != "" {
					call.arguments.Reset()
					call.arguments.WriteString(event.Item.Arguments)
				}
			}
		case "response.completed", "response.incomplete":
			usage = tokenUsageFromOpenAIResponse(event.Response.Usage)
			completion = completionFromOpenAIResponse(event.Response)
			if event.Type == "response.incomplete" && completion.StopReason == "" {
				completion.StopReason = "incomplete"
				completion.OutputTruncated = strings.EqualFold(completion.IncompleteReason, "max_output_tokens") || strings.EqualFold(completion.IncompleteReason, "max_tokens")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return AssistantMessage{}, fmt.Errorf("reading responses stream: %w", err)
	}

	message := AssistantMessage{Role: "assistant", Content: content.String(), Completion: completion, Usage: usage}
	indexes := make([]int, 0, len(calls))
	for index := range calls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		call := calls[index]
		message.ToolCalls = append(message.ToolCalls, ToolCall{
			ID: call.id, Type: "function",
			Function: FunctionCall{Name: call.name, Arguments: call.arguments.String()},
		})
	}
	message.normalizeInlineToolCalls()
	return message, nil
}

func buildOpenAIResponseRequest(model string, messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions, stream bool) (openAIResponseRequest, error) {
	request := openAIResponseRequest{Model: model, Input: openAIResponseInput(messages), Stream: stream}
	for _, definition := range definitions {
		schema, err := toolSchema(definition)
		if err != nil {
			return openAIResponseRequest{}, err
		}
		request.Tools = append(request.Tools, openAIResponseTool{
			Type: "function", Name: definition.Function.Name,
			Description: definition.Function.Description, Parameters: schema,
		})
	}
	if len(options.ResponseFormat) > 0 {
		request.Text = map[string]any{"format": options.ResponseFormat}
	}
	return request, nil
}

func openAIResponseInput(messages []map[string]any) []any {
	input := make([]any, 0, len(messages))
	for _, message := range messages {
		role, _ := message["role"].(string)
		switch role {
		case "tool":
			callID, _ := message["tool_call_id"].(string)
			input = append(input, map[string]any{
				"type": "function_call_output", "call_id": callID, "output": contentText(message["content"]),
			})
		case "assistant":
			if text := contentText(message["content"]); strings.TrimSpace(text) != "" {
				input = append(input, map[string]any{"role": "assistant", "content": text})
			}
			for _, call := range messageToolCalls(message) {
				input = append(input, map[string]any{
					"type": "function_call", "call_id": call.ID,
					"name": call.Function.Name, "arguments": call.Function.Arguments,
				})
			}
		case "system", "developer", "user":
			input = append(input, map[string]any{"role": role, "content": openAIResponseContent(message["content"])})
		}
	}
	return input
}

func openAIResponseContent(content any) any {
	parts, ok := content.([]any)
	if !ok {
		return content
	}
	converted := make([]any, 0, len(parts))
	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok {
			continue
		}
		switch partType, _ := part["type"].(string); partType {
		case "text", "input_text":
			converted = append(converted, map[string]any{"type": "input_text", "text": contentText([]any{part})})
		case "image_url", "input_image":
			url := imageURLFromPart(part)
			if url == "" {
				url, _ = part["image_url"].(string)
			}
			if url != "" {
				converted = append(converted, map[string]any{"type": "input_image", "image_url": url})
			}
		}
	}
	return converted
}

func assistantFromOpenAIResponse(response openAIResponseEnvelope) AssistantMessage {
	var content strings.Builder
	message := AssistantMessage{Role: "assistant", Completion: completionFromOpenAIResponse(response), Usage: tokenUsageFromOpenAIResponse(response.Usage)}
	for _, output := range response.Output {
		switch output.Type {
		case "message":
			for _, part := range output.Content {
				if part.Type == "output_text" || part.Type == "text" {
					content.WriteString(part.Text)
				}
			}
		case "function_call":
			message.ToolCalls = append(message.ToolCalls, ToolCall{
				ID: firstNonBlank(output.CallID, output.ID), Type: "function",
				Function: FunctionCall{Name: output.Name, Arguments: output.Arguments},
			})
		}
	}
	message.Content = content.String()
	message.normalizeInlineToolCalls()
	return message
}

func tokenUsageFromOpenAIResponse(usage openAIResponseUsage) TokenUsage {
	total := usage.TotalTokens
	if total == 0 {
		total = usage.InputTokens + usage.OutputTokens
	}
	return TokenUsage{PromptTokens: usage.InputTokens, CompletionTokens: usage.OutputTokens, TotalTokens: total}
}

func (c *Client) doJSONRequest(ctx context.Context, url string, payload any, stream bool, label string) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if stream {
		request.Header.Set("Accept", "text/event-stream")
	}
	if c.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.apiKey)
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
		return nil, fmt.Errorf("%s failed: %s", label, response.Status)
	}
	return nil, fmt.Errorf("%s failed: %s: %s", label, response.Status, strings.TrimSpace(string(errorPayload)))
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
