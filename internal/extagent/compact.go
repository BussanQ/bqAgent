package extagent

import (
	"context"
	"fmt"
	"strings"
	"unicode"
)

// A conversation control is recognized on the original single-chat text,
// before attachment descriptions or other context are appended to the prompt.
type CompactCommand struct {
	NativeOnly   bool
	Instructions string
	Attachments  bool
	Started      func()
}

func ParseCompactCommand(text string, attachments bool) *CompactCommand {
	text = strings.TrimSpace(text)
	word, rest := text, ""
	if i := strings.IndexFunc(text, unicode.IsSpace); i >= 0 {
		word, rest = text[:i], strings.TrimSpace(text[i:])
	}
	if !strings.EqualFold(word, "/compact") {
		return nil
	}
	return &CompactCommand{Instructions: rest, Attachments: attachments}
}

func compactSupported(t *AgentTransport) bool {
	switch t.Agent {
	case AgentCodex:
		return t.Kind == TransportAppServer
	case AgentPi:
		return t.Kind == TransportCLI
	case AgentClaude:
		return t.Kind == TransportCLI && !claudeCommandsDisabled(t.Command.Args)
	}
	return false
}

// Bind delayed compaction to the selected agent's existing native session when
// it is known. Dispatch rechecks that binding while holding the session lock.
func (broker *Broker) QueuedCompactBinding(ctx context.Context, name AgentName, id, text string) (string, error) {
	cmd := ParseCompactCommand(text, false)
	if cmd == nil {
		return "", fmt.Errorf("仅支持排队 /compact 命令")
	}
	t, state, err := broker.catalogTransport(ctx, name, id)
	if err != nil {
		return "", err
	}
	if !compactSupported(t) {
		return "", fmt.Errorf("%s 的当前接口不支持原生 /compact", name)
	}
	if name == AgentCodex && cmd.Instructions != "" {
		return "", fmt.Errorf("Codex 的 /compact 不接受附加说明")
	}
	return state.ExternalSessionID, nil
}

func compactCatalog(t *AgentTransport, rows []NativeCommand) []NativeCommand {
	if !compactSupported(t) {
		return rows
	}
	entry := NativeCommand{Name: "compact", Text: "/compact", Kind: "control", Description: "压缩当前 Agent 会话的上下文"}
	if t.Agent == AgentPi || t.Agent == AgentClaude {
		entry.Hint = "[希望保留的信息]"
	}
	result := []NativeCommand{entry}
	for _, row := range rows {
		if row.Text != entry.Text {
			result = append(result, row)
		}
	}
	return result
}

func (broker *Broker) compact(ctx context.Context, t *AgentTransport, state SessionState, req TurnRequest, generation uint64) (TurnResponse, error) {
	command := req.Compact
	if !compactSupported(t) {
		if t.Agent == AgentClaude {
			return TurnResponse{}, fmt.Errorf("当前 Claude CLI 配置禁用了斜杠命令")
		}
		return TurnResponse{}, fmt.Errorf("Codex CLI 不支持原生 /compact，请使用 Codex App Server")
	}
	if command.Attachments || len(req.Images) > 0 {
		return TurnResponse{}, fmt.Errorf("/compact 不接受附件")
	}
	if t.Agent == AgentCodex && command.Instructions != "" {
		return TurnResponse{}, fmt.Errorf("Codex 的 /compact 不接受附加说明")
	}
	out := TurnResponse{State: state, Command: "compact"}
	if state.ExternalSessionID == "" {
		out.Reply = fmt.Sprintf("%s 还没有可压缩的会话内容。", state.Agent)
		return out, nil
	}
	if t.Agent == AgentClaude {
		return broker.cli.sendTurn(ctx, t.Command, state, req, nil)
	}
	if t.Agent == AgentPi {
		ask := func(dialogCtx context.Context, p acpPermissionParams) acpPermissionOutcome {
			return broker.awaitPermission(dialogCtx, state.BQSessionID, state.Agent, p, req.PermissionSink, generation)
		}
		return runPiRPC(ctx, t.Command, state, req, ask)
	}
	// Only resume this agent's saved thread. Opening a new thread or retrying
	// a dispatched command could compact a different conversation or do it twice.
	client, err := broker.sessionClient(ctx, state.BQSessionID, state.Agent, t.Command, req.CWD, generation, t.Kind)
	if err != nil {
		return TurnResponse{}, err
	}
	c, ok := client.(interface {
		compactThread(context.Context, string, string, *CompactCommand) (string, error)
	})
	if !ok {
		return TurnResponse{}, fmt.Errorf("Codex 适配器不支持原生 /compact")
	}
	out.Reply, err = c.compactThread(ctx, state.ExternalSessionID, req.CWD, command)
	if err != nil {
		broker.discardACPClient(state.BQSessionID, state.Agent, client)
	}
	return out, err
}
