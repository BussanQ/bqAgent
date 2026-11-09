package extagent

import (
	"context"
	"encoding/json"

	"bqagent/internal/media"
	"bqagent/internal/transcript"
	"bqagent/internal/turn"
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
	ACPImages bool
	Agent     AgentName
	Kind      TransportKind
	Command   CommandSpec
}

func (t AgentTransport) SupportsImages() bool {
	return t.Kind == TransportAppServer || t.Kind == TransportCLI && cliTransportSupported(t.Agent) || t.Kind == TransportACP && t.ACPImages
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
	// AutoAppServer discovers the shared Codex daemon, with legacy fallbacks.
	// Explicit transport commands leave this disabled.
	AutoAppServer bool
}

type SessionState struct {
	InteractionMode      string `json:"interaction_mode,omitempty"`
	ServiceTier          string `json:"service_tier,omitempty"`
	PreferredServiceTier string `json:"preferred_service_tier,omitempty"`
	ForkBinding          bool   `json:"fork_binding,omitempty"`
	// An empty Claude fork first opens with --session-id, then resumes normally.
	ClaudeNewID       string        `json:"claude_new_id,omitempty"`
	Effort            string        `json:"effort,omitempty"`
	Model             string        `json:"model,omitempty"`
	BQSessionID       string        `json:"bq_session_id"`
	Agent             AgentName     `json:"agent"`
	Transport         TransportKind `json:"transport"`
	ExternalSessionID string        `json:"external_session_id,omitempty"`
}

type TurnRequest struct {
	InteractionMode       string
	InheritTierPreference bool
	ServiceTier           string
	ContextUsage          func(ContextUsage)
	ExpectedSession       string
	Compact               *CompactCommand
	Steering              *TurnSteering
	Images                []media.Image
	Effort                string
	Activity              ActivitySink
	Model                 string
	BQSessionID           string
	Agent                 AgentName
	Prompt                string
	CWD                   string
	PermissionSink        ACPPermissionSink
}

type PromptOptions struct {
	InteractionMode      string
	savedInteractionMode string
	QuestionsSettled     func()
	PreservedTier        *string
	ServiceTier          string
	TierExplicit         bool
	Steering             *TurnSteering
	Images               []media.Image
	Model                string
	Effort               string
}

// Ready exposes input only after the protocol identifies the active turn.
// Applied runs on the prompt's event loop after native acknowledgement.
type TurnSteering struct {
	// Started identifies a provider-acknowledged root turn or user message.
	Started    func(SessionState, string)
	Target     func(SessionState)
	Ready      func(func(turn.Input) error, ...turn.InputKind)
	Unconsumed func([]turn.Input)
	Applied    func(turn.Input)
}

type TurnResponse struct {
	settingsAcknowledged bool
	ForkTip              bool

	Command string
	Reply   string
	State   SessionState
}

type ACPPermissionOption struct {
	Description string `json:"description,omitempty"`
	OptionID    string `json:"option_id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
}

type ACPPermissionRequest struct {
	RequestID         string                `json:"request_id"`
	BQSessionID       string                `json:"session_id"`
	Agent             AgentName             `json:"agent"`
	ExternalSessionID string                `json:"external_session_id"`
	ToolCall          json.RawMessage       `json:"tool_call"`
	Options           []ACPPermissionOption `json:"options"`
	Dialog            *AgentDialog          `json:"dialog,omitempty"`
	Expired           bool                  `json:"expired,omitempty"`
	Selected          string                `json:"selected,omitempty"`
}

type AgentDialog struct {
	Questions   []transcript.PromptQuestion `json:"questions,omitempty"`
	Header      string                      `json:"header,omitempty"`
	FreeText    bool                        `json:"free_text,omitempty"`
	Secret      bool                        `json:"secret,omitempty"`
	NonBlocking bool                        `json:"nonblocking,omitempty"`
	Method      string                      `json:"method"`
	Title       string                      `json:"title"`
	Message     string                      `json:"message,omitempty"`
	Placeholder string                      `json:"placeholder,omitempty"`
	Prefill     string                      `json:"prefill,omitempty"`
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
