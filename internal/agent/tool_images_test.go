package agent

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
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
	if role, _ := history[len(history)-2]["role"].(string); role != "user" {
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
