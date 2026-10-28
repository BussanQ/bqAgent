package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"bqagent/internal/tools"
)

func TestDisableToolsRequestPayloads(t *testing.T) {
	definitions := []tools.Definition{{Type: "function", Function: tools.FunctionDefinition{
		Name: "lookup", Description: "d", Parameters: tools.JSONSchema{Type: "object"},
	}}}
	for _, apiType := range []APIType{APITypeOpenAI, APITypeOpenAIResponse, APITypeAnthropic} {
		for _, stream := range []bool{false, true} {
			for _, disable := range []bool{false, true} {
				name := string(apiType) + map[bool]string{false: "_nonstream", true: "_stream"}[stream] + map[bool]string{false: "_enabled", true: "_disabled"}[disable]
				t.Run(name, func(t *testing.T) {
					var body map[string]any
					client := newTestResilientClient(apiType, "https://tool-choice.test", &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
						raw, _ := io.ReadAll(request.Body)
						if err := json.Unmarshal(raw, &body); err != nil {
							t.Fatal(err)
						}
						payload := providerJSONSuccess(apiType, "ok")
						if stream {
							payload = providerStreamSuccess(apiType, "ok")
						}
						return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}, nil
					})}, 0)
					options := ChatCompletionOptions{DisableTools: disable}
					messages := []map[string]any{{"role": "user", "content": "hi"}}
					var err error
					if stream {
						_, err = client.CreateChatCompletionStreamWithOptions(context.Background(), "m", messages, definitions, options, func(string) {})
					} else {
						_, err = client.CreateChatCompletionWithOptions(context.Background(), "m", messages, definitions, options)
					}
					if err != nil {
						t.Fatal(err)
					}
					choice, hasChoice := body["tool_choice"]
					_, hasTools := body["tools"]
					if !disable {
						if hasChoice || !hasTools {
							t.Fatalf("normal request changed: %v", body)
						}
						return
					}
					if hasTools {
						t.Fatalf("tools sent while disabled: %v", body["tools"])
					}
					if apiType == APITypeAnthropic {
						if m, _ := choice.(map[string]any); m["type"] != "none" {
							t.Fatalf("tool_choice = %#v", choice)
						}
					} else if choice != "none" {
						t.Fatalf("tool_choice = %#v", choice)
					}
				})
			}
		}
	}
}

func TestDisableToolsTokenCountPayloads(t *testing.T) {
	definitions := []tools.Definition{{Type: "function", Function: tools.FunctionDefinition{Name: "lookup", Parameters: tools.JSONSchema{Type: "object"}}}}
	for _, apiType := range []APIType{APITypeOpenAIResponse, APITypeAnthropic} {
		t.Run(string(apiType), func(t *testing.T) {
			var body map[string]any
			client := newTestResilientClient(apiType, "https://tool-choice.test", &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				raw, _ := io.ReadAll(request.Body)
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Fatal(err)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"input_tokens":7}`))}, nil
			})}, 0)
			if _, err := client.CountInputTokens(context.Background(), "m", []map[string]any{{"role": "user", "content": "hi"}}, definitions, ChatCompletionOptions{DisableTools: true}); err != nil {
				t.Fatal(err)
			}
			if _, hasTools := body["tools"]; hasTools || body["tool_choice"] == nil {
				t.Fatalf("token count payload inconsistent: %v", body)
			}
		})
	}
}
