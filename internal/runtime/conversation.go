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
	Session             *session.Session
	Messages            []map[string]any
	UsingWorkingContext bool
	Prompt              agent.PromptSnapshot
}

const PromptSchemaVersion = 1

type SessionContextBuilder func() (string, error)

func PrepareConversation(store *session.Store, sessionID string, createOptions *session.CreateOptions, systemPrompt string) (*Conversation, error) {
	stable, sessionContext := splitLegacySystemPrompt(systemPrompt)
	return PrepareConversationWithPrompt(store, sessionID, createOptions, agent.NewFrozenPromptSnapshot(stable, sessionContext), nil)
}

// PrepareConversationWithPrompt restores an existing prompt snapshot when its
// stable hash still matches. The session context builder is deliberately lazy:
// mutable memory is read only for a new snapshot, never on an ordinary resume.
func PrepareConversationWithPrompt(store *session.Store, sessionID string, createOptions *session.CreateOptions, stablePrompt agent.PromptSnapshot, buildSessionContext SessionContextBuilder) (*Conversation, error) {
	var (
		savedSession *session.Session
		err          error
	)
	stablePrompt = agent.NewFrozenPromptSnapshot(stablePrompt.Stable, stablePrompt.SessionContext)

	sessionID = strings.TrimSpace(sessionID)
	switch {
	case sessionID != "":
		if store == nil {
			return nil, errors.New("session store is required when session_id is set")
		}
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
		usingWorkingContext := false
		messages, usingWorkingContext, err = savedSession.LoadResumableMessages()
		if err == nil && usingWorkingContext {
			var migrated bool
			messages, migrated = migrateSyntheticSummaryRoles(messages)
			if migrated {
				err = savedSession.SaveWorkingMessages(messages)
			}
		}
		meta := savedSession.Meta()
		promptMatches := meta.PromptSchemaVersion == PromptSchemaVersion &&
			meta.PromptStableHash == stablePrompt.StableHash && meta.PromptMessageCount > 0
		if err == nil && !usingWorkingContext && promptMatches {
			if checkpoint, checkpointErr := savedSession.LoadCheckpoint(); checkpointErr == nil {
				if provenance, provenanceErr := savedSession.LoadTranscriptProvenance(); provenanceErr == nil && checkpointCanRestore(checkpoint, stablePrompt, provenance) {
					messages = restoreCheckpointMessages(messages, checkpoint, meta.PromptMessageCount)
				}
			}
		}
		if err != nil {
			_ = savedSession.MarkFailed(err)
			return nil, err
		}
		conversation := &Conversation{Session: savedSession, Messages: messages, UsingWorkingContext: usingWorkingContext}
		if promptMatches {
			if restoredPrompt, ok := promptSnapshotFromMessages(messages, meta.PromptMessageCount, stablePrompt.StableHash); ok {
				conversation.Prompt = restoredPrompt
				return conversation, nil
			}
		}

		snapshot, snapshotErr := resolvePromptSnapshot(stablePrompt, buildSessionContext)
		if snapshotErr != nil {
			_ = savedSession.MarkFailed(snapshotErr)
			return nil, snapshotErr
		}
		previousCount := legacyPromptMessageCount(messages)
		if meta.PromptSchemaVersion == PromptSchemaVersion && meta.PromptMessageCount > 0 {
			previousCount = meta.PromptMessageCount
		}
		if err := conversation.EnsurePromptSnapshot(snapshot, previousCount); err != nil {
			_ = savedSession.MarkFailed(err)
			return nil, err
		}
		if err := savedSession.SetPromptSnapshot(PromptSchemaVersion, snapshot.StableHash, snapshot.MessageCount()); err != nil {
			_ = savedSession.MarkFailed(err)
			return nil, err
		}
		return conversation, nil
	}

	snapshot, err := resolvePromptSnapshot(stablePrompt, buildSessionContext)
	if err != nil {
		return nil, err
	}
	conversation := &Conversation{
		Session:  savedSession,
		Messages: messages,
	}
	if err := conversation.EnsurePromptSnapshot(snapshot, 0); err != nil {
		if savedSession != nil {
			// Best effort; the primary error is returned below.
			_ = savedSession.MarkFailed(err)
		}
		return nil, err
	}
	if savedSession != nil {
		if err := savedSession.SetPromptSnapshot(PromptSchemaVersion, snapshot.StableHash, snapshot.MessageCount()); err != nil {
			return nil, err
		}
	}
	return conversation, nil
}

func resolvePromptSnapshot(stablePrompt agent.PromptSnapshot, buildSessionContext SessionContextBuilder) (agent.PromptSnapshot, error) {
	sessionContext := stablePrompt.SessionContext
	if buildSessionContext != nil {
		var err error
		sessionContext, err = buildSessionContext()
		if err != nil {
			return agent.PromptSnapshot{}, err
		}
	}
	return agent.NewFrozenPromptSnapshot(stablePrompt.Stable, sessionContext), nil
}

func (conversation *Conversation) EnsurePromptSnapshot(snapshot agent.PromptSnapshot, previousCount int) error {
	snapshot = agent.NewFrozenPromptSnapshot(snapshot.Stable, snapshot.SessionContext)
	promptMessages := snapshot.Messages()
	removeCount := previousCount
	existingCount := legacyPromptMessageCount(conversation.Messages)
	if removeCount <= 0 || removeCount > existingCount {
		removeCount = existingCount
	}

	updated := make([]map[string]any, 0, len(promptMessages)+len(conversation.Messages)-removeCount)
	updated = append(updated, promptMessages...)
	updated = append(updated, conversation.Messages[removeCount:]...)
	conversation.Messages = updated
	conversation.Prompt = snapshot
	if conversation.Session == nil {
		return nil
	}
	if len(updated) == len(promptMessages) && removeCount == 0 {
		return conversation.Session.RecordMessages(promptMessages...)
	}
	if conversation.UsingWorkingContext {
		return conversation.Session.SaveWorkingMessages(updated)
	}
	return conversation.Session.RewriteMessages(updated)
}

func (conversation *Conversation) EnsureSystemMessage(systemPrompt string) error {
	snapshot := agent.NewFrozenPromptSnapshot(systemPrompt, "")
	if restored, ok := promptSnapshotFromMessages(conversation.Messages, 1, snapshot.StableHash); ok {
		conversation.Prompt = restored
		return nil
	}
	return conversation.EnsurePromptSnapshot(snapshot, legacyPromptMessageCount(conversation.Messages))
}

func checkpointCanRestore(checkpoint session.ContextCheckpoint, prompt agent.PromptSnapshot, provenance session.TranscriptProvenance) bool {
	if checkpoint.PromptStableHash != "" && checkpoint.PromptStableHash != prompt.StableHash {
		return false
	}
	if checkpoint.PromptStableHash == "" && strings.TrimSpace(checkpoint.SystemPrompt) != "" && stablePrompt(checkpoint.SystemPrompt) != stablePrompt(prompt.Combined()) {
		return false
	}
	if checkpoint.SourceTranscriptSHA256 != "" {
		return checkpoint.SourceTranscriptSHA256 == provenance.SHA256 && checkpoint.SourceTranscriptSize == provenance.Size
	}
	return !checkpoint.UpdatedAt.Before(provenance.ModTime)
}

func restoreCheckpointMessages(messages []map[string]any, checkpoint session.ContextCheckpoint, promptMessageCount int) []map[string]any {
	if strings.TrimSpace(checkpoint.Summary) == "" || len(checkpoint.TailMessages) == 0 {
		return messages
	}

	availablePromptCount := legacyPromptMessageCount(messages)
	if promptMessageCount <= 0 || promptMessageCount > availablePromptCount {
		promptMessageCount = availablePromptCount
	}
	restored := make([]map[string]any, 0, len(checkpoint.TailMessages)+promptMessageCount+1)
	if promptMessageCount > 0 {
		restored = append(restored, messages[:promptMessageCount]...)
	}
	restored = append(restored, map[string]any{
		"role":    "system",
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

// migrateSyntheticSummaryRoles repairs working snapshots written by older
// versions. Synthetic summaries are context, not assistant replies; keeping
// them as assistant messages before the first user turn is rejected by
// providers that require every assistant message to belong to a user turn.
func migrateSyntheticSummaryRoles(messages []map[string]any) ([]map[string]any, bool) {
	var migrated []map[string]any
	for index, message := range messages {
		role, _ := message["role"].(string)
		content, _ := message["content"].(string)
		if role != "assistant" || !strings.HasPrefix(content, agent.EarlierConversationSummaryPrefix) {
			continue
		}
		if migrated == nil {
			migrated = append([]map[string]any(nil), messages...)
		}
		copyMessage := make(map[string]any, len(message))
		for key, value := range message {
			copyMessage[key] = value
		}
		copyMessage["role"] = "system"
		migrated[index] = copyMessage
	}
	if migrated == nil {
		return messages, false
	}
	return migrated, true
}

func stablePrompt(prompt string) string {
	if index := strings.Index(prompt, "\n\n# Relevant structured memory\n"); index >= 0 {
		return prompt[:index]
	}
	return prompt
}

func splitLegacySystemPrompt(prompt string) (string, string) {
	marker := "\n\n# Relevant structured memory\n"
	if index := strings.Index(prompt, marker); index >= 0 {
		return prompt[:index], prompt[index+2:]
	}
	return prompt, ""
}

func legacyPromptMessageCount(messages []map[string]any) int {
	count := 0
	for _, message := range messages {
		role, _ := message["role"].(string)
		if role != "system" {
			break
		}
		content, _ := message["content"].(string)
		if strings.HasPrefix(content, agent.EarlierConversationSummaryPrefix) {
			break
		}
		count++
	}
	return count
}

func promptSnapshotFromMessages(messages []map[string]any, messageCount int, stableHash string) (agent.PromptSnapshot, bool) {
	if messageCount < 1 || messageCount > 2 || len(messages) < messageCount {
		return agent.PromptSnapshot{}, false
	}
	stableRole, _ := messages[0]["role"].(string)
	stable, stableOK := messages[0]["content"].(string)
	if stableRole != "system" || !stableOK {
		return agent.PromptSnapshot{}, false
	}
	sessionContext := ""
	if messageCount == 2 {
		contextRole, _ := messages[1]["role"].(string)
		var contextOK bool
		sessionContext, contextOK = messages[1]["content"].(string)
		if contextRole != "system" || !contextOK || strings.HasPrefix(sessionContext, agent.EarlierConversationSummaryPrefix) {
			return agent.PromptSnapshot{}, false
		}
	}
	snapshot := agent.NewFrozenPromptSnapshot(stable, sessionContext)
	return snapshot, snapshot.StableHash == stableHash
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

func (conversation *Conversation) SaveWorkingContext() error {
	if conversation == nil || conversation.Session == nil {
		return nil
	}
	if err := conversation.Session.SaveWorkingContext(conversation.Messages); err != nil {
		return err
	}
	conversation.UsingWorkingContext = true
	return nil
}

func (conversation *Conversation) MarkFailed(err error) error {
	if conversation == nil || conversation.Session == nil {
		return nil
	}
	return conversation.Session.MarkFailed(err)
}
