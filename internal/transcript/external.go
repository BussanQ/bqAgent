// Package transcript defines the on-disk presentation records shared by the
// session reader, provider request builders and WebUI.
package transcript

import (
	"encoding/json"
	"strings"
)

const (
	SubmissionKey      = "bq_submission_id"
	ExternalHistoryKey = "bq_external_history"
	ExternalPatchKey   = "bq_external_patch"
	ExternalInputKey   = "bq_external_input"
	ExternalToolsKey   = "bq_external_tools"
)

// Tool parts reference the sanitized snapshot in bq_external_tools. Text
// parts carry public assistant output. IDs are local to their segment.
type Part struct {
	Kind    string          `json:"kind"`
	ID      string          `json:"id"`
	Text    string          `json:"text,omitempty"`
	Prompt  *Prompt         `json:"prompt,omitempty"`
	Plan    *Plan           `json:"plan,omitempty"`
	Summary *SummarySection `json:"summary,omitempty"`
}

// Each public summary paragraph can be revised independently. All paragraphs
// of an item share one disclosure at the first observed position.
type SummarySection struct {
	Item      string `json:"item"`
	Index     int    `json:"index"`
	Ended     bool   `json:"ended,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

const MaxSummarySections = 32

// Prompt retains the public title and disposition. Free-form answers and
// editor contents belong to the native agent interaction.
type Prompt struct {
	Questions []PromptQuestion `json:"questions,omitempty"`
	Detail    string           `json:"detail,omitempty"`
	Header    string           `json:"header,omitempty"`
	Options   []PromptOption   `json:"options,omitempty"`
	Title     string           `json:"title"`
	Method    string           `json:"method,omitempty"`
	State     string           `json:"state"`
	Choice    string           `json:"choice,omitempty"`
}

type PromptOption struct {
	ID          string `json:"id,omitempty"`
	Selected    bool   `json:"selected,omitempty"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type PromptQuestion struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Header      string         `json:"header,omitempty"`
	MultiSelect bool           `json:"multi_select,omitempty"`
	Options     []PromptOption `json:"options,omitempty"`
}

type ExternalHistory struct {
	TurnID string `json:"turn_id,omitempty"`
	ID     string `json:"id"`
	Parts  []Part `json:"parts"`
}

type Patch struct {
	TurnID  string `json:"turn_id,omitempty"`
	Segment string `json:"segment"`
	Part
	Append bool `json:"append,omitempty"`
}

// Parts keeps first placement independently of later revisions and grows
// streamed text without repeatedly copying an entire assistant answer.
type Parts struct {
	keys  map[[2]string]int
	items []partValue
}

type partValue struct {
	Part
	text *strings.Builder
}

func (p *Parts) Apply(part Part, appendText bool) {
	if p.keys == nil {
		p.keys = make(map[[2]string]int)
	}
	key := [2]string{part.Kind, part.ID}
	i, ok := p.keys[key]
	if !ok {
		i = len(p.items)
		p.keys[key] = i
		p.items = append(p.items, partValue{Part: Part{Kind: part.Kind, ID: part.ID}})
	}
	item := &p.items[i]
	if part.Kind == "prompt" {
		item.Prompt = part.Prompt
	}
	if part.Kind == "plan" && part.Plan != nil {
		item.Plan = part.Plan
	}
	if part.Kind == "summary" {
		item.Summary = part.Summary
	}
	if part.Kind == "text" || part.Kind == "plan" || part.Kind == "summary" {
		if !appendText || item.text == nil {
			item.text = &strings.Builder{}
		}
		item.text.WriteString(part.Text)
	}
}

func (p *Parts) Snapshot() []Part {
	parts := make([]Part, 0, len(p.items))
	for _, item := range p.items {
		part := item.Part
		if item.text != nil {
			part.Text = item.text.String()
		}
		parts = append(parts, part)
	}
	return parts
}

func (p *Parts) Text(id string) string {
	part, _ := p.Get("text", id)
	return part.Text
}

func (p *Parts) Get(kind, id string) (Part, bool) {
	i, ok := p.keys[[2]string{kind, id}]
	if !ok {
		return Part{}, false
	}
	item := p.items[i]
	part := item.Part
	if item.text != nil {
		part.Text = item.text.String()
	}
	return part, true
}

func (p *Parts) Has(kind, id string) bool { _, ok := p.keys[[2]string{kind, id}]; return ok }

func ReadExternalHistory(raw any) (ExternalHistory, bool) {
	if h, ok := raw.(ExternalHistory); ok {
		return h, h.ID != ""
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return ExternalHistory{}, false
	}
	id, _ := obj["id"].(string)
	if id == "" {
		return ExternalHistory{}, false
	}
	h := ExternalHistory{ID: id}
	h.TurnID, _ = obj["turn_id"].(string)
	for _, entry := range asSlice(obj["parts"]) {
		v, _ := entry.(map[string]any)
		part := readPart(v)
		if part.ID != "" {
			h.Parts = append(h.Parts, part)
		}
	}
	return h, true
}

func readPart(v map[string]any) Part {
	kind, _ := v["kind"].(string)
	id, _ := v["id"].(string)
	text, _ := v["text"].(string)
	part := Part{Kind: kind, ID: id, Text: text}
	if p, ok := v["summary"].(map[string]any); ok {
		item, _ := p["item"].(string)
		index, ok := p["index"].(float64)
		ended, _ := p["ended"].(bool)
		truncated, _ := p["truncated"].(bool)
		if ok && item != "" && index >= 0 && index < MaxSummarySections && index == float64(int(index)) {
			part.Summary = &SummarySection{Item: item, Index: int(index), Ended: ended, Truncated: truncated}
		}
	}
	if p, ok := v["prompt"].(map[string]any); ok {
		title, _ := p["title"].(string)
		method, _ := p["method"].(string)
		state, _ := p["state"].(string)
		choice, _ := p["choice"].(string)
		part.Prompt = &Prompt{Title: title, Method: method, State: state, Choice: choice}
		part.Prompt.Header, _ = p["header"].(string)
		part.Prompt.Detail, _ = p["detail"].(string)
		if p["questions"] != nil {
			data, err := json.Marshal(p["questions"])
			var questions []PromptQuestion
			if err == nil && json.Unmarshal(data, &questions) == nil {
				part.Prompt.Questions = questions
			}
		}
		for _, raw := range asSlice(p["options"]) {
			entry, _ := raw.(map[string]any)
			label, _ := entry["label"].(string)
			description, _ := entry["description"].(string)
			if label != "" {
				part.Prompt.Options = append(part.Prompt.Options, PromptOption{Label: label, Description: description})
			}
		}
	}
	if p, ok := v["plan"].(map[string]any); ok {
		mode, _ := p["mode"].(string)
		plan := &Plan{Mode: mode}
		for _, raw := range asSlice(p["steps"]) {
			entry, _ := raw.(map[string]any)
			text, _ := entry["text"].(string)
			status, _ := entry["status"].(string)
			priority, _ := entry["priority"].(string)
			plan.Steps = append(plan.Steps, PlanStep{Text: text, Status: status, Priority: priority})
		}
		part.Plan = plan
	}
	return part
}

func asSlice(raw any) []any {
	if raw == nil {
		return nil
	}
	if rows, ok := raw.([]any); ok {
		return rows
	}
	// Also accept typed snapshots supplied by an in-process session caller.
	data, _ := json.Marshal(raw)
	var rows []any
	_ = json.Unmarshal(data, &rows)
	return rows
}

type pendingSegment struct {
	turnID    string
	position  int
	parts     Parts
	tools     []any
	toolIndex map[string]int
}

// Records folds journal revisions while decoding lines, keeping one logical
// message per segment. A later canonical record replaces the partial state
// at its first position, including when a user message has since arrived.
type Records struct {
	rows      []map[string]any
	offsets   []int64
	active    map[[2]string]*pendingSegment
	positions map[[2]string]int
	inputs    map[string]bool
}

func (r *Records) add(row map[string]any, offset int64) {
	r.rows = append(r.rows, row)
	if offset >= 0 {
		r.offsets = append(r.offsets, offset)
	}
}

func (s *pendingSegment) updateTools(tools any) {
	for _, tool := range asSlice(tools) {
		v, _ := tool.(map[string]any)
		id, _ := v["id"].(string)
		if id == "" {
			continue
		}
		if i, ok := s.toolIndex[id]; ok {
			s.tools[i] = tool
			continue
		}
		s.toolIndex[id] = len(s.tools)
		s.tools = append(s.tools, tool)
	}
}

func (r *Records) Add(row map[string]any, offset int64) {
	if id, ok := row[ExternalInputKey].(string); ok && id != "" && row["role"] == "user" {
		if r.inputs == nil {
			r.inputs = make(map[string]bool)
		}
		if r.inputs[id] {
			return
		}
		r.inputs[id] = true
	}
	if row["role"] != "assistant" || (row[ExternalPatchKey] == nil && row[ExternalHistoryKey] == nil) {
		r.add(row, offset)
		return
	}
	if r.positions == nil {
		r.positions = make(map[[2]string]int)
		r.active = make(map[[2]string]*pendingSegment)
	}
	name, _ := row["name"].(string)
	if raw, ok := row[ExternalPatchKey].(map[string]any); ok {
		id, _ := raw["segment"].(string)
		part := readPart(raw)
		if id == "" || part.ID == "" {
			return
		}
		key := [2]string{name, id}
		s := r.active[key]
		if s == nil {
			pos, exists := r.positions[key]
			if !exists {
				pos = len(r.rows)
				r.positions[key] = pos
				r.add(map[string]any{"role": "assistant", "name": name}, offset)
			}
			s = &pendingSegment{position: pos, toolIndex: make(map[string]int)}
			if exists {
				old := r.rows[pos]
				if h, ok := ReadExternalHistory(old[ExternalHistoryKey]); ok {
					s.turnID = h.TurnID
					for _, p := range h.Parts {
						s.parts.Apply(p, false)
					}
				}
				s.updateTools(old[ExternalToolsKey])
			}
			r.active[key] = s
		}
		appendText, _ := raw["append"].(bool)
		if turnID, _ := raw["turn_id"].(string); turnID != "" {
			s.turnID = turnID
		}
		if part.Kind == "text" || part.Kind == "tool" || part.Kind == "prompt" || part.Kind == "plan" || part.Kind == "summary" && part.Summary != nil {
			s.parts.Apply(part, appendText)
		}
		s.updateTools(row[ExternalToolsKey])
		return
	}
	var id string
	switch h := row[ExternalHistoryKey].(type) {
	case map[string]any:
		id, _ = h["id"].(string)
	case ExternalHistory:
		id = h.ID
	}
	if id != "" {
		key := [2]string{name, id}
		if pos, ok := r.positions[key]; ok {
			r.rows[pos] = row
			delete(r.active, key)
			return
		}
		r.positions[key] = len(r.rows)
	}
	r.add(row, offset)
}

func (r *Records) Messages() ([]map[string]any, []int64) {
	for key, s := range r.active {
		m := r.rows[s.position]
		parts := s.parts.Snapshot()
		m[ExternalHistoryKey] = ExternalHistory{ID: key[1], TurnID: s.turnID, Parts: parts}
		m["content"] = PartialReply(parts)
		if len(s.tools) > 0 {
			m[ExternalToolsKey] = s.tools
		}
	}
	return r.rows, r.offsets
}
