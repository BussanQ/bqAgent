package extagent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
)

// A card is resolved after the decision has reached the native writer.
// If Codex retracts it first, publish an expired card even if an HTTP
// submission for it was already accepted locally.
func codexApproval(thread, turn string, params acpPermissionParams, fallback any, reply func(string) any) codexPrompt {
	params.SessionID = thread
	params.receipt = &permissionReceipt{}
	return codexPrompt{
		thread: thread, turn: turn, fallback: fallback, receipt: params.receipt,
		ask: func(ctx context.Context, handler codexPromptHandler) any {
			outcome := handler(ctx, params)
			if outcome.Outcome != "selected" || !slices.ContainsFunc(params.Options, func(o ACPPermissionOption) bool { return o.OptionID == outcome.OptionID }) {
				return nil
			}
			return reply(outcome.OptionID)
		},
		settled: func(delivered bool, _ *appServerRun) {
			if params.receipt.publish != nil {
				params.receipt.publish(delivered)
			}
		},
	}
}

func (c *codexAppServerClient) startApproval(e rpcEnvelope) {
	var p struct {
		ThreadID, TurnID, ItemID, Reason string
		Command                          json.RawMessage
		Permissions                      json.RawMessage
		AvailableDecisions               []json.RawMessage
	}
	if json.Unmarshal(e.Params, &p) != nil {
		c.invalidPrompt(e.ID)
		return
	}
	options := []ACPPermissionOption{{OptionID: "accept", Name: "允许本次", Kind: "allow_once"}, {OptionID: "acceptForSession", Name: "本会话允许", Kind: "allow_always"}, {OptionID: "decline", Name: "拒绝", Kind: "reject_once"}, {OptionID: "cancel", Name: "取消", Kind: "reject_once"}}
	reply := func(decision string) any { return map[string]string{"decision": decision} }
	if e.Method == "item/permissions/requestApproval" {
		options = []ACPPermissionOption{{OptionID: "accept", Name: "允许本次请求的权限", Kind: "allow_once"}, {OptionID: "decline", Name: "拒绝", Kind: "reject_once"}}
		reply = func(decision string) any {
			result := map[string]any{"permissions": map[string]any{}, "scope": "turn"}
			if decision == "accept" && len(p.Permissions) > 0 {
				result["permissions"] = p.Permissions
			}
			return result
		}
	} else if p.AvailableDecisions != nil {
		var offered []ACPPermissionOption
		for _, raw := range p.AvailableDecisions {
			var id string
			if json.Unmarshal(raw, &id) != nil {
				continue
			}
			for _, o := range options {
				if id == o.OptionID {
					offered = append(offered, o)
					break
				}
			}
		}
		options = offered
	}
	var command string
	if len(p.Command) > 0 && json.Unmarshal(p.Command, &command) != nil {
		var args []string
		if json.Unmarshal(p.Command, &args) == nil {
			command = strings.Join(args, " ")
		}
	}
	tool, _ := json.Marshal(map[string]any{"source": "codex-app-server", "toolCallId": p.ItemID, "title": firstNonEmpty(command, p.Reason, e.Method), "rawInput": e.Params})
	spec := codexApproval(p.ThreadID, p.TurnID, acpPermissionParams{ToolCall: tool, Options: options}, reply("cancel"), reply)
	if len(options) == 0 {
		spec.ask = nil
	}
	c.startPrompt(e.ID, spec)
}
