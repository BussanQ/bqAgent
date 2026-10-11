package extagent

import (
	"context"
	"encoding/json"
)

type AgentName string

const (
	AgentClaude   AgentName = "claude"
	AgentCodex    AgentName = "codex"
	AgentCursor   AgentName = "cursor"
	AgentOpenCode AgentName = "opencode"
	AgentPi       AgentName = "pi"
	AgentDefault  AgentName = "default"
)

type TransportKind string

const (
	TransportACP       TransportKind = "acp"
	TransportCLI       TransportKind = "cli"
	TransportAppServer TransportKind = "app-server"
)

type CommandSpec struct {
	Command string
	Args    []string
}

type AgentTransport struct {
	Agent   AgentName
	Kind    TransportKind
	Command CommandSpec
}

type DetectionResult struct {
	Agent          AgentName
	Preferred      *AgentTransport
	ACP            *AgentTransport
	CLI            *AgentTransport
	AppServer      *AgentTransport
	CLIFallback    bool
	StartupError   string
	DetectionNotes []string
}

type Config struct {
	WorkspaceRoot string
	Agents        map[AgentName]AgentConfig
}

type AgentConfig struct {
	ACP       CommandSpec
	CLI       CommandSpec
	AppServer CommandSpec
}

type SessionState struct {
	BQSessionID       string        `json:"bq_session_id"`
	Agent             AgentName     `json:"agent"`
	Transport         TransportKind `json:"transport"`
	ExternalSessionID string        `json:"external_session_id,omitempty"`
}

type TurnRequest struct {
	BQSessionID    string
	Agent          AgentName
	Prompt         string
	CWD            string
	PermissionSink ACPPermissionSink
}

type TurnResponse struct {
	Reply string
	State SessionState
}

type ACPPermissionOption struct {
	OptionID string `json:"option_id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

type ACPPermissionRequest struct {
	RequestID         string                `json:"request_id"`
	BQSessionID       string                `json:"session_id"`
	Agent             AgentName             `json:"agent"`
	ExternalSessionID string                `json:"external_session_id"`
	ToolCall          json.RawMessage       `json:"tool_call"`
	Options           []ACPPermissionOption `json:"options"`
}

type ACPPermissionSink interface {
	EmitACPPermissionRequest(ACPPermissionRequest)
}

// SessionClient is the shared lifecycle for persistent external agent transports.
type SessionClient interface {
	Initialize(context.Context) error
	LoadSessionSupported() bool
	NewSession(context.Context, string) (string, error)
	LoadSession(context.Context, string, string) (string, error)
	Prompt(context.Context, string, string) (string, error)
	Close() error
}

// ACPClient remains an alias for existing integrations and factories.
type ACPClient = SessionClient
