package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"bqagent/internal/tools"
)

// tokenCountCapabilityTTL bounds how long a 404/405/501 count endpoint stays
// disabled, so a temporary gateway routing error does not disable exact
// counting for the rest of the process.
const tokenCountCapabilityTTL = 30 * time.Minute

// errTokenCountEndpointDisabled reports the request that disabled the count
// endpoint. It matches ErrInputTokenCountUnsupported for fallback, but is
// logged once, unlike later silent cache hits.
type errTokenCountEndpointDisabled struct{ status int }

func (err errTokenCountEndpointDisabled) Error() string {
	return fmt.Sprintf("%s: HTTP %d; disabled for %s", ErrInputTokenCountUnsupported, err.status, tokenCountCapabilityTTL)
}

func (errTokenCountEndpointDisabled) Unwrap() error { return ErrInputTokenCountUnsupported }

type openAIInputTokenCountResponse struct {
	InputTokens int `json:"input_tokens"`
}

type anthropicInputTokenCountRequest struct {
	Model        string                 `json:"model"`
	System       []anthropicSystemBlock `json:"system,omitempty"`
	Messages     []anthropicMessage     `json:"messages"`
	Tools        []anthropicTool        `json:"tools,omitempty"`
	Thinking     *anthropicThinking     `json:"thinking,omitempty"`
	OutputConfig *anthropicOutputConfig `json:"output_config,omitempty"`
}

type anthropicInputTokenCountResponse struct {
	InputTokens int `json:"input_tokens"`
}

func tokenCountEndpointUnsupportedStatus(err error) (int, bool) {
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) {
		return 0, false
	}
	switch providerErr.StatusCode {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return providerErr.StatusCode, true
	}
	return 0, false
}

// CountInputTokens dispatches to a provider-specific implementation. Keeping
// this capability separate from generation lets additional providers add their
// own exact count endpoint without changing the agent loop.
func (c *Client) CountInputTokens(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions) (InputTokenCount, error) {
	if options.PromptCache.Enabled {
		if _, disabled := cacheUnsupportedEndpoints.Load(c.promptCacheCapabilityKey()); disabled {
			options.PromptCache.Enabled = false
		}
	}
	switch c.apiType {
	case APITypeOpenAIResponse, APITypeAnthropic:
		key := strings.TrimSpace(model)
		if expiry, unsupported := c.tokenCountUnsupported.Load(key); unsupported && time.Now().Before(expiry.(time.Time)) {
			return InputTokenCount{}, ErrInputTokenCountUnsupported
		}
		var count InputTokenCount
		var err error
		if c.apiType == APITypeOpenAIResponse {
			count, err = c.countOpenAIResponseInputTokens(ctx, model, messages, definitions, options)
		} else {
			count, err = c.countAnthropicInputTokens(ctx, model, messages, definitions, options)
		}
		if status, ok := tokenCountEndpointUnsupportedStatus(err); ok {
			c.tokenCountUnsupported.Store(key, time.Now().Add(tokenCountCapabilityTTL))
			return InputTokenCount{}, errTokenCountEndpointDisabled{status: status}
		}
		return count, err
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
