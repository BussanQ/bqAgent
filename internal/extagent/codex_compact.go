package extagent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

// thread/compact/start returns no turn ID. The thread's next turn/started
// is a candidate. A contextCompaction item (or the legacy compacted event)
// ties the operation to a specific native turn even if shared-client events
// arrive in between. All observation happens under the client's mutex.
type codexCompactRun struct {
	usage                                *ContextUsage
	threadID, candidate, turnID, failure string
	marked, finished                     bool
	identified, done                     chan struct{}
	reply                                string
	err                                  error
}

func (r *codexCompactRun) identify(id string) bool {
	if r.turnID == "" && id != "" {
		if r.candidate != "" && r.candidate != id {
			return false
		}
		r.turnID = id
		close(r.identified)
	}
	return id != "" && r.turnID == id
}

func (r *codexCompactRun) observe(method string, p appServerNotification) {
	if r.finished || p.ThreadID != r.threadID {
		return
	}
	id := p.TurnID
	switch method {
	case "thread/tokenUsage/updated":
		if id != "" && id == firstNonEmpty(r.turnID, r.candidate) {
			if u := p.contextUsage(); u != nil {
				r.usage = u
			}
		}
	case "turn/started":
		if r.candidate == "" && id != "" {
			r.candidate = id
		}
	case "item/started", "item/completed":
		if p.Item.Type == "contextCompaction" && r.identify(id) && method == "item/completed" {
			r.usage = nil
			r.marked = true
		}
	case "thread/compacted":
		if id == "" {
			id = firstNonEmpty(r.turnID, r.candidate)
		}
		if r.identify(id) {
			r.usage = nil
			r.marked = true
		}
	case "error":
		if !p.WillRetry && id != "" && id == firstNonEmpty(r.turnID, r.candidate) {
			r.failure = p.Error.Message
		}
	case "turn/completed":
		if id == "" {
			return
		}
		compactItem := slices.ContainsFunc(p.Turn.Items, func(item appServerItem) bool { return item.Type == "contextCompaction" })
		if compactItem && r.identify(id) && p.Turn.Status == "completed" {
			r.marked = true
		}
		if id != firstNonEmpty(r.turnID, r.candidate) {
			return
		}
		if r.turnID == "" && len(p.Turn.Items) > 0 && !compactItem {
			// A different client completed an ordinary turn in this thread.
			r.candidate, r.failure = "", ""
			return
		}
		if p.Turn.Error != nil {
			r.failure = firstNonEmpty(p.Turn.Error.Message, r.failure)
		}
		r.finished = true
		switch {
		case r.marked:
			r.reply = "Codex 会话上下文已压缩。"
			if r.failure != "" {
				r.reply += "\n" + r.failure
			}
		case p.Turn.Status == "interrupted":
			r.err = context.Canceled
		case r.failure != "":
			r.err = fmt.Errorf("Codex 压缩失败：%s", r.failure)
		case p.Turn.Status != "completed":
			r.err = fmt.Errorf("Codex 压缩回合结束：%s", p.Turn.Status)
		default:
			r.err = errors.New("Codex 回合已结束，但未确认上下文压缩完成")
		}
		close(r.done)
	}
}

func (c *codexAppServerClient) compactThread(ctx context.Context, id, cwd string, command *CompactCommand) (string, error) {
	resumed, err := c.openThread(ctx, "thread/resume", map[string]any{"threadId": id, "cwd": cwd}, true)
	if err != nil {
		return "", err
	}
	if resumed != id {
		return "", errors.New("Codex 恢复的线程与当前会话不一致")
	}

	r := &codexCompactRun{threadID: id, identified: make(chan struct{}), done: make(chan struct{})}
	c.mu.Lock()
	if c.active != nil || c.compaction != nil {
		c.mu.Unlock()
		return "", errors.New("Codex 会话已有运行中的任务")
	}
	c.compaction = r
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.compaction = nil
		var reading *ContextUsage
		if r.marked {
			idleSink, _ := ctx.Value(idleUsageKey{}).(usageSink)
			c.lastUsage = codexUsageTarget{thread: id, turn: r.turnID, model: c.threadOptions.Model, report: idleSink}
			reading = r.usage
			if reading == nil {
				reading = &ContextUsage{}
			}
			if reading.Captured == 0 {
				c.stampUsage(reading)
			}
			if reading.Window > 0 {
				reading.Model = c.threadOptions.Model
			}
		}
		c.mu.Unlock()
		if sink := contextUsageSink(ctx); sink != nil && reading != nil {
			sink(id, *reading)
		}
	}()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if command.Started != nil {
		command.Started()
	}
	_, err = c.request(ctx, "thread/compact/start", map[string]string{"threadId": id})
	// Notifications can precede the acknowledgement. A refusal to start is
	// definitive, but an acknowledgement alone cannot complete the command.
	if err != nil && ctx.Err() == nil {
		return "", err
	}
	select {
	case <-r.done:
		return r.reply, r.err
	case <-ctx.Done():
		return c.stopCompact(ctx, r)
	case <-c.done:
		select {
		case <-r.done:
			return r.reply, r.err
		default:
			return "", c.failure()
		}
	}
}

func (c *codexAppServerClient) stopCompact(ctx context.Context, r *codexCompactRun) (string, error) {
	stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	select {
	case <-r.done:
		return r.reply, r.err
	case <-c.done:
		return "", c.failure()
	case <-stop.Done():
		return "", errors.New("已停止等待，Codex 尚未公布可中断的压缩回合")
	case <-r.identified:
	}
	// Interrupt only the turn that reported compaction. Stop can arrive before
	// the operation's acknowledgement or even before its first item.
	select {
	case <-r.done:
		return r.reply, r.err
	default:
	}
	_, err := c.request(stop, "turn/interrupt", map[string]string{"threadId": r.threadID, "turnId": r.turnID})
	if err != nil {
		select {
		case <-r.done:
			return r.reply, r.err
		default:
		}
		return "", fmt.Errorf("已停止等待，Codex 未确认中断压缩：%w", err)
	}
	select {
	case <-r.done:
		return r.reply, r.err
	case <-c.done:
		return "", c.failure()
	case <-stop.Done():
		return "", errors.New("Codex 已收到中断请求，压缩回合尚未确认结束")
	}
}
