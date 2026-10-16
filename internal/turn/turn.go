// Package turn defines the channel-independent lifecycle of one user request.
// A completed turn does not imply that a session's programming task is done.
package turn

import (
	"crypto/rand"
)

type Status string

const (
	Completed     Status = "completed"
	Failed        Status = "failed"
	Interrupted   Status = "interrupted"
	BudgetLimited Status = "budget_limited"
	Blocked       Status = "blocked"
	AwaitingInput Status = "awaiting_input"
)

type Relation string

const (
	ContinueTask Relation = "continue"
	SideQuestion Relation = "side_question"
)

func (r Relation) Valid() bool { return r == "" || r == ContinueTask || r == SideQuestion }

type Outcome struct {
	TaskRelationReason string   `json:"task_relation_reason,omitempty"`
	Status             Status   `json:"status"`
	Reason             string   `json:"reason,omitempty"`
	TaskRelation       Relation `json:"task_relation,omitempty"`
	TaskStatus         string   `json:"task_status,omitempty"`
	Notice             string   `json:"notice,omitempty"`
}

func (o *Outcome) Set(status Status, reason string) {
	o.Status, o.Reason = status, reason
	switch status {
	case BudgetLimited:
		o.Notice = "本轮已达到执行限制，任务尚未完成。"
	case Blocked:
		o.Notice = "本轮遇到阻碍，任务尚未完成。"
	case AwaitingInput:
		o.Notice = "等待补充信息后继续。"
	case Interrupted:
		o.Notice = "本轮已中断。"
	case Failed:
		o.Notice = "本轮执行失败。"
	default:
		o.Notice = ""
	}
}

// Event.Text is a full snapshot, never an append instruction. Seq makes
// duplicate delivery and delayed snapshots harmless in every presentation.
type Event struct {
	TurnID string `json:"turn_id"`
	ItemID string `json:"item_id,omitempty"`
	Seq    uint64 `json:"seq"`
	Kind   string `json:"kind"` // message.updated, message.completed
	Phase  string `json:"phase"`
	Text   string `json:"text"`
}

type Sink func(Event)

// ContextUsage is the latest request size, not accumulated per-turn billing.
type ContextUsage struct {
	Tokens    int
	Limit     int
	Estimated bool
}

func NewID() string { return rand.Text() }

// Messages is a projection of server events. It never decides whether work is done.
type Messages struct {
	TurnID string
	Items  map[string]Event
	Latest Event
}

func (m *Messages) Apply(e Event) bool {
	if e.Kind != "message.updated" && e.Kind != "message.completed" {
		return false
	}
	if e.TurnID == "" || e.ItemID == "" || (m.TurnID != "" && m.TurnID != e.TurnID) {
		return false
	}
	if m.Items == nil {
		m.Items = make(map[string]Event)
	}
	if old, ok := m.Items[e.ItemID]; ok && (e.Seq <= old.Seq || old.Kind == "message.completed") {
		return false
	}
	m.TurnID = e.TurnID
	m.Items[e.ItemID] = e
	if e.Seq > m.Latest.Seq {
		m.Latest = e
	}
	return true
}
