package extagent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bqagent/internal/media"
)

func TestNativeImagePromptBlocksOnCodexAndACP(t *testing.T) {
	images := []media.Image{{MIMEType: "image/png", Data: []byte{1, 2, 3}}, {MIMEType: "image/jpeg", Data: []byte{4, 5}}}
	c := &codexAppServerClient{responses: map[string]chan rpcEnvelope{}, done: make(chan struct{})}
	c.stdin = modelWire(func(msg rpcEnvelope) {
		var params struct {
			ThreadID, Model, Effort string
			Input                   []struct{ Type, Text, URL string }
		}
		if json.Unmarshal(msg.Params, &params) != nil || params.ThreadID != "s" || params.Model != "vision" || params.Effort != "high" || len(params.Input) != 3 || params.Input[0].Text != "@question" || params.Input[1].URL != "data:image/png;base64,AQID" || params.Input[2].URL != "data:image/jpeg;base64,BAU=" {
			t.Fatal(string(msg.Params))
		}
		c.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(`{"turn":{"id":"t"}}`)}
		c.notification(rpcEnvelope{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"s","turn":{"id":"t","status":"completed","items":[{"id":"a","type":"agentMessage","text":"ok"}]}}`)})
	})
	if _, err := c.PromptWithOptions(context.Background(), "s", "@question", PromptOptions{Images: images, Model: "vision", Effort: "high"}); err != nil {
		t.Fatal(err)
	}
	a := &stdioACPClient{responses: map[string]chan rpcEnvelope{}, collectors: map[string]*strings.Builder{}}
	a.stdin = modelWire(func(msg rpcEnvelope) {
		var params struct {
			SessionID string
			Prompt    []struct{ Type, MIMEType, Data string }
		}
		if json.Unmarshal(msg.Params, &params) != nil || msg.Method != "session/prompt" || params.SessionID != "s" || len(params.Prompt) != 2 || params.Prompt[0].Type != "image" || params.Prompt[0].MIMEType != "image/png" || params.Prompt[0].Data != "AQID" || params.Prompt[1].Data != "BAU=" {
			t.Fatal(string(msg.Params))
		}
		a.responses[string(msg.ID)] <- rpcEnvelope{}
	})
	if _, err := a.PromptWithOptions(context.Background(), "s", "", PromptOptions{Images: images}); err == nil {
		t.Fatal("sent images without ACP capability")
	}
	a.imagePromptsSupported = true
	if _, err := a.PromptWithOptions(context.Background(), "s", "", PromptOptions{Images: images}); err != nil {
		t.Fatal(err)
	}
}

func TestCLIImagesUsePrivateTemporaryFilesAndClaudeContentBlocks(t *testing.T) {
	images := []media.Image{{MIMEType: "image/png", Data: []byte{1, 2, 3}}, {MIMEType: "image/jpeg", Data: []byte{4, 5}}}
	paths, cleanup, err := cliImageFiles(images)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if len(paths) != 2 {
		t.Fatal(paths)
	}
	for i, path := range paths {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatal(path, err)
		}
		b, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(b, images[i].Data) {
			t.Fatal(path, b, err)
		}
	}
	if filepath.Ext(paths[0]) != ".png" || filepath.Ext(paths[1]) != ".jpg" {
		t.Fatal(paths)
	}
	cleanup()
	if _, err := os.Stat(filepath.Dir(paths[0])); !os.IsNotExist(err) {
		t.Fatal("temporary inputs survived", err)
	}
	input, err := claudeStreamInput("-special\ntext", images)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(input)
	var frame struct {
		Type    string
		Message struct {
			Role    string
			Content []struct {
				Type, Text string
				Source     struct {
					Data string
					MIME string `json:"media_type"`
				}
			}
		}
	}
	if json.Unmarshal(raw, &frame) != nil || frame.Type != "user" || frame.Message.Role != "user" || len(frame.Message.Content) != 3 || frame.Message.Content[0].Text != "-special\ntext" || frame.Message.Content[1].Source.Data != "AQID" || frame.Message.Content[2].Source.MIME != "image/jpeg" {
		t.Fatal(string(raw))
	}
}
