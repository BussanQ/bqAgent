package server

import (
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"

	"bqagent/internal/agent"
	"bqagent/internal/extagent"
	appruntime "bqagent/internal/runtime"
	"bqagent/internal/transcript"
	"bqagent/internal/turn"
)

func externalReplyMessage(name extagent.AgentName, content string, tools []agent.HistoryTool) map[string]any {
	message := map[string]any{"role": "assistant", "name": string(name), "content": content}
	if len(tools) > 0 {
		message[agent.ExternalToolsMessageKey] = tools
	}
	return message
}

// ExternalTextEvent adds to or revises a public native assistant item.
type ExternalTextEvent struct {
	ID     string             `json:"id"`
	Agent  extagent.AgentName `json:"agent"`
	Text   string             `json:"text"`
	Append bool               `json:"append,omitempty"`
}

// A timeline belongs to one prompt. A final snapshot is also replayed by
// some providers, so terminal events are idempotent for each native call ID.
type externalTimeline struct {
	summarySink           func(ExternalSummaryEvent)
	summaries             map[string]*externalSummaryItem
	summaryBytes          int
	pendingEdits          map[string]*transcript.FileEdits
	planSink              func(ExternalPlanEvent)
	sealedPlans           map[string]*sealedPlan
	sealedPlanIDs         []string
	replyItemID           string
	mu                    sync.Mutex
	tokens                io.Writer
	sink                  agent.ToolEventSink
	closed                bool
	seq                   uint64
	conversation          *appruntime.Conversation
	name                  extagent.AgentName
	textSink              func(ExternalTextEvent)
	parts                 transcript.Parts
	segment, implicitText string
	sealed                map[[2]string]map[string]any
	replayed              map[string]string
	textSeq               uint64
	index                 map[string]int
	tools                 []agent.HistoryTool
	recorded              map[string]bool
	historyErr            error
	dirty                 bool
	waiting               []map[string]any
	historyTurn           string
}

func (t *externalTimeline) steering(registry *activeTurnRegistry, turnID string, inputs *turn.InputQueue, sink turn.Sink, conversation *appruntime.Conversation, name extagent.AgentName) *extagent.TurnSteering {
	t.conversation, t.name = conversation, name
	if sink == nil {
		return nil
	}
	var seq uint64
	emit := func(kind, item, phase, text string) {
		seq++
		sink(turn.Event{TurnID: turnID, ItemID: item, Seq: seq, Kind: kind, Phase: phase, Text: text})
	}
	return &extagent.TurnSteering{
		Unconsumed: inputs.Restore,
		Ready: func(send func(turn.Input) error, kinds ...turn.InputKind) {
			if registry.setNativeSteering(turnID, inputs, send) {
				for _, kind := range kinds {
					emit("input.available", "", string(kind), string(name))
				}
			}
		},
		Applied: func(input turn.Input) {
			t.recordInput(input)
			emit("input.applied", input.ID, string(input.Kind), input.Text)
		},
	}
}

// Commit what preceded the correction together with the acknowledged user
// message. The history projection updates in-flight calls in this segment
// when a later checkpoint records their outcome.
func (t *externalTimeline) recordInput(input turn.Input) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var messages []map[string]any
	tools := t.unrecordedTools()
	if t.segment != "" {
		messages = append(messages, t.message(t.textContent(), tools))
	}
	content := input.Content
	if content == nil {
		content = input.Text
	}
	messages = append(messages, map[string]any{"role": "user", "content": content, transcript.ExternalInputKey: input.ID})
	if err := t.write(messages...); err != nil {
		t.waiting = append(t.waiting, messages...)
	}
	t.conversation.Messages = append(t.conversation.Messages, messages...)
	if t.sealed == nil {
		t.sealed = make(map[[2]string]map[string]any)
	}
	for i, p := range t.parts.Snapshot() {
		if p.Summary != nil {
			if item := t.summaries[p.Summary.Item]; item != nil {
				item.sealed = messages[0]
				item.sections[p.Summary.Index].position = i
			}
		}
		if p.Kind == "plan" {
			t.sealPlan(p, messages[0], i)
		}
		if p.Kind == "prompt" || (p.Kind == "text" && strings.HasPrefix(p.ID, "native/")) {
			t.sealed[[2]string{p.Kind, p.ID}] = messages[0]
		}
	}
	t.parts = transcript.Parts{}
	t.replayed = nil
	t.implicitText, t.segment = "", ""
	t.dirty = false
	if t.recorded == nil {
		t.recorded = make(map[string]bool)
	}
	for _, tool := range tools {
		t.recorded[tool.ID] = tool.Status != "running"
	}
}

// Keep acknowledged user boundaries ahead of later items on retry. Their
// IDs and the segment IDs make a partially written batch safe to repeat.
func (t *externalTimeline) write(messages ...map[string]any) error {
	t.historyErr = t.conversation.Session.RecordMessages(append(t.waiting, messages...)...)
	if t.historyErr == nil {
		t.waiting = nil
	}
	return t.historyErr
}

func (t *externalTimeline) startSegment() {
	if t.segment == "" {
		t.segment = turn.NewID()
	}
	if t.historyTurn == "" {
		t.historyTurn = turn.NewID()
	}
}

func (t *externalTimeline) textContent() string {
	return transcript.PartialReply(t.parts.Snapshot())
}

func (t *externalTimeline) message(content string, tools []agent.HistoryTool) map[string]any {
	t.startSegment()
	message := externalReplyMessage(t.name, content, tools)
	message[transcript.ExternalHistoryKey] = transcript.ExternalHistory{ID: t.segment, TurnID: t.historyTurn, Parts: t.parts.Snapshot()}
	return message
}

func (t *externalTimeline) patch(part transcript.Part, appendText bool, tool *agent.HistoryTool) {
	t.startSegment()
	if part.Kind != "tool_update" {
		t.parts.Apply(part, appendText)
	}
	row := map[string]any{"role": "assistant", "name": string(t.name), "content": "", transcript.ExternalPatchKey: transcript.Patch{Segment: t.segment, TurnID: t.historyTurn, Part: part, Append: appendText}}
	if tool != nil {
		row[agent.ExternalToolsMessageKey] = []agent.HistoryTool{*tool}
	}
	if t.conversation != nil {
		if t.dirty {
			t.dirty = t.write(t.message(t.textContent(), t.unrecordedTools())) != nil
		} else {
			t.dirty = t.write(row) != nil
		}
	}
}

func (t *externalTimeline) nextTextID() string {
	t.textSeq++
	return fmt.Sprintf("text/%d", t.textSeq)
}

func (t *externalTimeline) sendText(id, text string, appendText bool) {
	if t.textSink != nil {
		t.textSink(ExternalTextEvent{ID: t.segment + "/" + id, Agent: t.name, Text: text, Append: appendText})
	}
}
func (t *externalTimeline) unrecordedTools() []agent.HistoryTool {
	var tools []agent.HistoryTool
	for _, tool := range t.tools {
		if !t.recorded[tool.ID] {
			tool.TurnID = t.historyTurn
			tools = append(tools, tool)
		}
	}
	return tools
}

func (t *externalTimeline) observe(event extagent.Activity) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	if event.Summary != nil {
		t.observeSummary(*event.Summary)
		return
	}
	if event.Plan != nil {
		t.observePlan(*event.Plan)
		return
	}
	if event.Text != "" || event.TextSnapshot {
		id := "native/" + event.TextID
		if row := t.sealed[[2]string{"text", id}]; event.TextID != "" && row != nil {
			t.reviseSealedText(row, id, event)
			return
		}
		if event.TextID == "" {
			if t.implicitText == "" {
				t.implicitText = t.nextTextID()
			}
			id = t.implicitText
		}
		previous := t.parts.Text(id)
		if event.TextSnapshot && event.Text == previous && t.parts.Has("text", id) {
			return
		}
		appendText := !event.TextSnapshot
		text := event.Text
		if event.TextSnapshot && strings.HasPrefix(text, previous) {
			appendText = true
			text = strings.TrimPrefix(text, previous)
		}
		if text == "" && appendText && t.parts.Has("text", id) {
			return
		}
		t.patch(transcript.Part{Kind: "text", ID: id, Text: text}, appendText, nil)
		t.sendText(id, text, appendText)
		if t.textSink == nil && t.tokens != nil && appendText {
			_, _ = io.WriteString(t.tokens, text)
		}
		return
	}
	if event.CallID == "" {
		return
	}
	if t.index == nil {
		t.index = make(map[string]int)
	}
	i, exists := t.index[event.CallID]
	if !exists {
		t.implicitText = ""
		t.startSegment()
		i = len(t.tools)
		t.index[event.CallID] = i
		t.tools = append(t.tools, agent.HistoryTool{TurnID: t.historyTurn, ID: event.CallID, Name: "tool", Status: "running", Source: "external"})
	}
	call := &t.tools[i]
	edits := event.Edits
	if edits != nil && len(edits.Files) == 0 {
		edits = nil
	}
	if call.Status != "running" {
		// The terminal thread snapshot may add a patch omitted from the
		// original completion. Revise the same card at its first position.
		if call.Status == "succeeded" && event.Status == "succeeded" && event.Edits != nil && !reflect.DeepEqual(call.Edits, edits) {
			call.Edits = edits
			if t.recorded != nil {
				t.recorded[call.ID] = false
			}
			t.patch(transcript.Part{Kind: "tool_update", ID: call.ID}, false, call)
			t.emit(*call)
		}
		return
	}
	if event.Edits != nil {
		if t.pendingEdits == nil {
			t.pendingEdits = make(map[string]*transcript.FileEdits)
		}
		t.pendingEdits[event.CallID] = edits
	}
	if event.Status != "" && event.Status != "running" {
		if event.Status == "succeeded" {
			call.Edits = t.pendingEdits[event.CallID]
		}
		delete(t.pendingEdits, event.CallID)
	}
	changed := !exists
	if event.Name != "" && event.Name != call.Name {
		call.Name = event.Name
		changed = true
	}
	if event.Input != nil {
		args := agent.DisplayToolArguments(event.Input)
		if !reflect.DeepEqual(call.Arguments, args) {
			call.Arguments = args
			changed = true
		}
	}
	if event.Output != "" && (!event.AppendOutput || !call.Truncated) {
		output := event.Output
		if event.AppendOutput {
			output = call.Result + output
		}
		runes := []rune(output)
		preview, truncated := string(runes[:min(4096, len(runes))]), len(runes) > 4096
		if call.Result != preview || call.Truncated != truncated {
			call.Result, call.Truncated = preview, truncated
			changed = true
		}
	}
	if event.Status != "" && event.Status != call.Status {
		call.Status = event.Status
		changed = true
	}
	if !changed {
		return
	}
	kind := "tool_update"
	if !exists || t.parts.Has("tool", call.ID) {
		kind = "tool"
	}
	t.patch(transcript.Part{Kind: kind, ID: call.ID}, false, call)
	t.emit(*call)
}

// A native turn may replay completed items after a steering boundary. Keep
// the text at its original position, including in already-written history.
func (t *externalTimeline) reviseSealedText(row map[string]any, id string, event extagent.Activity) {
	h := row[transcript.ExternalHistoryKey].(transcript.ExternalHistory)
	if event.TextSnapshot {
		if t.replayed == nil {
			t.replayed = make(map[string]string)
		}
		t.replayed[id] = strings.TrimSpace(event.Text)
	}
	var texts []string
	for i, p := range h.Parts {
		if p.Kind != "text" {
			continue
		}
		if p.ID == id {
			text := event.Text
			if !event.TextSnapshot {
				text = p.Text + text
			}
			if text == p.Text {
				return
			}
			h.Parts[i].Text = text
		}
		texts = append(texts, h.Parts[i].Text)
	}
	row["content"] = strings.Join(texts, "\n\n")
	if err := t.write(row); err != nil {
		t.waiting = append(t.waiting, row)
	}
	if t.textSink != nil {
		t.textSink(ExternalTextEvent{ID: h.ID + "/" + id, Agent: t.name, Text: event.Text, Append: !event.TextSnapshot})
	}
}

func (t *externalTimeline) emit(call agent.HistoryTool) {
	if t.sink == nil {
		return
	}
	t.seq++
	kind := "tool_result"
	if call.Status == "running" {
		kind = "tool_start"
	}
	t.sink.EmitToolEvent(agent.ToolEvent{Sender: string(t.name), Source: call.Source, Kind: kind, Seq: t.seq, ID: call.ID, Name: call.Name, Status: call.Status, Arguments: call.Arguments, Preview: call.Result, Truncated: call.Truncated, TurnID: call.TurnID, Edits: call.Edits})
}

func (t *externalTimeline) finish() ([]agent.HistoryTool, string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	for _, item := range t.summaries {
		t.endSummary(item)
	}
	for i := range t.tools {
		if t.tools[i].Status == "running" {
			t.tools[i].Status = "unknown"
			t.emit(t.tools[i])
		}
	}
	return t.unrecordedTools(), t.textContent()
}

// Native protocols may report only the final answer, a last-item snapshot,
// or all streamed text concatenated. Preserve earlier commentary and append
// only the missing answer when reconciling that authoritative reply.
func (t *externalTimeline) recordReply(content string, tools []agent.HistoryTool, notice string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	parts := t.parts.Snapshot()
	var texts []string
	for _, p := range parts {
		if p.Kind == "text" && p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	reply := strings.TrimSpace(content)
	replayed := false
	if len(texts) == 0 {
		for _, text := range t.replayed {
			if text == reply {
				replayed = true
				break
			}
		}
	}
	if notice == "" && (len(texts) == 0 || strings.TrimSpace(texts[len(texts)-1]) != reply) {
		t.replyItemID = t.matchingPlan(reply)
	}
	lastID, newText := "", ""
	if notice != "" {
		newText = notice
	} else if t.replyItemID == "" && !replayed && (len(texts) == 0 || (reply != strings.TrimSpace(texts[len(texts)-1]) && reply != strings.TrimSpace(strings.Join(texts, "")) && reply != strings.TrimSpace(strings.Join(texts, "\n\n")))) {
		newText = content
		if len(parts) > 0 {
			last := parts[len(parts)-1]
			combined := strings.Join(texts, "")
			if combined != "" && strings.HasPrefix(content, combined) {
				newText = strings.TrimPrefix(content, combined)
				if last.Kind == "text" {
					lastID = last.ID
					newText = last.Text + newText
				}
			} else if last.Kind == "text" && last.Text != "" && strings.HasPrefix(content, last.Text) {
				lastID = last.ID
			}
		}
	}
	if newText != "" {
		if lastID == "" {
			lastID = t.nextTextID()
		}
		t.parts.Apply(transcript.Part{Kind: "text", ID: lastID, Text: newText}, false)
	}
	message := t.message(content, tools)
	if err := t.write(message); err != nil {
		return err
	}
	t.conversation.Messages = append(t.conversation.Messages, message)
	if newText != "" {
		t.sendText(lastID, newText, false)
	}
	return nil
}
