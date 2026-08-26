package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"bqagent/internal/tools"
)

type openAIInputTokenCountResponse struct {
	InputTokens int `json:"input_tokens"`
}

type anthropicInputTokenCountRequest struct {
	Model        string                 `json:"model"`
	System       string                 `json:"system,omitempty"`
	Messages     []anthropicMessage     `json:"messages"`
	Tools        []anthropicTool        `json:"tools,omitempty"`
	Thinking     *anthropicThinking     `json:"thinking,omitempty"`
	OutputConfig *anthropicOutputConfig `json:"output_config,omitempty"`
}

type anthropicInputTokenCountResponse struct {
	InputTokens int `json:"input_tokens"`
}

// CountInputTokens dispatches to a provider-specific implementation. Keeping
// this capability separate from generation lets additional providers add their
// own exact count endpoint without changing the agent loop.
func (c *Client) CountInputTokens(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions) (InputTokenCount, error) {
	switch c.apiType {
	case APITypeOpenAIResponse:
		return c.countOpenAIResponseInputTokens(ctx, model, messages, definitions, options)
	case APITypeAnthropic:
		return c.countAnthropicInputTokens(ctx, model, messages, definitions, options)
	case APITypeOpenAI:
		return InputTokenCount{}, ErrInputTokenCountUnsupported
	default:
		return InputTokenCount{}, fmt.Errorf("%w: %s", ErrInputTokenCountUnsupported, c.apiType)
	}
}

func (c *Client) countOpenAIResponseInputTokens(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions) (InputTokenCount, error) {
	payload, err := buildOpenAIResponseRequest(model, messages, definitions, options, false)
	if err != nil {
		return InputTokenCount{}, &ProviderError{Category: ProviderErrorRequest, Message: "build responses input token count request", Cause: err}
	}
	response, err := c.doJSONRequest(ctx, c.baseURL+"/responses/input_tokens", payload, false, "responses input token count")
	if err != nil {
		return InputTokenCount{}, err
	}
	defer response.Body.Close()

	var decoded openAIInputTokenCountResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return InputTokenCount{}, providerProtocolError("decode responses input token count", err)
	}
	if decoded.InputTokens <= 0 {
		return InputTokenCount{}, providerProtocolError("decode responses input token count", fmt.Errorf("invalid input_tokens %d", decoded.InputTokens))
	}
	return InputTokenCount{Tokens: decoded.InputTokens, Exact: true}, nil
}

func (c *Client) countAnthropicInputTokens(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions) (InputTokenCount, error) {
	messageRequest, err := buildAnthropicRequest(model, messages, definitions, options, false)
	if err != nil {
		return InputTokenCount{}, &ProviderError{Category: ProviderErrorRequest, Message: "build anthropic input token count request", Cause: err}
	}
	payload := anthropicInputTokenCountRequest{
		Model:        messageRequest.Model,
		System:       messageRequest.System,
		Messages:     messageRequest.Messages,
		Tools:        messageRequest.Tools,
		Thinking:     messageRequest.Thinking,
		OutputConfig: messageRequest.OutputConfig,
	}
	response, err := c.doAnthropicJSONRequest(ctx, c.baseURL+"/messages/count_tokens", payload, false, "anthropic input token count request")
	if err != nil {
		return InputTokenCount{}, err
	}
	defer response.Body.Close()

	var decoded anthropicInputTokenCountResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return InputTokenCount{}, providerProtocolError("decode anthropic input token count", err)
	}
	if decoded.InputTokens <= 0 {
		return InputTokenCount{}, providerProtocolError("decode anthropic input token count", fmt.Errorf("invalid input_tokens %d", decoded.InputTokens))
	}
	return InputTokenCount{Tokens: decoded.InputTokens, Exact: true}, nil
}
