package tui

import (
	"context"
	"io"

	"bqagent/internal/chatcommand"
	"bqagent/internal/modelcatalog"
	"bqagent/internal/taskstate"
	"bqagent/internal/turn"
)

const ResumeHistoryBudget = 200 << 10

// Backend isolates the terminal state machine from server, provider and HTTP
// implementation details.
type Backend interface {
	RunTurn(context.Context, string, string, TurnEvents) (TurnResult, error)
	RuntimeInfo(string) RuntimeInfo
	History(string, int) (History, error)
	Commands() []Command
	ProviderSettings(context.Context) (ProviderSettings, error)
	DiscoverProviderModels(context.Context, ProviderInput) ([]modelcatalog.Model, error)
	SaveProvider(context.Context, string, ProviderInput) (RuntimeInfo, error)
}

// AttachmentBackend is implemented by backends that accept images and
// workspace files with a turn. Text-only backends reject image turns and
// receive @file mentions as plain text.
type AttachmentBackend interface {
	RunTurnWithAttachments(context.Context, string, string, TurnAttachments, TurnEvents) (TurnResult, error)
}

// TurnAttachments are sent alongside the message text. Files are workspace
// relative, slash-separated paths whose contents the backend inlines.
type TurnAttachments struct {
	Images []Image
	Files  []string
}

func (attachments TurnAttachments) empty() bool {
	return len(attachments.Images) == 0 && len(attachments.Files) == 0
}

// Image is a validated attachment ready for a multimodal user message. Label
// is the placeholder shown in the prompt and sent with the message text.
type Image struct {
	Label    string
	MIMEType string
	Data     []byte
}

type TurnEvents struct {
	Message  turn.Sink
	Session  func(string)
	Token    func(string)
	Progress func(string)
	Tool     func(ToolEvent)
	Usage    func(TokenUsage)
	Context  func(turn.ContextUsage)
}

type TokenUsage struct {
	InputTokens     int
	OutputTokens    int
	InputEstimated  bool
	OutputEstimated bool
	Calls           int
}

type ToolEvent struct {
	Kind       string
	Seq        uint64
	ID         string
	Name       string
	Status     string
	Arguments  map[string]any
	Preview    string
	DurationMS int64
	Truncated  bool
}

type TurnResult struct {
	TurnID           string
	Outcome          turn.Outcome
	SessionID        string
	Reply            string
	Model            string
	ModelDisplayName string
	Mode             string
	Streamed         bool
	Metrics          *GenerationMetrics
}

type GenerationMetrics struct {
	FirstTokenLatencyMS  int64
	PromptTokens         int
	CachedPromptTokens   int
	CacheUsageAvailable  bool
	CompletionTokens     int
	ReasoningTokens      int
	GenerationDurationMS int64
	TokensPerSecond      float64
	CacheMetrics         *CacheMetrics
}

type CacheMetrics struct {
	Available           bool
	Calls               int
	InputTokens         int
	CacheReadTokens     int
	CacheWriteTokens    int
	UncachedInputTokens int
	HitRate             float64
}

type RuntimeInfo struct {
	Provider         string
	APIType          string
	Model            string
	ModelDisplayName string
	Mode             string
}

type ProviderSettings struct {
	ActiveProvider string
	Providers      []Provider
}

type Provider struct {
	ID                string
	Name              string
	APIType           string
	BaseURL           string
	Models            []string
	ModelDisplayNames modelcatalog.Names
	DefaultModel      string
	APIKeyConfigured  bool
}

type ProviderInput struct {
	OriginalID        string
	ID                string
	Name              string
	APIType           string
	BaseURL           string
	APIKey            string
	Models            []string
	ModelDisplayNames modelcatalog.Names
	DefaultModel      string
	APIKeyConfigured  bool
}

type History struct {
	ID       string
	Title    string
	Task     *taskstate.State
	Messages []HistoryMessage
	Omitted  int
}

type HistoryMessage struct {
	Role    string
	Content string
	Tools   []ToolEvent
}

type Command = chatcommand.Command
type CommandArgument = chatcommand.Argument

type Config struct {
	// ActivitySink must be nonblocking. States are idle, working and blocked.
	ActivitySink func(state, sessionID string)
	Context      context.Context
	Input        io.Reader
	Output       io.Writer
	Workspace    string
	AgentDir     string
	InitialTask  string
	SessionID    string
	NoColor      bool
}

// TurnInputBackend is implemented by backends that consume turn.InputQueue
// from the turn context. Backends without it never see steering or follow-up
// input; the TUI keeps the next-turn queue for them so nothing is swallowed.
type TurnInputBackend interface {
	SupportsTurnInput() bool
}
