package extagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode"
)

// An operator may have routed approvals to a separate MCP tool or disabled
// host prompts. Inspect the Claude segment of a wrapped command only.
func claudeApprovalArgs(args []string) ([]string, bool) {
	flags := args
	if i := commandWrapperSeparator(args); i >= 0 {
		flags = args[i+2:]
	}
	stdio := false
	for i := 0; i < len(flags); i++ {
		flag, value, inline := strings.Cut(flags[i], "=")
		if flag != "--permission-prompt-tool" && flag != "--permission-prompts" {
			continue
		}
		if !inline && i+1 < len(flags) {
			i++
			value = flags[i]
		}
		if flag == "--permission-prompt-tool" {
			if value != "stdio" {
				return args, false
			}
			stdio = true
		} else if value != "host" {
			return args, false
		}
	}
	if !stdio {
		args = append(slices.Clone(args), "--permission-prompt-tool", "stdio")
	}
	return args, true
}

type claudeToolRequest struct {
	subagent                    bool
	Subtype, Title, Description string
	Name                        string `json:"tool_name"`
	ToolID                      string `json:"tool_use_id"`
	Agent                       string `json:"agent_id"`
	DisplayName                 string `json:"display_name"`
	Reason                      string `json:"decision_reason"`
	Path                        string `json:"blocked_path"`
	Input                       json.RawMessage
	Suggestions                 []map[string]json.RawMessage `json:"permission_suggestions"`
	SuppressRule                bool                         `json:"suppress_always_allow_rule"`
	DefaultToNo                 bool                         `json:"default_to_no"`
	RequiresInteraction         bool                         `json:"requires_user_interaction"`
}

var claudeTerminalEscape = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\))`)

func claudeDisplayText(s string) string {
	return approvalPreview(strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && (unicode.IsControl(r) || unicode.In(r, unicode.Cf)) {
			return -1
		}
		return r
	}, claudeTerminalEscape.ReplaceAllString(s, "")))
}

func (p claudeToolRequest) deny(reason string) map[string]any {
	return map[string]any{"behavior": "deny", "message": reason, "toolUseID": p.ToolID}
}

func (p claudeToolRequest) decide(ctx context.Context, session string, ask piDialogHandler, receipt *permissionReceipt) map[string]any {
	plan := p.Name == "ExitPlanMode"
	if len(p.Input) == 0 || p.Input[0] != '{' || p.ToolID == "" || p.Name == "" {
		return p.deny("Invalid Claude tool permission request.")
	}
	if p.Name == "AskUserQuestion" {
		return p.askQuestions(ctx, session, ask, receipt)
	}
	if p.RequiresInteraction && !plan {
		return p.deny("This request requires a dedicated interaction that this WebUI cannot display.")
	}
	title := firstNonEmpty(p.Title, p.DisplayName, p.Name)
	options := []ACPPermissionOption{{OptionID: "allow", Name: "允许本次", Kind: "allow_once"}}
	deny := ACPPermissionOption{OptionID: "deny", Name: "拒绝", Kind: "reject_once"}
	denial := "User denied this action."
	if plan {
		options[0].Name, deny.Name = "批准计划", "继续规划"
		denial = "The user asked you to keep planning. Revise the plan and call ExitPlanMode again."
	}
	var detail []string
	for _, field := range []string{p.Description, p.Reason, p.Path} {
		if field != "" {
			detail = append(detail, field)
		}
	}
	if p.Agent != "" {
		detail = append(detail, "子 Agent："+p.Agent)
	}
	input := string(p.Input)
	if plan {
		var args struct {
			Plan, PlanFilePath string
			FilePath           string `json:"plan_file_path"`
		}
		if json.Unmarshal(p.Input, &args) == nil && args.Plan != "" {
			if path := firstNonEmpty(args.PlanFilePath, args.FilePath); path != "" {
				detail = append(detail, "计划文件："+path)
			}
			input = args.Plan
		}
	}
	detail = append(detail, input)
	if !plan && !p.SuppressRule && len(p.Suggestions) > 0 {
		// Send only the exact native updates displayed on the session option.
		available := true
		for _, update := range p.Suggestions {
			var kind string
			if json.Unmarshal(update["type"], &kind) != nil || !slices.Contains([]string{"addRules", "replaceRules", "removeRules", "setMode", "addDirectories", "removeDirectories"}, kind) {
				available = false
				break
			}
			update["destination"] = json.RawMessage(`"session"`)
		}
		if raw, err := json.Marshal(p.Suggestions); available && err == nil && len(raw) <= maxApprovalPreview {
			options = append(options, ACPPermissionOption{OptionID: "allowForSession", Name: "本次 Claude 运行期间允许", Kind: "allow_always", Description: string(raw)})
		}
	}
	if p.DefaultToNo {
		options = append([]ACPPermissionOption{deny}, options...)
	} else {
		options = append(options, deny)
	}
	call, _ := json.Marshal(map[string]string{"source": "claude-cli", "kind": "tool_approval", "tool": claudeDisplayText(p.Name), "title": claudeDisplayText(title), "toolCallId": p.ToolID, "detail": claudeDisplayText(strings.Join(detail, "\n"))})
	outcome := ask(ctx, acpPermissionParams{SessionID: session, ToolCall: call, Options: options, receipt: receipt})
	if outcome.Outcome != "selected" || !slices.ContainsFunc(options, func(o ACPPermissionOption) bool { return o.OptionID == outcome.OptionID }) || outcome.OptionID == "deny" {
		return p.deny(denial)
	}
	result := map[string]any{"behavior": "allow", "toolUseID": p.ToolID, "updatedInput": p.Input}
	if outcome.OptionID == "allowForSession" {
		result["updatedPermissions"] = p.Suggestions
	}
	return result
}

type claudePendingPrompt struct{ cancel context.CancelFunc }

type claudePromptHost struct {
	mode    claudePlanMode
	ctx     context.Context
	ask     piDialogHandler
	mu      sync.Mutex
	pending map[string]*claudePendingPrompt
	ended   bool
	workers sync.WaitGroup
}

func newClaudePromptHost(ctx context.Context, ask piDialogHandler) *claudePromptHost {
	return &claudePromptHost{ctx: ctx, ask: ask, pending: make(map[string]*claudePendingPrompt)}
}

func (h *claudePromptHost) withdraw(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if p := h.pending[id]; p != nil {
		delete(h.pending, id)
		p.cancel()
	}
}

func (h *claudePromptHost) end() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ended = true
	for id, p := range h.pending {
		delete(h.pending, id)
		p.cancel()
	}
}

func (c *claudeUsageControl) dispatch(id, session string, req claudeToolRequest) error {
	h := c.host
	if id == "" || len(id) > 4096 {
		return fmt.Errorf("Claude control_request 缺少有效的请求 ID")
	}
	h.mu.Lock()
	if h.ended || h.pending[id] != nil || h.ctx.Err() != nil {
		h.mu.Unlock()
		return nil
	}
	if len(h.pending) >= 64 {
		h.mu.Unlock()
		return fmt.Errorf("Claude 同时等待的工具请求过多")
	}
	ctx, cancel := context.WithCancel(h.ctx)
	p := &claudePendingPrompt{cancel}
	h.pending[id] = p
	h.workers.Add(1)
	h.mu.Unlock()
	c.mu.Lock()
	c.receivedRequest = true
	if c.timer != nil {
		c.timer.Stop()
	}
	c.mu.Unlock()
	go func() {
		defer h.workers.Done()
		defer cancel()
		receipt := new(permissionReceipt)
		reply := map[string]any{"request_id": id}
		var decision map[string]any
		if req.Subtype == "can_use_tool" {
			decision = req.decide(ctx, session, h.ask, receipt)
			reply["subtype"], reply["response"] = "success", decision
		} else {
			reply["subtype"], reply["error"] = "error", "Unsupported Claude control request: "+req.Subtype
		}
		c.writeMu.Lock()
		h.mu.Lock()
		owned := h.pending[id] == p && ctx.Err() == nil
		deliver := owned && !receipt.revoked.Load()
		var exitPlan any
		if deliver {
			exitPlan = h.mode.exitRequest(req, decision)
		}
		h.mu.Unlock()
		var err error
		if owned {
			if !deliver {
				reply["response"] = req.deny("This permission request is no longer active.")
			}
			envelope := map[string]any{"type": "control_response", "response": reply}
			if exitPlan == nil {
				err = json.NewEncoder(c.input).Encode(envelope)
			} else {
				err = c.writePlanDecision(exitPlan, envelope)
			}
		}
		c.writeMu.Unlock()
		h.mu.Lock()
		if h.pending[id] == p {
			delete(h.pending, id)
		}
		h.mu.Unlock()
		if err != nil {
			c.close()
		}
		if receipt.publish != nil {
			receipt.publish(deliver && err == nil)
		}
	}()
	return nil
}

// Commit both lines in one pipe write. The CLI receives the mode control
// before the allow, even if its mode acknowledgement arrives much later.
func (c *claudeUsageControl) writePlanDecision(exit, approval any) error {
	var batch bytes.Buffer
	enc := json.NewEncoder(&batch)
	if err := enc.Encode(exit); err != nil {
		return err
	}
	if err := enc.Encode(approval); err != nil {
		return err
	}
	_, err := c.input.Write(batch.Bytes())
	return err
}
