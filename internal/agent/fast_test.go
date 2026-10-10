package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestFastServiceTierRequests(t *testing.T) {
	for _, api := range []APIType{APITypeOpenAI, APITypeOpenAIResponse} {
		for _, stream := range []bool{false, true} {
			for _, tier := range []string{"", "priority"} {
				t.Run(fmt.Sprintf("%s/stream=%v/tier=%s", api, stream, tier), func(t *testing.T) {
					calls := 0
					transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
						calls++
						var body map[string]any
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Fatal(err)
						}
						got, present := body["service_tier"]
						if tier == "" && present || tier != "" && got != tier {
							t.Fatalf("service_tier=%v present=%v", got, present)
						}
						payload := `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`
						if api == APITypeOpenAIResponse {
							payload = `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`
						}
						if stream {
							payload = "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
							if api == APITypeOpenAIResponse {
								payload = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"
							}
						}
						return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}, nil
					})
					client := NewClientWithAPIType("", "http://test.invalid", api, &http.Client{Transport: transport})
					options := ChatCompletionOptions{ServiceTier: tier}
					messages := []map[string]any{{"role": "user", "content": "hi"}}
					var result AssistantMessage
					var err error
					if stream {
						result, err = client.CreateChatCompletionStreamWithOptions(context.Background(), "test", messages, nil, options, nil)
					} else {
						result, err = client.CreateChatCompletionWithOptions(context.Background(), "test", messages, nil, options)
					}
					if err != nil || result.FinalContent() != "ok" || calls != 1 {
						t.Fatalf("result=%v err=%v calls=%d", result, err, calls)
					}
				})
			}
		}
	}
}
