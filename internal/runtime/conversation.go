package runtime

import (
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
