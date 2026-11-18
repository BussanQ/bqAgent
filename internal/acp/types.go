// Package acp implements the ACP version 1 JSON-RPC protocol over JSONL streams.
package acp

import "context"

type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type Model struct {
	ModelID     string `json:"modelId"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type ModelState struct {
	CurrentModelID  string  `json:"currentModelId"`
	AvailableModels []Model `json:"availableModels"`
}

type Update map[string]any

type Emit func(Update) error

// Backend opens a new conversation when sessionID is empty and loads one otherwise.
// Open must validate that cwd is an existing absolute directory. Methods should
// honor context cancellation. Each returned Conversation is owned by Serve.
type Backend interface {
	Open(ctx context.Context, cwd, sessionID string) (Conversation, error)
}

// Conversation methods are serialized per session, except that Close may run
// concurrently with an operation during shutdown. Close must release resources
// and should return promptly. Emit must only be called during Replay or Prompt;
// retained callbacks reject updates after completion or cancellation.
type Conversation interface {
	ID() string
	Models() ModelState
	Replay(context.Context, Emit) error
	Prompt(context.Context, []ContentBlock, Emit) (stopReason string, err error)
	SetModel(context.Context, string) error
	Close()
}
