package extagent

import "context"

type AgentName string

const (
	AgentClaude   AgentName = "claude"
	AgentCodex    AgentName = "codex"
	AgentCursor   AgentName = "cursor"
	AgentOpenCode AgentName = "opencode"
	AgentDefault  AgentName = "default"
)

type TransportKind string

const (
	TransportACP TransportKind = "acp"
	TransportCLI TransportKind = "cli"
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
	CLIFallback    bool
	StartupError   string
	DetectionNotes []string
}

type Config struct {
	WorkspaceRoot string
	Agents        map[AgentName]AgentConfig
}

type AgentConfig struct {
	ACP CommandSpec
	CLI CommandSpec
}

type SessionState struct {
	BQSessionID       string        `json:"bq_session_id"`
	Agent             AgentName     `json:"agent"`
	Transport         TransportKind `json:"transport"`
	ExternalSessionID string        `json:"external_session_id,omitempty"`
}

type TurnRequest struct {
	BQSessionID string
	Agent       AgentName
	Prompt      string
	CWD         string
}

type TurnResponse struct {
	Reply string
	State SessionState
}

type ACPClient interface {
	Initialize(context.Context) error
	LoadSessionSupported() bool
	NewSession(context.Context, string) (string, error)
	LoadSession(context.Context, string, string) (string, error)
	Prompt(context.Context, string, string) (string, error)
	Close() error
}
