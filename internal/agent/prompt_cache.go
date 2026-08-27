package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"bqagent/internal/tools"
)

var gptVersionPattern = regexp.MustCompile(`(?i)(?:^|[/_-])gpt[-_](\d+)(?:\.(\d+))?`)

func buildPromptCacheOptions(apiType APIType, model string, prompt PromptSnapshot, definitions []tools.Definition, options ChatCompletionOptions) PromptCacheOptions {
	if prompt.Stable == "" || prompt.StableHash == "" {
		return PromptCacheOptions{}
	}
	shapeHash := requestShapeHash(definitions, options)
	explicit := apiType == APITypeAnthropic || modelSupportsExplicitPromptCache(model)
	mode := "implicit"
	if apiType == APITypeAnthropic {
		mode = "automatic+explicit"
	} else if explicit {
		mode = "implicit+explicit"
	}
	return PromptCacheOptions{
		Enabled:            true,
		Key:                promptCacheKey(model, prompt.StableHash, shapeHash),
		StableMessageCount: 1,
		StableHash:         prompt.StableHash,
		RequestShapeHash:   shapeHash,
		Mode:               mode,
		ExplicitBreakpoint: explicit,
	}
}

func promptCacheKey(model, stableHash, shapeHash string) string {
	return fmt.Sprintf("bq1:%s:%s:%s", hashedValuePrefix(model, 8), digestPrefix(stableHash, 16), digestPrefix(shapeHash, 16))
}

func hashedValuePrefix(value string, length int) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return truncateHash(hex.EncodeToString(sum[:]), length)
}

func digestPrefix(value string, length int) string {
	value = strings.TrimSpace(value)
	if decoded, err := hex.DecodeString(value); err == nil && len(decoded) > 0 {
		value = strings.ToLower(value)
	} else {
		return hashedValuePrefix(value, length)
	}
	return truncateHash(value, length)
}

func truncateHash(value string, length int) string {
	if len(value) > length {
		return value[:length]
	}
	return value
}

func modelSupportsExplicitPromptCache(model string) bool {
	match := gptVersionPattern.FindStringSubmatch(strings.ToLower(strings.TrimSpace(model)))
	if len(match) == 0 {
		return false
	}
	major, majorErr := strconv.Atoi(match[1])
	minor := 0
	minorErr := error(nil)
	if len(match) > 2 && match[2] != "" {
		minor, minorErr = strconv.Atoi(match[2])
	}
	if majorErr != nil || minorErr != nil {
		return false
	}
	return major > 5 || major == 5 && minor >= 6
}

func openAIPromptCacheKey(options PromptCacheOptions) string {
	if !options.Enabled {
		return ""
	}
	return options.Key
}

func openAIPromptCacheRequest(options PromptCacheOptions) *openAIPromptCacheRequestOptions {
	if !options.Enabled || !options.ExplicitBreakpoint {
		return nil
	}
	return &openAIPromptCacheRequestOptions{Mode: "implicit"}
}

func openAIChatMessagesWithCache(messages []map[string]any, options PromptCacheOptions) []map[string]any {
	if !options.Enabled || !options.ExplicitBreakpoint || options.StableMessageCount <= 0 || len(messages) < options.StableMessageCount {
		return messages
	}
	index := options.StableMessageCount - 1
	result := append([]map[string]any(nil), messages...)
	message := cloneMessageMap(messages[index])
	message["content"] = openAITextContentWithBreakpoint(message["content"], "text")
	result[index] = message
	return result
}

func openAITextContentWithBreakpoint(content any, textType string) []any {
	breakpoint := map[string]any{"mode": "explicit"}
	if text, ok := content.(string); ok {
		return []any{map[string]any{
			"type": textType, "text": text,
			"prompt_cache_breakpoint": breakpoint,
		}}
	}
	parts, ok := content.([]any)
	if !ok || len(parts) == 0 {
		return []any{map[string]any{
			"type": textType, "text": contentText(content),
			"prompt_cache_breakpoint": breakpoint,
		}}
	}
	cloned := cloneContentParts(parts)
	for index := len(cloned) - 1; index >= 0; index-- {
		part, ok := cloned[index].(map[string]any)
		if !ok {
			continue
		}
		part["prompt_cache_breakpoint"] = breakpoint
		return cloned
	}
	return cloned
}

func cloneMessageMap(message map[string]any) map[string]any {
	cloned := make(map[string]any, len(message))
	for key, value := range message {
		cloned[key] = value
	}
	return cloned
}
