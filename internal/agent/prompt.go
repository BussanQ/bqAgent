package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// PromptSnapshot is the provider-visible prompt prefix for one conversation.
// Stable may be shared across conversations; SessionContext is frozen when the
// conversation is created so later turns can append without rewriting history.
type PromptSnapshot struct {
	Stable         string
	SessionContext string
	StableHash     string
}

func NewPromptSnapshot(stable, sessionContext, model string, apiType APIType) PromptSnapshot {
	return NewFrozenPromptSnapshot(AppendModelIdentitySystemPrompt(stable, model, apiType), sessionContext)
}

// NewFrozenPromptSnapshot normalizes an already assembled stable prefix without
// changing its model identity. It is used when restoring the exact bytes saved
// for an existing conversation.
func NewFrozenPromptSnapshot(stable, sessionContext string) PromptSnapshot {
	stable = normalizePromptText(stable)
	sessionContext = normalizePromptText(sessionContext)
	return PromptSnapshot{
		Stable:         stable,
		SessionContext: sessionContext,
		StableHash:     hashPromptText(stable),
	}
}

func (snapshot PromptSnapshot) Messages() []map[string]any {
	messages := make([]map[string]any, 0, snapshot.MessageCount())
	if snapshot.Stable != "" {
		messages = append(messages, map[string]any{"role": "system", "content": snapshot.Stable})
	}
	if snapshot.SessionContext != "" {
		messages = append(messages, map[string]any{"role": "system", "content": snapshot.SessionContext})
	}
	return messages
}

func (snapshot PromptSnapshot) MessageCount() int {
	count := 0
	if snapshot.Stable != "" {
		count++
	}
	if snapshot.SessionContext != "" {
		count++
	}
	return count
}

func (snapshot PromptSnapshot) Combined() string {
	parts := make([]string, 0, 2)
	if snapshot.Stable != "" {
		parts = append(parts, snapshot.Stable)
	}
	if snapshot.SessionContext != "" {
		parts = append(parts, snapshot.SessionContext)
	}
	return strings.Join(parts, "\n\n")
}

func normalizePromptText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	lines := strings.Split(value, "\n")
	for index := range lines {
		lines[index] = strings.TrimRight(lines[index], " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func hashPromptText(value string) string {
	sum := sha256.Sum256([]byte(normalizePromptText(value)))
	return hex.EncodeToString(sum[:])
}
