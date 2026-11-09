package extagent

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
)

// Only the owning root can report its execution policy. Keep it even when
// the next status switches to plan and the user takes time to review.
type claudePlanMode struct {
	mu                         sync.Mutex
	launch, execution, current string
	configured                 string
	revision                   uint64
	pending                    map[string]claudeModeChange
}

type claudeModeChange struct {
	mode     string
	revision uint64
}

func claudeLaunchMode(args []string) string {
	if sep := commandWrapperSeparator(args); sep >= 0 {
		args = args[sep+2:]
	}
	var mode string
	for i := 0; i < len(args); i++ {
		flag, value, inline := strings.Cut(args[i], "=")
		if flag != "--permission-mode" {
			continue
		}
		if !inline && i+1 < len(args) {
			i++
			value = args[i]
		}
		mode = value
	}
	return mode
}

func (m *claudePlanMode) observe(subtype, mode string) {
	if subtype != "init" && subtype != "status" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record(mode)
}

func (m *claudePlanMode) record(mode string) {
	if mode == "" {
		return
	}
	m.revision++
	m.current = mode
	if mode != "plan" {
		m.execution = mode
	}
}

func (m *claudePlanMode) interaction() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch m.current {
	case "plan":
		return "plan"
	case "default", "acceptEdits", "auto", "dontAsk", "bypassPermissions":
		return "default"
	}
	return ""
}

// A launch preference cannot prove that this child was allowed to bypass
// permissions. Only an observed execution mode can restore that privilege.
func (m *claudePlanMode) buildPolicy() (string, error) {
	mode := m.execution
	if mode == "" {
		mode = m.configured
		if mode == "" && m.launch == "dontAsk" {
			mode = "dontAsk"
		}
		if mode == "bypassPermissions" {
			mode = "default"
		}
	}
	switch mode {
	case "", "plan":
		return "default", nil
	case "default", "acceptEdits", "dontAsk", "auto", "bypassPermissions":
		return mode, nil
	default:
		return "", fmt.Errorf("Claude 报告了未知的执行权限 %q", claudeDisplayText(mode))
	}
}

// Call under the prompt host lock after acquiring the native writer. A
// subagent's plan does not change its parent's permissions.
func (m *claudePlanMode) exitRequest(req claudeToolRequest, decision map[string]any) any {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current != "plan" || req.Name != "ExitPlanMode" || req.subagent || req.Agent != "" || decision["behavior"] != "allow" {
		return nil
	}
	mode, err := m.buildPolicy()
	if err != nil {
		return nil
	}
	id := newClaudeUUID()
	if m.pending == nil {
		m.pending = make(map[string]claudeModeChange)
	}
	m.pending[id] = claudeModeChange{mode: mode, revision: m.revision}
	return map[string]any{"type": "control_request", "request_id": id, "request": map[string]string{"subtype": "set_permission_mode", "mode": mode}}
}

func claudeAcknowledgedMode(r claudeControlReply, expected string) (string, error) {
	if r.Subtype != "success" {
		return "", fmt.Errorf("Claude 未确认权限模式 %s：%s", expected, firstNonEmpty(claudeDisplayText(r.Error), "请求被拒绝"))
	}
	var result struct{ Mode json.RawMessage }
	if len(r.Response) != 0 && json.Unmarshal(r.Response, &result) != nil {
		return "", fmt.Errorf("Claude 返回了无效的权限模式确认")
	}
	// Earlier stream-json versions acknowledge setters without a readback.
	if len(result.Mode) == 0 {
		return expected, nil
	}
	var mode string
	if json.Unmarshal(result.Mode, &mode) != nil || mode == "" {
		return "", fmt.Errorf("Claude 返回了无效的权限模式确认")
	}
	if mode != expected {
		return "", fmt.Errorf("Claude 确认的权限模式为 %q，预期 %q", claudeDisplayText(mode), expected)
	}
	return expected, nil
}

func (m *claudePlanMode) reply(r claudeControlReply) {
	m.mu.Lock()
	defer m.mu.Unlock()
	change, known := m.pending[r.ID]
	if !known {
		return
	}
	delete(m.pending, r.ID)
	mode, err := claudeAcknowledgedMode(r, change.mode)
	if err != nil {
		log.Printf("Claude 未确认退出规划模式（%s）", change.mode)
	} else if m.revision == change.revision {
		m.record(mode)
	}
}
