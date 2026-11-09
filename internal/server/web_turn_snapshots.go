package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"bqagent/internal/turn"
)

type webMessagePatch struct {
	turn.Event
	BaseSeq uint64 `json:"base_seq"`
}

type webSnapshotState map[string]turn.Event

func isMessageSnapshot(e turn.Event) bool {
	return e.ItemID != "" && (e.Kind == "message.updated" || e.Kind == "message.completed")
}

// The agent publishes cumulative snapshots on each token. Store growing
// items as suffixes so a long response uses linear journal space.
func (s webSnapshotState) compact(e turn.Event) (string, any) {
	if !isMessageSnapshot(e) {
		return "message_snapshot", e
	}
	prev, found := s[e.ItemID]
	if found && (prev.Kind == "message.completed" || e.Seq <= prev.Seq) {
		return "message_snapshot", e
	}
	s[e.ItemID] = e
	if found && prev.Seq < e.Seq && len(prev.Text) > 32 {
		if suffix, ok := strings.CutPrefix(e.Text, prev.Text); ok {
			e.Text = suffix
			return "message_patch", webMessagePatch{e, prev.Seq}
		}
	}
	return "message_snapshot", e
}

// Existing SSE clients receive the full snapshots they already understand.
// For a cursor-only subscription reconstruct item state before that cursor.
func (s webSnapshotState) fullBatch(data []byte, after int, external map[string]*strings.Builder) ([]byte, error) {
	var out bytes.Buffer
	for len(data) > 0 {
		frame, rest, ok := bytes.Cut(data, []byte("\n\n"))
		if !ok {
			return nil, fmt.Errorf("incomplete journal frame")
		}
		data = rest
		header, body, ok := bytes.Cut(frame, []byte("\n"))
		if !ok {
			return nil, fmt.Errorf("invalid journal header")
		}
		id, err := strconv.Atoi(strings.TrimPrefix(string(header), "id: "))
		if err != nil {
			return nil, err
		}
		snapshot := []byte("event: message_snapshot\ndata: ")
		patch := []byte("event: message_patch\ndata: ")
		switch {
		case bytes.HasPrefix(body, []byte("event: external_text\ndata: ")):
			var e ExternalTextEvent
			if err := json.Unmarshal(body[len("event: external_text\ndata: "):], &e); err != nil {
				return nil, err
			}
			prev := external[e.ID]
			if prev == nil {
				prev = &strings.Builder{}
				external[e.ID] = prev
			}
			delta := e.Text
			if !e.Append {
				var extends bool
				delta, extends = strings.CutPrefix(e.Text, prev.String())
				if !extends {
					delta = ""
				}
				prev.Reset()
			}
			prev.WriteString(e.Text)
			if id > after {
				encoded, _ := json.Marshal(map[string]string{"delta": delta})
				fmt.Fprintf(&out, "id: %d\ndata: %s\n\n", id, encoded)
			}
			continue
		case bytes.HasPrefix(body, snapshot):
			var e turn.Event
			if err := json.Unmarshal(body[len(snapshot):], &e); err != nil {
				return nil, err
			}
			if isMessageSnapshot(e) {
				prev, ok := s[e.ItemID]
				if !ok || prev.Kind != "message.completed" && e.Seq > prev.Seq {
					s[e.ItemID] = e
				}
			}
		case bytes.HasPrefix(body, patch):
			var p webMessagePatch
			if err := json.Unmarshal(body[len(patch):], &p); err != nil {
				return nil, err
			}
			prev, ok := s[p.ItemID]
			if !ok || prev.Seq != p.BaseSeq {
				return nil, fmt.Errorf("invalid message patch base")
			}
			p.Text = prev.Text + p.Text
			s[p.ItemID] = p.Event
			if id > after {
				encoded, err := json.Marshal(p.Event)
				if err != nil {
					return nil, err
				}
				fmt.Fprintf(&out, "id: %d\nevent: message_snapshot\ndata: %s\n\n", id, encoded)
			}
			continue
		}
		if id > after {
			out.Write(frame)
			out.WriteString("\n\n")
		}
	}
	return out.Bytes(), nil
}
