package agent

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"bqagent/internal/tools"
)

func toolSchema(definition tools.Definition) (json.RawMessage, error) {
	if len(definition.Function.RawParameters) > 0 {
		return append(json.RawMessage(nil), definition.Function.RawParameters...), nil
	}
	return json.Marshal(definition.Function.Parameters)
}

func messageToolCalls(message map[string]any) []ToolCall {
	raw, ok := message["tool_calls"]
	if !ok || raw == nil {
		return nil
	}
	if calls, ok := raw.([]ToolCall); ok {
		return calls
	}
	payload, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var calls []ToolCall
	if err := json.Unmarshal(payload, &calls); err != nil {
		return nil
	}
	return calls
}

func contentText(content any) string {
	switch value := content.(type) {
	case nil:
		return ""
	case string:
		return value
	case []any:
		var builder strings.Builder
		for _, part := range value {
			partMap, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if text, _ := partMap["text"].(string); text != "" {
				builder.WriteString(text)
			}
		}
		return builder.String()
	default:
		return fmt.Sprint(value)
	}
}

func parseToolArguments(raw string) any {
	var input any
	if err := json.Unmarshal([]byte(raw), &input); err == nil {
		return input
	}
	return map[string]any{}
}

func dataURI(raw string) (mediaType, data string, ok bool) {
	if !strings.HasPrefix(raw, "data:") {
		return "", "", false
	}
	header, payload, found := strings.Cut(strings.TrimPrefix(raw, "data:"), ",")
	if !found || !strings.HasSuffix(strings.ToLower(header), ";base64") {
		return "", "", false
	}
	mediaType = strings.TrimSuffix(header, ";base64")
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	if _, err := base64.StdEncoding.DecodeString(payload); err != nil {
		return "", "", false
	}
	return mediaType, payload, true
}

func imageURLFromPart(part map[string]any) string {
	image, ok := part["image_url"]
	if !ok {
		return ""
	}
	switch value := image.(type) {
	case string:
		return value
	case map[string]any:
		url, _ := value["url"].(string)
		return url
	default:
		return ""
	}
}
