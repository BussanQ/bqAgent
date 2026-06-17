package runtime

import (
	"encoding/base64"
	"errors"
	"log"
	"os"
	"strings"

	"bqagent/internal/agent"
	"bqagent/internal/session"
)

type Conversation struct {
	Session  *session.Session
	Messages []map[string]any
}

func PrepareConversation(store *session.Store, sessionID string, createOptions *session.CreateOptions, systemPrompt string) (*Conversation, error) {
	var (
		savedSession *session.Session
		err          error
	)

	sessionID = strings.TrimSpace(sessionID)
	switch {
	case sessionID != "":
		savedSession, err = store.Open(sessionID)
		if err != nil && errors.Is(err, os.ErrNotExist) && createOptions != nil {
			log.Printf("session %s not found on disk; starting fresh session", sessionID)
			savedSession, err = store.Create(*createOptions)
		}
	case createOptions != nil:
		savedSession, err = store.Create(*createOptions)
	}
	if err != nil {
		return nil, err
	}

	messages := []map[string]any{}
	if savedSession != nil {
		if err := savedSession.MarkRunning(); err != nil {
			return nil, err
		}
		messages, err = savedSession.LoadMessages()
		if err != nil {
			// Best effort; the load error below is the one that matters.
			_ = savedSession.MarkFailed(err)
			return nil, err
		}
		if checkpoint, checkpointErr := savedSession.LoadCheckpoint(); checkpointErr == nil {
			messages = restoreCheckpointMessages(messages, checkpoint, systemPrompt)
		}
	}

	conversation := &Conversation{
		Session:  savedSession,
		Messages: messages,
	}
	if err := conversation.EnsureSystemMessage(systemPrompt); err != nil {
		if savedSession != nil {
			// Best effort; the primary error is returned below.
			_ = savedSession.MarkFailed(err)
		}
		return nil, err
	}
	return conversation, nil
}

func (conversation *Conversation) EnsureSystemMessage(systemPrompt string) error {
	systemMessage := map[string]any{"role": "system", "content": systemPrompt}
	if len(conversation.Messages) == 0 {
		conversation.Messages = append(conversation.Messages, systemMessage)
		if conversation.Session != nil {
			return conversation.Session.RecordMessage(systemMessage)
		}
		return nil
	}

	role, _ := conversation.Messages[0]["role"].(string)
	content, _ := conversation.Messages[0]["content"].(string)
	if role == "system" {
		if content == systemPrompt {
			return nil
		}
		conversation.Messages[0] = systemMessage
	} else {
		conversation.Messages = append([]map[string]any{systemMessage}, conversation.Messages...)
	}
	if conversation.Session != nil {
		return conversation.Session.RewriteMessages(conversation.Messages)
	}
	return nil
}

func restoreCheckpointMessages(messages []map[string]any, checkpoint session.ContextCheckpoint, systemPrompt string) []map[string]any {
	if strings.TrimSpace(checkpoint.Summary) == "" || len(checkpoint.TailMessages) == 0 {
		return messages
	}
	if strings.TrimSpace(checkpoint.SystemPrompt) != "" && checkpoint.SystemPrompt != systemPrompt {
		return messages
	}

	restored := make([]map[string]any, 0, len(checkpoint.TailMessages)+2)
	if len(messages) > 0 {
		if role, _ := messages[0]["role"].(string); role == "system" {
			restored = append(restored, messages[0])
		}
	}
	restored = append(restored, map[string]any{
		"role":    "assistant",
		"content": agent.EarlierConversationSummaryPrefix + checkpoint.Summary,
	})
	for _, message := range checkpoint.TailMessages {
		copyMessage := make(map[string]any, len(message))
		for key, value := range message {
			copyMessage[key] = value
		}
		restored = append(restored, copyMessage)
	}
	return restored
}

func (conversation *Conversation) AddUserMessage(content string) error {
	userMessage := map[string]any{"role": "user", "content": content}
	conversation.Messages = append(conversation.Messages, userMessage)
	if conversation.Session != nil {
		return conversation.Session.RecordMessage(userMessage)
	}
	return nil
}

// AddUserMessageWithImages appends a user message that may carry images. With no
// images the content stays a plain string (identical to AddUserMessage); with
// images the content becomes an OpenAI multimodal array of text + image_url
// parts, each image inlined as a base64 data URI. The full message (including the
// base64 payload) is recorded to the transcript so resume reconstructs it.
func (conversation *Conversation) AddUserMessageWithImages(content string, images []agent.ImageAttachment) error {
	userMessage := map[string]any{"role": "user", "content": userMessageContent(content, images)}
	conversation.Messages = append(conversation.Messages, userMessage)
	if conversation.Session != nil {
		return conversation.Session.RecordMessage(userMessage)
	}
	return nil
}

func userMessageContent(content string, images []agent.ImageAttachment) any {
	if len(images) == 0 {
		return content
	}
	parts := make([]any, 0, len(images)+1)
	if strings.TrimSpace(content) != "" {
		parts = append(parts, map[string]any{"type": "text", "text": content})
	}
	for _, image := range images {
		if len(image.Data) == 0 {
			continue
		}
		mimeType := strings.TrimSpace(image.MIMEType)
		if mimeType == "" {
			mimeType = "image/jpeg"
		}
		dataURI := "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(image.Data)
		parts = append(parts, map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": dataURI},
		})
	}
	if len(parts) == 0 {
		return content
	}
	return parts
}

func (conversation *Conversation) Recorder() agent.MessageRecorder {
	if conversation == nil || conversation.Session == nil {
		return nil
	}
	return conversation.Session
}

func (conversation *Conversation) MarkCompleted() error {
	if conversation == nil || conversation.Session == nil {
		return nil
	}
	return conversation.Session.MarkCompleted()
}

func (conversation *Conversation) MarkFailed(err error) error {
	if conversation == nil || conversation.Session == nil {
		return nil
	}
	return conversation.Session.MarkFailed(err)
}
