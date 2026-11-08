package server

import (
	"cmp"
	"context"
	"log"
	"slices"
	"time"

	"bqagent/internal/session"
)

// A handoff can be uncertain even when the tool process has exited. Preserve
// the person's message for chat history separately from the model transcript.
type queueDelivery struct {
	ID          string                `json:"id"`
	State       string                `json:"state"`
	Attachments []queueAttachmentInfo `json:"attachments,omitempty"`
}

type queueHandoffHistory struct {
	queueDelivery
	Text     string    `json:"text"`
	Blob     string    `json:"blob,omitempty"`
	Context  int       `json:"context"`
	Position int       `json:"position"`
	Recorded time.Time `json:"recorded"`
}

func (q *commandQueue) recordHandoff(s *session.Session, row *queuedCommand) {
	if q.History == nil {
		q.History = make(map[string]queueHandoffHistory)
	}
	h, ok := q.History[row.TurnID]
	if !ok {
		h = queueHandoffHistory{Text: row.Text, Blob: row.Blob, Context: s.ContextNumber(), Recorded: time.Now().UTC()}
		h.ID, h.Attachments = row.TurnID, slices.Clone(row.Attachments)
		rows, err := s.LoadMessages()
		if err == nil {
			h.Position = len(projectContextMessages(s, rows, nil, false))
		} else {
			log.Printf("locate queued handoff in history: %v", err)
			h.Position = int(^uint(0) >> 1)
		}
	}
	h.State = row.State
	q.History[h.ID] = h
}

func (h queueHandoffHistory) message() ConversationHistoryMessage {
	return ConversationHistoryMessage{Role: "user", Kind: "queue_delivery", Content: h.Text, Delivery: &h.queueDelivery}
}

func (svc *Service) withQueueHandoffs(s *session.Session, messages []ConversationHistoryMessage, before time.Time, recoverCurrent bool) []ConversationHistoryMessage {
	q, err := readCommandQueue(s, "")
	if err != nil {
		log.Printf("read queued handoff history for %s: %v", s.ID(), err)
		return messages
	}
	// A history request can be the first read after a crash. Reconcile only
	// the current journal and never acquire lasting ownership on behalf of a reader.
	if recoverCurrent && svc.webTurns != nil && (len(q.History) > 0 || slices.ContainsFunc(q.Entries, func(row queuedCommand) bool { return row.State == "steering" })) {
		reg := svc.webTurns
		reg.queueMu.Lock()
		owned := reg.queueLeases[s.ID()] != nil
		pin, _ := s.ContextAt(s.ContextNumber())
		live, err := reg.readQueue(pin, context.Background(), false)
		if !owned {
			reg.releaseQueue(s.ID())
		}
		reg.queueMu.Unlock()
		if err == nil && pin.ContextNumber() == s.ContextNumber() {
			q = live
		} else if err != nil {
			log.Printf("reconcile queued handoff history for %s: %v", s.ID(), err)
		}
	}
	var history []queueHandoffHistory
	for _, h := range q.History {
		if h.Context == s.ContextNumber() && (before.IsZero() || !h.Recorded.After(before)) {
			history = append(history, h)
		}
	}
	if len(history) == 0 {
		return messages
	}
	slices.SortFunc(history, func(a, b queueHandoffHistory) int {
		if n := cmp.Compare(a.Position, b.Position); n != 0 {
			return n
		}
		if n := a.Recorded.Compare(b.Recorded); n != 0 {
			return n
		}
		return cmp.Compare(a.ID, b.ID)
	})
	result := make([]ConversationHistoryMessage, 0, len(messages)+len(history))
	from := 0
	for _, h := range history {
		at := min(max(h.Position, from), len(messages))
		result = append(result, messages[from:at]...)
		result = append(result, h.message())
		from = at
	}
	return append(result, messages[from:]...)
}
