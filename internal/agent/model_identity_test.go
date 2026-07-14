package agent

import (
	"context"
	"strings"
	"testing"
)

func TestEffectiveModelUsesDefaultWhenUnset(t *testing.T) {
	if got := EffectiveModel("  "); got != DefaultModel {
		t.Fatalf("EffectiveModel() = %q, want %q", got, DefaultModel)
	}
}

func TestAppendModelIdentitySystemPromptIsIdempotentAndReplacesStaleValue(t *testing.T) {
	base := "Custom instructions.\n\nCurrent runtime model: old-model (API type: openai). When asked which model is in use, answer with this exact current-run value."
	prompt := AppendModelIdentitySystemPrompt(base, "claude-test", APITypeAnthropic)
	prompt = AppendModelIdentitySystemPrompt(prompt, "claude-test", APITypeAnthropic)

	if !strings.Contains(prompt, "Custom instructions.") {
		t.Fatalf("prompt = %q, want custom instructions", prompt)
	}
	if strings.Contains(prompt, "old-model") {
		t.Fatalf("prompt = %q, contains stale model", prompt)
	}
	identity := "Current runtime model: claude-test (API type: anthropic)."
	if count := strings.Count(prompt, identity); count != 1 {
		t.Fatalf("identity count = %d, want 1 in %q", count, prompt)
	}
}

func TestRunConversationReplacesSystemMessageWithCurrentModelIdentity(t *testing.T) {
	client := &stubClient{responses: []AssistantMessage{{Content: "done"}}}
	app := NewWithOptions(client, "gpt-test", Options{
		APIType: APITypeOpenAIResponse,
		Context: ContextConfig{Enabled: false},
	})

	_, err := app.RunConversation(context.Background(), []map[string]any{
		{"role": "system", "content": "stale prompt"},
		{"role": "user", "content": "which model?"},
	}, 1)
	if err != nil {
		t.Fatalf("RunConversation returned error: %v", err)
	}
	if len(client.messages) != 1 || len(client.messages[0]) == 0 {
		t.Fatalf("captured messages = %#v", client.messages)
	}
	content, _ := client.messages[0][0]["content"].(string)
	if !strings.Contains(content, "Current runtime model: gpt-test (API type: openai-response).") {
		t.Fatalf("system prompt = %q, want current model identity", content)
	}
}
