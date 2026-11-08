package server

import (
	"strconv"
	"strings"

	"bqagent/internal/extagent"
	"bqagent/internal/transcript"
)

type ExternalSummaryEvent struct {
	ID        string             `json:"id"`
	Agent     extagent.AgentName `json:"agent"`
	Index     int                `json:"index"`
	Text      string             `json:"text"`
	Append    bool               `json:"append,omitempty"`
	Ended     bool               `json:"ended,omitempty"`
	Truncated bool               `json:"truncated,omitempty"`
}

const (
	maxSummaryItems        = 32
	maxSummarySectionBytes = 16 << 10
	maxTurnSummaryBytes    = 256 << 10
)

type externalSummaryItem struct {
	id, segment  string
	sections     map[int]*externalSummarySection
	first        int
	ended, retry bool
	sealed       map[string]any
}

type externalSummarySection struct {
	text      strings.Builder
	truncated bool
	position  int
}

func (t *externalTimeline) observeSummary(u extagent.SummaryUpdate) {
	if u.ID == "" || len(u.ID) > 1024 || u.Index < 0 {
		return
	}
	item := t.summaries[u.ID]
	if item == nil {
		if len(t.summaries) >= maxSummaryItems {
			return
		}
		if u.Delta == "" {
			hasText := false
			for _, section := range u.Sections[:min(len(u.Sections), transcript.MaxSummarySections)] {
				if strings.TrimSpace(section) != "" {
					hasText = true
					break
				}
			}
			if !hasText {
				return
			}
		}
		if t.summaries == nil {
			t.summaries = make(map[string]*externalSummaryItem)
		}
		item = &externalSummaryItem{id: u.ID, sections: make(map[int]*externalSummarySection), first: -1}
		t.summaries[u.ID] = item
	}
	if item.ended && !u.Ended {
		return
	}
	if u.Delta != "" {
		if u.Index >= transcript.MaxSummarySections {
			t.updateSummarySection(item, transcript.MaxSummarySections-1, "", true, true, 0)
			return
		}
		room := maxSummarySectionBytes
		if section := item.sections[u.Index]; section != nil {
			room -= section.text.Len()
		}
		t.updateSummarySection(item, u.Index, u.Delta, true, false, min(room, maxTurnSummaryBytes-t.summaryBytes))
		return
	}
	// A completion without paragraphs closes the stream without erasing
	// public deltas. A substantive snapshot may revise or remove paragraphs.
	if len(u.Sections) > 0 {
		// Reserve this snapshot's budget as a whole. Replacing a later,
		// longer paragraph can free room for an earlier correction.
		room := maxTurnSummaryBytes - t.summaryBytes
		for index, section := range item.sections {
			if u.Ended || index < len(u.Sections) {
				room += section.text.Len()
			}
		}
		for index, text := range u.Sections[:min(len(u.Sections), transcript.MaxSummarySections)] {
			overflow := index == transcript.MaxSummarySections-1 && len(u.Sections) > transcript.MaxSummarySections
			t.updateSummarySection(item, index, text, false, overflow, min(room, maxSummarySectionBytes))
			if section := item.sections[index]; section != nil {
				room -= section.text.Len()
			}
		}
		if u.Ended {
			for index := range item.sections {
				if index >= len(u.Sections) {
					t.updateSummarySection(item, index, "", false, false, 0)
				}
			}
		}
	}
	if u.Ended {
		t.endSummary(item)
	}
}

func (t *externalTimeline) updateSummarySection(item *externalSummaryItem, index int, value string, appendText, clipped bool, limit int) {
	part := item.sections[index]
	if part == nil {
		if value == "" && !clipped {
			return
		}
		part = &externalSummarySection{position: -1}
		item.sections[index] = part
		if item.first < 0 {
			item.first = index
		}
	}
	if appendText && part.truncated {
		return
	}
	old := part.text.String()
	text := historyUTF8Prefix(value, max(0, limit))
	clipped = clipped || len(text) != len(value)
	if !appendText && strings.HasPrefix(text, old) {
		text, appendText = text[len(old):], true
	}
	if appendText && text == "" && clipped == part.truncated {
		return
	}
	if !appendText {
		part.text.Reset()
	}
	part.text.WriteString(text)
	t.summaryBytes += part.text.Len() - len(old)
	part.truncated = clipped
	t.publishSummary(item, index, text, appendText)
}

func (t *externalTimeline) endSummary(item *externalSummaryItem) {
	if item.ended {
		return
	}
	item.ended = true
	if item.first >= 0 {
		t.publishSummary(item, item.first, "", true)
	}
}

func (t *externalTimeline) publishSummary(item *externalSummaryItem, index int, text string, appendText bool) {
	section := item.sections[index]
	part := transcript.Part{Kind: "summary", ID: "summary/" + item.id + "/" + strconv.Itoa(index), Text: text,
		Summary: &transcript.SummarySection{Item: item.id, Index: index, Ended: item.ended, Truncated: section.truncated}}
	if item.sealed == nil {
		if item.segment == "" {
			t.implicitText = ""
		}
		t.patch(part, appendText, nil)
		item.segment = t.segment
	} else {
		// A correction can close a chat segment while the native reasoning
		// item is still open. Later paragraphs belong to that same row.
		h := item.sealed[transcript.ExternalHistoryKey].(transcript.ExternalHistory)
		whole := part
		whole.Text = section.text.String()
		if section.position < 0 {
			section.position = len(h.Parts)
			h.Parts = append(h.Parts, whole)
		} else {
			h.Parts[section.position] = whole
		}
		item.sealed[transcript.ExternalHistoryKey] = h
		record := map[string]any{"role": "assistant", "name": string(t.name), "content": "", transcript.ExternalPatchKey: transcript.Patch{TurnID: t.historyTurn, Segment: item.segment, Part: part, Append: appendText}}
		if item.retry || len(t.waiting) > 0 {
			record = item.sealed
		}
		if err := t.write(record); err != nil {
			if !item.retry {
				t.waiting = append(t.waiting, item.sealed)
			}
			item.retry = true
		} else {
			item.retry = false
		}
	}
	if t.summarySink != nil {
		t.summarySink(ExternalSummaryEvent{ID: item.segment + "/summary/" + item.id, Agent: t.name, Index: index, Text: text, Append: appendText, Ended: item.ended, Truncated: section.truncated})
	}
}
