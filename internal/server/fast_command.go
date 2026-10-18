package server

import (
	"fmt"
	"strings"

	"bqagent/internal/agent"
	"bqagent/internal/session"
)

func (service *Service) sessionServiceTier(saved *session.Session) string {
	service = service.withLLMSnapshot()
	if saved != nil && saved.Meta().FastMode && service.apiType != agent.APITypeAnthropic {
		return "priority"
	}
	return ""
}

func (service *Service) handleFastCommand(message string, saved *session.Session, attachments bool) (string, bool, error) {
	service = service.withLLMSnapshot()
	fields := strings.Fields(message)
	if len(fields) == 0 || !strings.EqualFold(fields[0], "/fast") {
		return "", false, nil
	}
	if attachments {
		return "", true, fmt.Errorf("/fast 命令不能携带图片或文件附件")
	}
	if saved == nil {
		return "", true, fmt.Errorf("session is required")
	}
	if len(fields) > 2 {
		return "", true, fmt.Errorf("用法：/fast、/fast on|off|status")
	}
	enabled := !saved.Meta().FastMode
	statusOnly := false
	if len(fields) == 2 {
		switch strings.ToLower(fields[1]) {
		case "on":
			enabled = true
		case "off":
			enabled = false
		case "status":
			enabled, statusOnly = saved.Meta().FastMode, true
		default:
			return "", true, fmt.Errorf("用法：/fast、/fast on|off|status")
		}
	}
	if enabled && !statusOnly && service.apiType == agent.APITypeAnthropic {
		return "", true, fmt.Errorf("当前 Anthropic API 暂不支持 Fast 模式；可使用支持 service_tier=priority 的 OpenAI 兼容 API")
	}
	state := "关闭"
	if enabled {
		state = "开启"
	}
	reply := "当前会话 Fast 模式：" + state
	if enabled {
		if service.apiType == agent.APITypeAnthropic {
			reply += "（当前 Anthropic API 不生效）"
		} else {
			reply += "（请求 service_tier=priority，实际加速及计费取决于上游支持）"
		}
	}

	previous := saved.Meta().FastMode
	if err := saved.SetFastMode(enabled); err != nil {
		return "", true, err
	}
	if err := saved.RecordMessages(map[string]any{"role": "user", "content": message}, map[string]any{"role": "assistant", "content": reply}); err != nil {
		if rollbackErr := saved.SetFastMode(previous); rollbackErr != nil {
			return "", true, fmt.Errorf("record fast command: %w; rollback: %v", err, rollbackErr)
		}
		return "", true, err
	}
	return reply, true, nil
}
