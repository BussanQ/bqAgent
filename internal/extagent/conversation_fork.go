package extagent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"bqagent/internal/privatefile"
	"bqagent/internal/session"
)

type ForkCheckpoint struct {
	Tip   bool         `json:"tip,omitempty"`
	State SessionState `json:"state"`
	Turn  string       `json:"turn"`
}

type ForkBinding struct {
	State SessionState
	// Native turn IDs (Codex) retained in the child.
	IDs map[string]string
}

var ErrForkUnknown = errors.New("Agent 未确认新会话 ID，无法找回这次分支；可重新创建分支")

func (b *Broker) ForkConversation(ctx context.Context, child *session.Session, opDir, cwd string, p ForkCheckpoint) (ForkBinding, error) {
	var result ForkBinding
	if b == nil {
		return result, errors.New("外部 Agent 未配置")
	}
	if err := b.waitForDetections(ctx); err != nil {
		return result, err
	}
	t, err := transportForSession(b.Detection(p.State.Agent), p.State)
	if err != nil {
		return result, err
	}
	state := p.State
	switch {
	case state.Agent == AgentCodex && state.Transport == TransportAppServer && t.Kind == state.Transport:
		client, err := NewCodexAppServerClient(ctx, t.Command, cwd)
		if err != nil {
			return result, err
		}
		defer client.Close()
		if err := client.Initialize(ctx); err != nil {
			return result, err
		}
		state.ExternalSessionID, result.IDs, err = copyCodexCheckpoint(ctx, client.(*codexAppServerClient).request, opDir, cwd, p)
		if err != nil {
			return result, err
		}
	case p.Tip && state.Agent == AgentOpenCode && state.Transport == TransportACP && t.Kind == state.Transport:
		active, err := b.ActiveSession(state.BQSessionID)
		if err != nil {
			return result, err
		}
		if active.Agent != state.Agent || active.Transport != state.Transport || active.ExternalSessionID != state.ExternalSessionID {
			return result, errors.New("OpenCode 原生会话已变更")
		}
		client, err := b.acpFactory(ctx, t.Command, cwd)
		if err != nil {
			return result, err
		}
		defer client.Close()
		if err := client.Initialize(ctx); err != nil {
			return result, err
		}
		state.ExternalSessionID, err = copyACPTip(ctx, client, opDir, cwd, p)
		if err != nil {
			return result, err
		}
	default:
		return result, errors.New("此 Agent 接口不支持从回复创建分支")
	}
	state.BQSessionID, state.ClaudeNewID = child.ID(), ""
	state.ForkBinding = true
	result.State = state
	return result, writeSingleRecord(filepath.Join(child.ContextDir(), "external-agent.json"), singleSessionRecord{SessionState: state})
}

type nativeForkReceipt struct {
	Source, Turn, Child string
	Prefix              []string
}

// Persist intent before asking a provider to mint a random ID. A lost reply
// is ambiguous. A saved ID can be verified again without creating a sibling.
func nativeForkID(path, source, turn string, preflight func() ([]string, error), create func() (string, bool, error)) (nativeForkReceipt, error) {
	r := nativeForkReceipt{Source: source, Turn: turn}
	data, err := os.ReadFile(path)
	if err == nil {
		if json.Unmarshal(data, &r) != nil || r.Source != source || r.Turn != turn {
			return r, errors.New("分支操作位置已变更")
		}
		if r.Child == "" || r.Child == source {
			return r, ErrForkUnknown
		}
		return r, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return r, err
	}
	if preflight != nil {
		r.Prefix, err = preflight()
		if err != nil {
			return r, err
		}
	}
	data, _ = json.Marshal(r)
	if err := privatefile.WriteFile(path, data); err != nil {
		return r, err
	}
	child, refused, err := create()
	if err != nil {
		if refused {
			return r, errors.Join(err, os.Remove(path))
		}
		return r, errors.Join(ErrForkUnknown, err)
	}
	if child == "" || child == source {
		return r, ErrForkUnknown
	}
	r.Child = child
	data, _ = json.Marshal(r)
	if err := privatefile.WriteFile(path, data); err != nil {
		return r, errors.Join(ErrForkUnknown, err)
	}
	return r, nil
}

func (c *stdioACPClient) SupportsConversationFork() bool {
	return c.forkSessionSupported && c.LoadSessionSupported()
}

func forkRequestRefused(msg rpcEnvelope) bool {
	// An internal RPC failure can occur after the provider creates the child.
	return msg.Error != nil && (msg.Error.Code == -32601 || msg.Error.Code == -32602)
}

func (c *stdioACPClient) forkSession(ctx context.Context, source, cwd string) (string, bool, error) {
	msg, err := c.request(ctx, "session/fork", map[string]any{"sessionId": source, "cwd": cwd, "mcpServers": []any{}})
	if err != nil {
		return "", forkRequestRefused(msg), err
	}
	var res struct{ SessionID string }
	if json.Unmarshal(msg.Result, &res) != nil {
		return "", false, ErrForkUnknown
	}
	return res.SessionID, false, nil
}

func copyACPTip(ctx context.Context, client SessionClient, opDir, cwd string, p ForkCheckpoint) (string, error) {
	fork, ok := client.(interface {
		SupportsConversationFork() bool
		forkSession(context.Context, string, string) (string, bool, error)
	})
	if !ok || !fork.SupportsConversationFork() {
		return "", errors.New("当前 OpenCode 接口未声明可恢复的 session/fork")
	}
	r, err := nativeForkID(filepath.Join(opDir, "acp-fork.json"), p.State.ExternalSessionID, p.Turn, nil, func() (string, bool, error) { return fork.forkSession(ctx, p.State.ExternalSessionID, cwd) })
	if err != nil {
		return "", err
	}
	child, err := client.LoadSession(ctx, r.Child, cwd)
	if err != nil {
		return "", err
	}
	if child != r.Child {
		return "", errors.New("OpenCode 未恢复新分支")
	}
	return child, nil
}

func copyCodexCheckpoint(ctx context.Context, rpc func(context.Context, string, any) (rpcEnvelope, error), opDir, cwd string, p ForkCheckpoint) (string, map[string]string, error) {
	source := p.State.ExternalSessionID
	if source == "" || p.Turn == "" {
		return "", nil, errors.New("缺少 Codex 分支位置")
	}
	receipt, err := nativeForkID(filepath.Join(opDir, "codex-fork.json"), source, p.Turn, func() ([]string, error) {
		return codexForkTurns(ctx, rpc, source, p.Turn, 0)
	}, func() (string, bool, error) {
		msg, err := rpc(ctx, "thread/fork", map[string]any{"threadId": source, "lastTurnId": p.Turn, "excludeTurns": true, "cwd": cwd})
		if err != nil {
			return "", forkRequestRefused(msg), err
		}
		var response struct {
			Thread struct{ ID, ForkedFromID string }
		}
		if json.Unmarshal(msg.Result, &response) != nil || response.Thread.ForkedFromID != "" && response.Thread.ForkedFromID != source {
			return "", false, ErrForkUnknown
		}
		return response.Thread.ID, false, nil
	})
	if err != nil {
		return "", nil, err
	}
	parent := receipt.Prefix
	if len(parent) == 0 || parent[len(parent)-1] != p.Turn {
		return "", nil, errors.New("Codex 分支位置凭据无效")
	}

	// Some releases remap native turn IDs. Check ancestry and the whole
	// prefix length before pairing ordinal positions; never compare text.
	msg, err := rpc(ctx, "thread/read", map[string]any{"threadId": receipt.Child, "includeTurns": false})
	if err != nil {
		return "", nil, err
	}
	var info struct {
		Thread struct{ ID, ForkedFromID, HistoryMode string }
	}
	if json.Unmarshal(msg.Result, &info) != nil || info.Thread.ID != receipt.Child || receipt.Child == source || info.Thread.ForkedFromID != "" && info.Thread.ForkedFromID != source {
		return "", nil, errors.New("Codex 未确认新线程来源")
	}
	ids, err := codexForkTurns(ctx, rpc, receipt.Child, "", len(parent))
	if err != nil {
		return "", nil, err
	}
	if len(ids) != len(parent) {
		return "", nil, errors.New("Codex 分支未停在所选回复")
	}
	mapping := make(map[string]string, len(ids))
	for i, id := range parent {
		mapping[id] = ids[i]
	}
	for i, id := range ids {
		if id != parent[i] && (info.Thread.ForkedFromID != source || mapping[id] != "") {
			return "", nil, errors.New("Codex 分支回合顺序不一致")
		}
	}
	return receipt.Child, mapping, nil
}

func codexForkTurns(ctx context.Context, rpc func(context.Context, string, any) (rpcEnvelope, error), id, target string, max int) ([]string, error) {
	var ids []string
	seenIDs, seenPages := map[string]bool{}, map[string]bool{}
	var cursor *string
	for {
		msg, err := rpc(ctx, "thread/turns/list", map[string]any{"threadId": id, "sortDirection": "asc", "limit": 100, "itemsView": "notLoaded", "cursor": cursor})
		if err != nil {
			return nil, err
		}
		var page struct {
			Data       []struct{ ID, Status string }
			NextCursor *string
		}
		if json.Unmarshal(msg.Result, &page) != nil || page.Data == nil {
			return nil, errors.New("Codex 回合分页无效")
		}
		for _, row := range page.Data {
			terminal := row.Status == "completed" || row.Status == "interrupted" || row.Status == "failed"
			if row.ID == "" || seenIDs[row.ID] || !terminal || row.ID == target && row.Status == "failed" {
				return nil, errors.New("Codex 回合尚未结束或目录有误")
			}
			seenIDs[row.ID] = true
			ids = append(ids, row.ID)
			if row.ID == target {
				return ids, nil
			}
			if max > 0 && len(ids) > max {
				return nil, errors.New("Codex 分支包含所选回复之后的回合")
			}
		}
		cursor = page.NextCursor
		if cursor == nil {
			break
		}
		if *cursor == "" || seenPages[*cursor] {
			return nil, errors.New("Codex 分页游标重复")
		}
		seenPages[*cursor] = true
	}
	if target != "" {
		return nil, errors.New("Codex 原线程中找不到所选回复")
	}
	return ids, nil
}
