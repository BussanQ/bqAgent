package extagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"bqagent/internal/session"
)

// The caller holds the exclusive context lock through provider confirmation
// and the atomic switch to the retained local history.
func (b *Broker) RewindCodex(ctx context.Context, saved *session.Session, p session.PendingRewind, turns []string, cwd string, prepare func(*session.Session) error) error {
	if b == nil || b.store == nil {
		return errors.New("Codex 未配置")
	}
	lock := b.sessionLock(saved.ID())
	if !lock.TryLock() {
		return session.ErrContextBusy
	}
	defer lock.Unlock()
	if err := b.waitForDetections(ctx); err != nil {
		return err
	}
	record, err := b.store.loadSingle(saved.ID())
	if err != nil {
		return err
	}
	if record.Agent != AgentCodex || record.Transport != TransportAppServer || record.ExternalSessionID != p.Thread {
		return errors.New("原 Codex 线程已更换，无法回退")
	}
	t, err := transportForSession(b.Detection(AgentCodex), record.SessionState)
	if err != nil || t.Kind != TransportAppServer {
		return errors.New("原 Codex App Server 不可用")
	}
	generation, err := b.sessionGeneration(saved.ID())
	if err != nil {
		return err
	}
	client, err := b.sessionClient(ctx, saved.ID(), AgentCodex, t.Command, cwd, generation, TransportAppServer)
	if err != nil {
		return err
	}
	c, ok := client.(*codexAppServerClient)
	if !ok {
		return errors.New("Codex 接口不支持回退")
	}
	id, err := c.LoadSession(ctx, p.Thread, cwd)
	if err != nil {
		if c.failure() != nil || isACPConnectionError(err) {
			b.discardACPClient(saved.ID(), AgentCodex, client)
		}
		return err
	}
	if id != p.Thread {
		return errors.New("Codex 恢复了其他线程")
	}
	if err := c.rewind(ctx, saved, p, turns); err != nil {
		if c.failure() != nil || isACPConnectionError(err) || errors.Is(err, context.DeadlineExceeded) {
			b.discardACPClient(saved.ID(), AgentCodex, client)
		}
		return err
	}
	err = saved.RewindContext(p.Operation, string(AgentCodex), p.Target, func(next *session.Session) error {
		if err := writeSingleRecord(filepath.Join(next.ContextDir(), "external-agent.json"), record); err != nil {
			return err
		}
		return prepare(next)
	})
	if err == nil {
		for _, old := range b.invalidateSession(saved.ID()) {
			_ = old.Close()
		}
	}
	return err
}

func (c *codexAppServerClient) rewind(ctx context.Context, s *session.Session, p session.PendingRewind, turns []string) error {
	prior, err := s.PendingRewind()
	if err != nil {
		return err
	}
	if prior != nil {
		if prior.Operation != p.Operation || prior.Target != p.Target || prior.Thread != p.Thread || prior.Before != p.Before {
			return session.ErrRewindPending
		}
		p = *prior
	}
	applied, err := c.readRewindCut(ctx, &p, turns, prior != nil)
	if err != nil || applied {
		return err
	}
	// A different Codex client can start a turn while we page through history.
	if err := c.requireRewindIdle(ctx, p.Thread); err != nil {
		return err
	}
	tip, err := c.request(ctx, "thread/turns/list", map[string]any{"threadId": p.Thread, "sortDirection": "desc", "limit": 1})
	if err != nil {
		return err
	}
	var latest struct{ Data []struct{ ID string } }
	if json.Unmarshal(tip.Result, &latest) != nil || len(latest.Data) == 0 || latest.Data[0].ID != p.Head {
		return errors.New("Codex 的最新回合已变更，请重新选择消息")
	}
	if err := s.SavePendingRewind(p); err != nil {
		return err
	}
	reply, err := c.request(ctx, "thread/revert", map[string]string{"threadId": p.Thread, "beforeTurnId": p.Before})
	if err != nil {
		if reply.Error != nil && (reply.Error.Code == -32601 || reply.Error.Code == -32602 || reply.Error.Message == "thread/revert only supports paginated threads") {
			return errors.Join(fmt.Errorf("Codex 不支持此次回退：%w", err), s.RemovePendingRewind())
		}
		return errors.Join(session.ErrRewindPending, err)
	}
	var result struct{ Thread struct{ ID string } }
	if json.Unmarshal(reply.Result, &result) != nil || result.Thread.ID != p.Thread {
		return session.ErrRewindPending
	}
	applied, err = c.readRewindCut(ctx, &p, turns, true)
	if err != nil || !applied {
		return errors.Join(session.ErrRewindPending, err)
	}
	return nil
}

// Walk only the removed tail plus its predecessor. A turn created by another
// client must never be silently included in the cut. On recovery the exact
// retained tip (including an empty thread) proves a lost revert succeeded.
func (c *codexAppServerClient) requireRewindIdle(ctx context.Context, thread string) error {
	meta, err := c.request(ctx, "thread/read", map[string]any{"threadId": thread, "includeTurns": false})
	if err != nil {
		return err
	}
	var info struct {
		Thread struct {
			ID, HistoryMode string
			Status          struct{ Type string }
		}
	}
	if json.Unmarshal(meta.Result, &info) != nil || info.Thread.ID != thread || info.Thread.HistoryMode != "paginated" {
		return errors.New("该 Codex 线程未提供可回退的分页历史")
	}
	if info.Thread.Status.Type != "idle" {
		return errors.New("Codex 原生线程尚未空闲")
	}
	return nil
}

func (c *codexAppServerClient) readRewindCut(ctx context.Context, p *session.PendingRewind, turns []string, recovering bool) (bool, error) {
	if len(turns) == 0 || turns[0] != p.Before {
		return false, errors.New("缺少原生回合位置")
	}
	if err := c.requireRewindIdle(ctx, p.Thread); err != nil {
		return false, err
	}
	var cursor *string
	seen := make(map[string]bool)
	count := 0
	var head string
	for {
		msg, err := c.request(ctx, "thread/turns/list", map[string]any{"threadId": p.Thread, "sortDirection": "desc", "limit": 100, "cursor": cursor})
		if err != nil {
			return false, err
		}
		var page struct {
			Data       []struct{ ID string }
			NextCursor *string
		}
		if json.Unmarshal(msg.Result, &page) != nil || page.Data == nil {
			return false, errors.New("Codex 回合目录无效")
		}
		for _, row := range page.Data {
			if row.ID == "" {
				return false, errors.New("Codex 回合缺少 ID")
			}
			if count == 0 {
				head = row.ID
				if recovering && head == p.Kept {
					return true, nil
				}
			}
			if count == len(turns) {
				if recovering && (head != p.Head || row.ID != p.Kept) {
					return false, session.ErrRewindPending
				}
				p.Head, p.Kept = head, row.ID
				return false, nil
			}
			if row.ID != turns[len(turns)-1-count] {
				return false, errors.New("Codex 原生历史与本次回退范围不一致")
			}
			count++
		}
		if page.NextCursor == nil {
			if recovering && count == 0 && p.Kept == "" {
				return true, nil
			}
			if count != len(turns) || recovering && (p.Head != head || p.Kept != "") {
				return false, errors.New("无法确认 Codex 回退位置")
			}
			p.Head, p.Kept = head, ""
			return false, nil
		}
		cursor = page.NextCursor
		if *cursor == "" || seen[*cursor] {
			return false, errors.New("Codex 回合分页游标无效")
		}
		seen[*cursor] = true
	}
}
