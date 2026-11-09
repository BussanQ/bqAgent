package agent

import (
	"bqagent/internal/transcript"
	"encoding/json"
	"strings"
	"testing"
)

func TestExternalSummaryIsNotSentAsConversationContent(t *testing.T) {
	const summary = "visible-only-in-summary"
	messages := []map[string]any{
		{"role": "user", "content": "question"},
		{"role": "assistant", "name": "codex", "content": "final answer", transcript.ExternalHistoryKey: transcript.ExternalHistory{
			ID: "s", Parts: []transcript.Part{{Kind: "summary", ID: "r/0", Text: summary, Summary: &transcript.SummarySection{Item: "r", Ended: true}}},
		}},
	}
	_, anthropic, _ := anthropicMessages(messages, PromptCacheOptions{})
	for name, request := range map[string]any{
		"chat":      openAIChatMessagesWithCache(messages, PromptCacheOptions{}),
		"responses": openAIResponseInput(messages, PromptCacheOptions{}),
		"anthropic": anthropic,
	} {
		encoded, err := json.Marshal(request)
		if err != nil || strings.Contains(string(encoded), summary) || !strings.Contains(string(encoded), "final answer") {
			t.Fatalf("%s: %s, %v", name, encoded, err)
		}
	}
}

func TestChatRequestsOmitExternalTimelineWithoutMutatingHistory(t *testing.T) {
	root := map[string]any{"role": "user", "content": "same", transcript.SubmissionKey: "root-id"}
	user := openAIChatMessagesWithCache([]map[string]any{root}, PromptCacheOptions{})
	if user[0][transcript.SubmissionKey] != nil || root[transcript.SubmissionKey] != "root-id" || user[0]["content"] != "same" {
		t.Fatal(user, root)
	}
	message := map[string]any{"role": "assistant", "name": "pi", "content": "final", transcript.ExternalHistoryKey: transcript.ExternalHistory{ID: "s", Parts: []transcript.Part{{Kind: "text", ID: "i", Text: "private commentary"}}}, ExternalToolsMessageKey: []HistoryTool{{Name: "Edit", Result: "private log", Edits: &transcript.FileEdits{Files: []transcript.EditFile{{Path: "file", Lines: []transcript.EditLine{{Kind: "add", Text: "private diff"}}}}}}}}
	wire := openAIChatMessagesWithCache([]map[string]any{message}, PromptCacheOptions{})
	if wire[0][transcript.ExternalHistoryKey] != nil || message[transcript.ExternalHistoryKey] == nil {
		t.Fatal("presentation metadata leaked or was mutated")
	}
	if _, ok := wire[0][ExternalToolsMessageKey]; ok || message[ExternalToolsMessageKey] == nil || wire[0]["content"] != "final" {
		t.Fatalf("request=%v history=%v", wire, message)
	}
}
