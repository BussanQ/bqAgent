package agent

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bqagent/internal/tools"
)

func TestRunConversationAttachesReadFileImagesAfterToolResults(t *testing.T) {
	root := t.TempDir()
	pngData := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	if err := os.WriteFile(filepath.Join(root, "shot.png"), pngData, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	client := &stubClient{responses: []AssistantMessage{
		{ToolCalls: []ToolCall{
			{ID: "img", Function: FunctionCall{Name: "read_file", Arguments: `{"path":"shot.png"}`}},
			{ID: "txt", Function: FunctionCall{Name: "read_file", Arguments: `{"path":"notes.txt"}`}},
		}},
		{Content: "done"},
	}}
	app := NewWithOptions(client, "", Options{
		Context:   ContextConfig{Enabled: false},
		Functions: map[string]tools.Function{"read_file": tools.ReadFileFromRoot(root)},
	})

	_, history, err := app.RunConversationTurn(context.Background(), []map[string]any{{"role": "user", "content": "check the screenshot"}}, 4)
	if err != nil {
		t.Fatalf("RunConversationTurn: %v", err)
	}
	if len(client.messages) != 2 {
		t.Fatalf("model calls = %d, want 2", len(client.messages))
	}
	sent := client.messages[1]
	var roles []string
	for _, message := range sent[len(sent)-4:] {
		role, _ := message["role"].(string)
		roles = append(roles, role)
	}
	if want := []string{"assistant", "tool", "tool", "user"}; !equalStrings(roles, want) {
		t.Fatalf("tail roles = %v, want %v", roles, want)
	}
	if text, _ := sent[len(sent)-3]["content"].(string); text == "" || text[0] == 'E' {
		t.Fatalf("image tool result = %q", text)
	}
	parts, ok := sent[len(sent)-1]["content"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("image message content = %#v", sent[len(sent)-1]["content"])
	}
	imagePart, _ := parts[1].(map[string]any)
	url, _ := imagePart["image_url"].(map[string]any)
	if want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngData); url["url"] != want {
		t.Fatalf("image url = %v, want %s", url["url"], want)
	}
	if !IsToolImagesMessage(history[len(history)-2]) {
		t.Fatalf("image message not kept in history: %#v", history[len(history)-2])
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func toolImageHistory() []map[string]any {
	image := map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAAA"}}
	return []map[string]any{
		{"role": "user", "content": "first question"},
		{"role": "assistant", "content": "first answer"},
		{"role": "user", "content": "check the screenshot"},
		{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "img", "type": "function", "function": map[string]any{"name": "read_file", "arguments": `{"path":"shot.png"}`}}}},
		{"role": "tool", "tool_call_id": "img", "content": "Image loaded: shot.png"},
		{"role": "user", "content": []any{map[string]any{"type": "text", "text": "Image from read_file: shot.png"}, image}, ToolImagesMessageKey: true},
	}
}

func TestToolImagesMessageIsNotCountedAsUserTurn(t *testing.T) {
	messages := toolImageHistory()
	if start := safeTailStart(messages, 1); start != 2 {
		t.Fatalf("safeTailStart keep=1 = %d, want 2 (the real user turn)", start)
	}
	if start := safeTailStart(messages, 2); start != 0 {
		t.Fatalf("safeTailStart keep=2 = %d, want 0", start)
	}
	groups := messageTailGroups(messages, 3)
	if len(groups) != 1 || len(groups[0].messages) != 3 {
		t.Fatalf("tool batch group = %#v, want assistant+tool+images", groups)
	}
}

func TestHardPruneReservesBudgetForRealUserInputNotToolImages(t *testing.T) {
	messages := toolImageHistory()
	userInput := "check the screenshot " + strings.Repeat("keep ", 50)
	messages[2]["content"] = userInput
	messages[4]["content"] = "Image loaded: shot.png " + strings.Repeat("detail ", 400)
	parts := messages[5]["content"].([]any)
	parts[1] = map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + strings.Repeat("A", 400_000)}}
	budget := estimateMessagesTokens(messages[2:3]) + 120
	result := hardPruneMessagesToBudget(messages, budget)
	foundUser := false
	for index, message := range result {
		if text, _ := message["content"].(string); text == userInput {
			foundUser = true
		}
		if IsToolImagesMessage(message) {
			if index == 0 {
				t.Fatal("tool images message kept without its tool batch")
			}
			if role, _ := result[index-1]["role"].(string); role != "tool" {
				t.Fatalf("tool images message follows %q, want tool", role)
			}
		}
	}
	if !foundUser {
		t.Fatalf("latest real user input was pruned or clipped: %#v", result)
	}
}

func TestChatRequestStripsToolImagesMarker(t *testing.T) {
	stripped := chatMessagesWithoutResponseMetadata(toolImageHistory())
	if _, ok := stripped[len(stripped)-1][ToolImagesMessageKey]; ok {
		t.Fatal("tool_images marker sent to provider")
	}
}
