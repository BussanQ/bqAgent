package server

import (
	"encoding/json"
	"strings"

	"bqagent/internal/extagent"
	"bqagent/internal/transcript"
)

type externalPromptSink struct {
	timeline *externalTimeline
	next     extagent.ACPPermissionSink
}

func (s externalPromptSink) QuestionsSettled() {
	if observer, ok := s.next.(interface{ QuestionsSettled() }); ok {
		observer.QuestionsSettled()
	}
}

func (t *externalTimeline) permissions(next extagent.ACPPermissionSink) extagent.ACPPermissionSink {
	if next == nil {
		return nil
	}
	return externalPromptSink{timeline: t, next: next}
}

func (s externalPromptSink) EmitACPPermissionRequest(req extagent.ACPPermissionRequest) {
	s.timeline.mu.Lock()
	defer s.timeline.mu.Unlock()
	s.timeline.recordPrompt(req)
	s.next.EmitACPPermissionRequest(req)
}

func (s externalPromptSink) EmitACPPermissionResolution(req extagent.ACPPermissionRequest) {
	s.timeline.mu.Lock()
	defer s.timeline.mu.Unlock()
	s.timeline.recordPrompt(req)
	if sink, ok := s.next.(interface {
		EmitACPPermissionResolution(extagent.ACPPermissionRequest)
	}); ok {
		sink.EmitACPPermissionResolution(req)
	} else if req.Dialog != nil && req.Expired {
		s.next.EmitACPPermissionRequest(req)
	}
}

func (t *externalTimeline) recordPrompt(req extagent.ACPPermissionRequest) {
	if t.closed || req.RequestID == "" {
		return
	}
	p := transcript.Prompt{State: "pending"}
	if req.Dialog != nil {
		p.Title, p.Method = req.Dialog.Title, req.Dialog.Method
		if p.Method == "question" {
			p.Header = req.Dialog.Header
			p.Questions = req.Dialog.Questions
			for _, opt := range req.Options {
				if opt.OptionID != "cancel" && opt.OptionID != "submit" {
					p.Options = append(p.Options, transcript.PromptOption{Label: opt.Name, Description: opt.Description})
				}
			}
		}
	} else {
		var call struct{ Title, Kind, Source, Server, Tool, Detail string }
		_ = json.Unmarshal(req.ToolCall, &call)
		p.Title = strings.TrimSpace(call.Title)
		if p.Title == "" {
			p.Title = call.Kind
		}
		p.Method = "permission"
		if req.Agent == extagent.AgentCodex && call.Source == "codex-app-server" && call.Kind == "mcp_tool_call" {
			p.Method, p.Detail = "mcp_approval", call.Detail
			var identity []string
			if call.Server != "" {
				identity = append(identity, call.Server)
			}
			if call.Tool != "" {
				identity = append(identity, call.Tool)
			}
			p.Header = strings.Join(identity, " / ")
		} else if req.Agent == extagent.AgentClaude && call.Source == "claude-cli" {
			p.Header, p.Detail = call.Tool, call.Detail
		}
		if p.Method == "mcp_approval" || p.Header != "" {
			for _, opt := range req.Options {
				p.Options = append(p.Options, transcript.PromptOption{Label: opt.Name, Description: opt.Description})
			}
		}
	}
	if req.Selected != "" {
		p.State, p.Choice = "selected", req.Selected
		for _, o := range req.Options {
			if o.OptionID == req.Selected {
				p.Choice = o.Name
				break
			}
		}
		if req.Dialog != nil && req.Dialog.Method == "question" && (req.Selected == "submit" || req.Dialog.Secret) {
			p.Choice = ""
		}
	} else if req.Expired {
		p.State = "cancelled"
	}
	part := transcript.Part{Kind: "prompt", ID: req.RequestID, Prompt: &p}
	if row := t.sealed[[2]string{"prompt", part.ID}]; row != nil {
		h := row[transcript.ExternalHistoryKey].(transcript.ExternalHistory)
		for i, current := range h.Parts {
			if current.Kind == "prompt" && current.ID == part.ID {
				h.Parts[i] = part
				break
			}
		}
		if err := t.write(row); err != nil {
			t.waiting = append(t.waiting, row)
		}
		return
	}
	if !t.parts.Has("prompt", part.ID) {
		t.implicitText = ""
	}
	t.patch(part, false, nil)
}
