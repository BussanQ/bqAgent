package extagent

import (
	"context"
	"encoding/json"
	"strings"

	"bqagent/internal/transcript"
)

// Activity is one agent-reported text delta or tool update. CallID is scoped
// to the native turn. Input and output can arrive after the initial event.
type Activity struct {
	Summary              *SummaryUpdate
	Edits                *transcript.FileEdits
	NativeSession        string
	ContextUsage         *ContextUsage
	Plan                 *PlanUpdate
	Text                 string
	TextID               string
	TextSnapshot         bool
	CallID, Name, Status string
	Input                map[string]any
	Output               string
	AppendOutput         bool
}

// SummaryUpdate carries only the provider's public summary channel. An item
// snapshot may finish a streamed summary without repeating its paragraphs.
type SummaryUpdate struct {
	ID       string
	Index    int
	Delta    string
	Sections []string
	Ended    bool
}

type PlanUpdate struct {
	ID     string
	Text   string
	Append bool
	Plan   transcript.Plan
}

type ActivitySink func(Activity)
type activityContextKey struct{}

// ActivitySinkFromContext returns the observer for this prompt, if the caller requested updates.
func ActivitySinkFromContext(ctx context.Context) ActivitySink {
	sink, _ := ctx.Value(activityContextKey{}).(ActivitySink)
	return sink
}

func activityInput(raw json.RawMessage) map[string]any {
	var v any
	if json.Unmarshal(raw, &v) != nil || v == nil {
		return nil
	}
	if obj, ok := v.(map[string]any); ok {
		return obj
	}
	return map[string]any{"input": v}
}

// Keep binary result blocks out of the text preview.
func activityOutput(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	if text := extractACPText(raw); text != "" {
		return text
	}
	var result struct {
		Content json.RawMessage
		Type    string
	}
	if json.Unmarshal(raw, &result) == nil && (len(result.Content) != 0 || result.Type == "image" || result.Type == "audio") {
		return ""
	}
	if strings.HasPrefix(strings.TrimSpace(string(raw)), "[") {
		return ""
	}
	return string(raw)
}
