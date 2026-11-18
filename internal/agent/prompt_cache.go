package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"bqagent/internal/tools"
)

func buildPromptCacheOptions(apiType APIType, model string, prompt PromptSnapshot, definitions []tools.Definition, options ChatCompletionOptions) PromptCacheOptions {
	if prompt.Stable == "" || prompt.StableHash == "" {
		return PromptCacheOptions{}
	}
	shapeHash := requestShapeHash(definitions, options)
	mode := "implicit"
	if apiType == APITypeAnthropic {
		mode = "automatic+explicit"
	}
	return PromptCacheOptions{
		Enabled:            true,
		Key:                promptCacheKey(model, prompt.StableHash, shapeHash),
		StableMessageCount: 1,
		StableHash:         prompt.StableHash,
		RequestShapeHash:   shapeHash,
		Mode:               mode,
		ExplicitBreakpoint: apiType == APITypeAnthropic,
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

func openAIPromptCacheKey(options PromptCacheOptions) string {
	if !options.Enabled {
		return ""
	}
	return options.Key
}

func cloneMessageMap(message map[string]any) map[string]any {
	cloned := make(map[string]any, len(message))
	for key, value := range message {
		cloned[key] = value
	}
	return cloned
}
