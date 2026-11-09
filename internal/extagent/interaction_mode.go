package extagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

func supportsInteractionMode(agent AgentName, transport TransportKind) bool {
	return agent == AgentCodex && transport == TransportAppServer || agent == AgentClaude && transport == TransportCLI
}

func validateInteractionMode(mode string, agent AgentName, transport TransportKind) error {
	if mode == "" {
		return nil
	}
	if !supportsInteractionMode(agent, transport) {
		return fmt.Errorf("%s 的 %s 接口不支持切换执行或规划模式", agent, transport)
	}
	if mode != "plan" && mode != "default" {
		return fmt.Errorf("未知的 Agent 交互模式 %q", mode)
	}
	return nil
}

type codexCollaborationMode struct {
	Mode     string `json:"mode"`
	Settings struct {
		Model        string  `json:"model"`
		Effort       *string `json:"reasoning_effort"`
		Instructions *string `json:"developer_instructions"`
	} `json:"settings"`
}

var errNoCodexModeList = errors.New("Codex 未提供交互模式目录")

func codexModeCatalog(ctx context.Context, rpc func(context.Context, string, any) (rpcEnvelope, error)) ([]string, error) {
	reply, err := rpc(ctx, "collaborationMode/list", map[string]any{})
	if reply.Error != nil && reply.Error.Code == -32601 {
		return nil, errNoCodexModeList
	}
	if err != nil {
		return nil, err
	}
	var list struct{ Data []struct{ Mode string } }
	if json.Unmarshal(reply.Result, &list) != nil || list.Data == nil {
		return nil, fmt.Errorf("Codex 返回了无效的交互模式目录")
	}
	modes := make([]string, 0, 2)
	for _, preset := range list.Data {
		if (preset.Mode == "plan" || preset.Mode == "default") && !slices.Contains(modes, preset.Mode) {
			modes = append(modes, preset.Mode)
		}
	}
	return modes, nil
}

// A resumed thread can have been changed by another native client. Use its
// mode unless the user selected a new one. Older servers omit the readback.
func (c *codexAppServerClient) requestedMode(ctx context.Context, opened SessionState, options PromptOptions) (*codexCollaborationMode, error) {
	mode := options.InteractionMode
	if mode == "" {
		if opened.InteractionMode == "" {
			mode = options.savedInteractionMode
		} else if options.Model != "" && options.Model != opened.Model || options.Effort != "" && options.Effort != opened.Effort {
			mode = opened.InteractionMode
		}
	}
	if mode == "" {
		return nil, nil
	}
	if err := validateInteractionMode(mode, AgentCodex, TransportAppServer); err != nil {
		return nil, err
	}
	setting := &codexCollaborationMode{Mode: mode}
	// Switching models inside a mode should not replace that mode's native
	// instruction override. Entering another mode uses its built-in rules.
	c.mu.Lock()
	if prior := c.threadCollaboration; c.threadOptions.ExternalSessionID == opened.ExternalSessionID && prior != nil && prior.Mode == mode {
		setting.Settings.Instructions = prior.Settings.Instructions
	}
	c.mu.Unlock()
	setting.Settings.Model = firstNonEmpty(options.Model, opened.Model)
	if setting.Settings.Model == "" {
		return nil, fmt.Errorf("Codex 未报告线程模型，请先选择模型再切换交互模式")
	}
	if opened.InteractionMode != mode {
		// Some older servers silently ignore unknown turn/start fields. Ask
		// for presets before sending a prompt that is expected to only plan.
		probe, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		modes, err := codexModeCatalog(probe, c.request)
		if errors.Is(err, errNoCodexModeList) && opened.InteractionMode != "" {
			// Its native resume response proves this server understands the
			// mode field even though it doesn't expose preset enumeration.
		} else if err != nil {
			return nil, fmt.Errorf("Codex 无法确认原生交互模式：%w", err)
		} else if !slices.Contains(modes, mode) {
			return nil, fmt.Errorf("当前 Codex App Server 未提供 %q 交互模式", mode)
		}
	}
	effort := options.Effort
	if effort == "" && setting.Settings.Model == opened.Model {
		effort = opened.Effort
	}
	if effort != "" {
		setting.Settings.Effort = &effort
	}
	return setting, nil
}
