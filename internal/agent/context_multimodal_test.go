package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPruneMultimodalTextPreservesLatestInput(t *testing.T) {
	for _, text := range []string{strings.Repeat("important input ", 500), strings.Repeat("请保留用户的最新指令。", 500)} {
		messages := []map[string]any{{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": text},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + strings.Repeat("A", 4000)}},
			map[string]any{"type": "text", "text": "keep ending"},
		}}}
		before, _ := json.Marshal(messages)
		result := hardPruneMessagesToBudget(messages, 160)
		if len(result) != 1 || result[0]["role"] != "user" {
			t.Fatalf("latest input lost: %#v", result)
		}
		if got := estimateMessagesTokens(result); got > 160 {
			t.Fatalf("tokens=%d", got)
		}
		parts := result[0]["content"].([]any)
		if len(parts) != 2 {
			t.Fatalf("parts=%#v", parts)
		}
		clipped := parts[0].(map[string]any)["text"].(string)
		if clipped == "" || len(clipped) >= len(text) || !utf8.ValidString(clipped) {
			t.Fatalf("invalid clipped text: %q", clipped)
		}
		after, _ := json.Marshal(messages)
		if string(before) != string(after) {
			t.Fatal("source history mutated")
		}
	}
}
