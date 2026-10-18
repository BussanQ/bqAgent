package runtime

import (
	"bqagent/internal/session"
	"os"
	"strings"
	"testing"
)

func TestPrepareConversationRestoresCheckpointAfterIncompleteAppend(t *testing.T) {
	for _, newer := range []bool{false, true} {
		store := session.NewStore(t.TempDir())
		original, err := PrepareConversation(store, "", &session.CreateOptions{Task: "hello", Chat: true}, "system prompt")
		if err != nil {
			t.Fatal(err)
		}
		if err := original.AddUserMessage("old detail"); err != nil {
			t.Fatal(err)
		}
		if err := original.Session.SaveCheckpointSummary("checkpoint summary", []map[string]any{{"role": "user", "content": "checkpoint tail"}}, "system prompt"); err != nil {
			t.Fatal(err)
		}
		if newer {
			if err := original.AddUserMessage("newer user input"); err != nil {
				t.Fatal(err)
			}
		}
		file, err := os.OpenFile(original.Session.MessagesPath(), os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString(`{"role":"assistant","content":"interrupted`); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		restored, err := PrepareConversation(store, original.Session.ID(), nil, "system prompt")
		if err != nil {
			t.Fatal(err)
		}
		var contents []string
		for _, message := range restored.Messages {
			if text, ok := message["content"].(string); ok {
				contents = append(contents, text)
			}
		}
		joined := strings.Join(contents, "\n")
		if newer {
			if !strings.Contains(joined, "newer user input") || strings.Contains(joined, "checkpoint tail") {
				t.Fatalf("stale checkpoint restored: %s", joined)
			}
		} else if !strings.Contains(joined, "checkpoint summary") || !strings.Contains(joined, "checkpoint tail") {
			t.Fatalf("checkpoint not restored: %s", joined)
		}
	}
}
