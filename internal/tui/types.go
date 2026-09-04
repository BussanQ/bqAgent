package tui

import (
	"context"
	"io"
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
	DiscoverProviderModels(context.Context, ProviderInput) ([]string, error)
	SaveProvider(context.Context, string, ProviderInput) (RuntimeInfo, error)
}

type TurnEvents struct {
	Token    func(string)
	Progress func(string)
	Tool     func(ToolEvent)
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
	SessionID string
	Reply     string
	Model     string
	Mode      string
	Streamed  bool
	Metrics   *GenerationMetrics
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
	Provider string
	APIType  string
	Model    string
	Mode     string
}

type ProviderSettings struct {
	ActiveProvider string
	Providers      []Provider
}

type Provider struct {
	ID               string
	Name             string
	APIType          string
	BaseURL          string
	Models           []string
	DefaultModel     string
	APIKeyConfigured bool
}

type ProviderInput struct {
	OriginalID       string
	ID               string
	Name             string
	APIType          string
	BaseURL          string
	APIKey           string
	Models           []string
	DefaultModel     string
	APIKeyConfigured bool
}

type History struct {
	ID       string
	Title    string
	Messages []HistoryMessage
	Omitted  int
}

type HistoryMessage struct {
	Role    string
	Content string
	Tools   []ToolEvent
}

type Command struct {
	Name        string
	Description string
	Arguments   []CommandArgument
}

type CommandArgument struct {
	Value       string
	Description string
}

type Config struct {
	Context     context.Context
	Input       io.Reader
	Output      io.Writer
	Workspace   string
	AgentDir    string
	InitialTask string
	SessionID   string
	NoColor     bool
}
