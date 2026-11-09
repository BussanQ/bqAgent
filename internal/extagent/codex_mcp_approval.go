package extagent

import (
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"
)

type mcpApprovalMeta struct {
	Kind    string          `json:"codex_approval_kind"`
	Tool    string          `json:"tool_name"`
	Persist json.RawMessage `json:"persist"`
	Params  json.RawMessage `json:"tool_params"`
	Display json.RawMessage `json:"tool_params_display"`
}

func (m mcpApprovalMeta) offersSession() bool {
	var one string
	if json.Unmarshal(m.Persist, &one) == nil {
		return one == "session"
	}
	var many []json.RawMessage
	if json.Unmarshal(m.Persist, &many) != nil {
		return false
	}
	return slices.ContainsFunc(many, func(raw json.RawMessage) bool {
		var scope string
		return json.Unmarshal(raw, &scope) == nil && scope == "session"
	})
}

func emptyMCPApprovalForm(mode, schema json.RawMessage) bool {
	if len(mode) != 0 {
		var value string
		if json.Unmarshal(mode, &value) != nil || value != "form" {
			return false
		}
	}
	if len(schema) == 0 || string(schema) == "null" {
		return true
	}
	var form struct {
		Type       string
		Properties map[string]json.RawMessage
		Required   []string
	}
	return json.Unmarshal(schema, &form) == nil && form.Type == "object" && form.Properties != nil && len(form.Properties) == 0 && len(form.Required) == 0
}

const maxApprovalPreview = 16 << 10

func approvalPreview(text string) string {
	if len(text) <= maxApprovalPreview {
		return text
	}
	end := maxApprovalPreview
	for !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + "\n…（已截断）"
}

func (m mcpApprovalMeta) detail() string {
	if len(m.Display) == 0 || string(m.Display) == "null" {
		if string(m.Params) == "null" {
			return ""
		}
		return approvalPreview(string(m.Params))
	}
	// When the provider supplies presentation fields, use that projection.
	// It may intentionally omit internal arguments from the approval.
	var fields []struct {
		Name        string
		DisplayName string `json:"display_name"`
		Value       json.RawMessage
	}
	if json.Unmarshal(m.Display, &fields) != nil {
		return ""
	}
	var text strings.Builder
	for _, field := range fields {
		label := firstNonEmpty(field.DisplayName, field.Name)
		if label == "" || len(field.Value) == 0 {
			continue
		}
		if text.Len() > 0 {
			text.WriteByte('\n')
		}
		text.WriteString(approvalPreview(label))
		text.WriteString(": ")
		var value string
		if field.Value[0] != '"' || json.Unmarshal(field.Value, &value) != nil {
			value = string(field.Value)
		}
		text.WriteString(approvalPreview(value))
		if text.Len() > maxApprovalPreview {
			break
		}
	}
	return approvalPreview(text.String())
}

func mcpDecision(decision string) any {
	action := "decline"
	var meta map[string]string
	switch decision {
	case "accept":
		action = "accept"
	case "acceptForSession":
		action = "accept"
		meta = map[string]string{"persist": "session"}
	case "cancel":
		action = "cancel"
		meta = map[string]string{"codex_approval_cancellation_reason": "user"}
	}
	return map[string]any{"action": action, "content": nil, "_meta": meta}
}

func (c *codexAppServerClient) startMCPElicitation(e rpcEnvelope) {
	var p struct {
		ThreadID, TurnID, ServerName, Message string
		Mode, RequestedSchema                 json.RawMessage
		Meta                                  json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(e.Params, &p) != nil {
		c.invalidPrompt(e.ID)
		return
	}
	spec := codexPrompt{thread: p.ThreadID, turn: p.TurnID, fallback: mcpDecision("")}
	var meta mcpApprovalMeta
	if p.TurnID != "" && p.ThreadID != "" && json.Unmarshal(p.Meta, &meta) == nil && meta.Kind == "mcp_tool_call" && emptyMCPApprovalForm(p.Mode, p.RequestedSchema) {
		options := []ACPPermissionOption{{OptionID: "accept", Name: "允许本次", Kind: "allow_once"}}
		if meta.offersSession() {
			options = append(options, ACPPermissionOption{OptionID: "acceptForSession", Name: "本会话允许此工具", Kind: "allow_always"})
		}
		options = append(options, ACPPermissionOption{OptionID: "cancel", Name: "拒绝本次调用", Kind: "reject_once"})
		tool, _ := json.Marshal(map[string]string{"source": "codex-app-server", "kind": "mcp_tool_call", "title": approvalPreview(firstNonEmpty(p.Message, "允许调用 MCP 工具？")), "server": approvalPreview(p.ServerName), "tool": approvalPreview(meta.Tool), "detail": meta.detail()})
		spec = codexApproval(p.ThreadID, p.TurnID, acpPermissionParams{ToolCall: tool, Options: options}, spec.fallback, mcpDecision)
	}
	c.startPrompt(e.ID, spec)
}
