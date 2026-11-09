package extagent

import (
	"context"
	"encoding/json"
	"strconv"
)

type codexPromptHandler func(context.Context, acpPermissionParams) acpPermissionOutcome

type codexPrompt struct {
	receipt      *permissionReceipt
	thread, turn string
	nonblocking  bool
	fallback     any
	ask          func(context.Context, codexPromptHandler) any
	settled      func(bool, *appServerRun)
}

type pendingCodexPrompt struct {
	run    *appServerRun
	ctx    context.Context
	cancel context.CancelFunc
	spec   codexPrompt
}

func (c *codexAppServerClient) startServerRequest(e rpcEnvelope) {
	// Replayed requests, including malformed copies, cannot invalidate an
	// already-pending callback with the same native ID.
	c.mu.Lock()
	pending := c.prompts[codexRequestKey(e.ID)] != nil
	c.mu.Unlock()
	if pending {
		return
	}
	switch e.Method {
	case "item/tool/requestUserInput":
		c.startQuestions(e)
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval":
		c.startApproval(e)
	case "mcpServer/elicitation/request":
		c.startMCPElicitation(e)
	default:
		c.writeImmediate(map[string]any{"id": e.ID, "error": rpcError{Code: -32601, Message: "bqAgent does not support this app-server request: " + e.Method}})
	}
}

func (c *codexAppServerClient) invalidPrompt(id json.RawMessage) {
	c.writeImmediate(map[string]any{"id": id, "error": rpcError{Code: -32602, Message: "invalid Codex prompt parameters"}})
}

func (c *codexAppServerClient) writeImmediate(payload any) {
	if err := c.write(payload); err != nil {
		c.stop(err)
	}
}

func (c *codexAppServerClient) startPrompt(id json.RawMessage, spec codexPrompt) {
	key := codexRequestKey(id)
	if key == "" {
		c.invalidPrompt(id)
		return
	}
	c.mu.Lock()
	if c.prompts[key] != nil {
		c.mu.Unlock()
		return
	}
	if len(c.prompts) >= 64 {
		c.mu.Unlock()
		c.writeImmediate(map[string]any{"id": id, "result": spec.fallback})
		return
	}
	run, handler := c.active, c.permissionHandler
	p := &pendingCodexPrompt{spec: spec}
	if run != nil && !run.drained && run.ctx.Err() == nil && run.threadID == spec.thread && spec.turn != "" &&
		(run.turnID == "" || run.turnID == spec.turn) {
		p.run = run
		p.ctx, p.cancel = context.WithCancel(run.ctx)
		run.promptWorkers.Add(1)
	}
	if c.prompts == nil {
		c.prompts = make(map[string]*pendingCodexPrompt)
	}
	c.prompts[key] = p
	c.mu.Unlock()
	go func() {
		var result any
		if p.run != nil {
			defer p.run.promptWorkers.Done()
			defer p.cancel()
			if handler != nil && spec.ask != nil && c.ownsPrompt(p.ctx, p.run, spec.thread, spec.turn) {
				result = spec.ask(p.ctx, handler)
			}
		}
		delivered, err := c.writePromptReply(id, key, p, result)
		if err != nil {
			c.stop(err)
		}
		c.mu.Lock()
		if c.prompts[key] == p {
			delete(c.prompts, key)
		}
		c.mu.Unlock()
		if spec.settled != nil {
			spec.settled(delivered, p.run)
		}
	}()
}

// Re-check ownership after acquiring the writer. A withdrawal may have
// arrived, or the same wire ID may already belong to another request.
func (c *codexAppServerClient) writePromptReply(id json.RawMessage, key string, p *pendingCodexPrompt, result any) (bool, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	select {
	case <-c.done:
		return false, nil
	default:
	}
	c.mu.Lock()
	if c.prompts[key] != p {
		c.mu.Unlock()
		return false, nil
	}
	if result != nil && p.run != nil {
		progress := p.run.turns[p.spec.turn]
		if c.active != p.run || p.run.turnID != p.spec.turn || p.ctx.Err() != nil || p.run.drained || progress != nil && progress.completed != nil {
			result = nil
		}
	}
	c.mu.Unlock()
	chosen := result != nil && (p.spec.receipt == nil || !p.spec.receipt.revoked.Load())
	if !chosen {
		result = p.spec.fallback
	}
	err := c.writeLocked(map[string]any{"id": id, "result": result})
	return chosen && err == nil, err
}

func (c *codexAppServerClient) blockingPrompt() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range c.prompts {
		if p.spec.ask != nil && p.run != nil && p.run.turnID == p.spec.turn && p.ctx.Err() == nil && !p.spec.nonblocking {
			return true
		}
	}
	return false
}

func (c *codexAppServerClient) resolvePrompt(raw json.RawMessage) {
	var event struct {
		ThreadID  string
		RequestID json.RawMessage
	}
	if json.Unmarshal(raw, &event) != nil {
		return
	}
	key := codexRequestKey(event.RequestID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if p := c.prompts[key]; p != nil && p.spec.thread == event.ThreadID {
		delete(c.prompts, key)
		if p.cancel != nil {
			p.cancel()
		}
	}
}

func codexRequestKey(raw json.RawMessage) string {
	if len(raw) == 0 || len(raw) > 4096 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return "s/" + s
		}
	} else if n, err := strconv.ParseInt(string(raw), 10, 64); err == nil {
		return "n/" + strconv.FormatInt(n, 10)
	}
	return ""
}
