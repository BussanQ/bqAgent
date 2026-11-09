package server

import (
	"slices"
	"strings"

	"bqagent/internal/extagent"
	"bqagent/internal/transcript"
)

// ExternalPlanEvent updates a public checklist or a streamed proposal in place.
type ExternalPlanEvent struct {
	ID     string             `json:"id"`
	Agent  extagent.AgentName `json:"agent"`
	Text   string             `json:"text"`
	Plan   transcript.Plan    `json:"plan"`
	Append bool               `json:"append,omitempty"`
}

// After steering the native turn may still revise the same plan. Keep the
// original row in the working context and append small patches to the journal.
type sealedPlan struct {
	row     map[string]any
	segment string
	index   int
	text    strings.Builder
	retry   bool
}

func (t *externalTimeline) sealPlan(p transcript.Part, row map[string]any, index int) {
	if t.sealedPlans == nil {
		t.sealedPlans = make(map[string]*sealedPlan)
	}
	s := &sealedPlan{row: row, index: index, segment: t.segment}
	s.text.WriteString(p.Text)
	t.sealedPlans[p.ID] = s
	t.sealedPlanIDs = append(t.sealedPlanIDs, p.ID)
}

func (s *sealedPlan) part() transcript.Part {
	return s.row[transcript.ExternalHistoryKey].(transcript.ExternalHistory).Parts[s.index]
}

func (t *externalTimeline) observePlan(update extagent.PlanUpdate) {
	if update.ID == "" {
		return
	}
	old, exists := t.parts.Get("plan", update.ID)
	segment := t.segment
	sealed := t.sealedPlans[update.ID]
	if sealed != nil {
		old, exists, segment = sealed.part(), true, sealed.segment
	}
	if exists && old.Plan != nil && !update.Append && old.Plan.Mode == update.Plan.Mode && slices.Equal(old.Plan.Steps, update.Plan.Steps) {
		if update.Text == old.Text {
			return
		}
		if update.Plan.Mode == "proposal" && strings.HasPrefix(update.Text, old.Text) {
			update.Append = true
			update.Text = strings.TrimPrefix(update.Text, old.Text)
		}
	}
	if exists && update.Append && update.Text == "" {
		return
	}
	update.Plan.Steps = slices.Clone(update.Plan.Steps)
	part := transcript.Part{Kind: "plan", ID: update.ID, Text: update.Text, Plan: &update.Plan}
	if sealed == nil {
		if !exists {
			t.implicitText = ""
		}
		t.patch(part, update.Append, nil)
		segment = t.segment
	} else {
		if !update.Append {
			sealed.text.Reset()
		}
		sealed.text.WriteString(update.Text)
		h := sealed.row[transcript.ExternalHistoryKey].(transcript.ExternalHistory)
		h.Parts[sealed.index] = part
		h.Parts[sealed.index].Text = sealed.text.String()
		sealed.row["content"] = transcript.PartialReply(h.Parts)
		patch := map[string]any{"role": "assistant", "name": string(t.name), "content": "", transcript.ExternalPatchKey: transcript.Patch{Segment: segment, Part: part, Append: update.Append}}
		if sealed.retry {
			patch = sealed.row
		}
		if err := t.write(patch); err != nil {
			if !sealed.retry {
				t.waiting = append(t.waiting, sealed.row)
			}
			sealed.retry = true
		} else {
			sealed.retry = false
		}
	}
	if t.planSink != nil {
		t.planSink(ExternalPlanEvent{ID: segment + "/" + part.ID, Agent: t.name, Text: update.Text, Plan: update.Plan, Append: update.Append})
	}
}

func (t *externalTimeline) matchingPlan(reply string) string {
	if reply == "" {
		return ""
	}
	parts := t.parts.Snapshot()
	for i := len(parts) - 1; i >= 0; i-- {
		p := parts[i]
		if p.Kind == "plan" && p.Plan != nil && strings.TrimSpace(p.Plan.ReplyText(p.Text)) == reply {
			return t.segment + "/" + p.ID
		}
	}
	for i := len(t.sealedPlanIDs) - 1; i >= 0; i-- {
		s := t.sealedPlans[t.sealedPlanIDs[i]]
		p := s.part()
		if p.Plan != nil && strings.TrimSpace(p.Plan.ReplyText(p.Text)) == reply {
			return s.segment + "/" + p.ID
		}
	}
	return ""
}
