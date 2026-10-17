package extagent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// codexAppServerClient implements Codex's own JSON-RPC protocol, while exposing
// the same session lifecycle and approval bridge as the ACP adapter.
type codexAppServerClient struct {
	cmd               *exec.Cmd
	stdin             io.WriteCloser
	mu                sync.Mutex
	writeMu           sync.Mutex
	nextID            atomic.Int64
	responses         map[string]chan rpcEnvelope
	done              chan struct{}
	waitDone          chan struct{}
	stopOnce          sync.Once
	closeOnce         sync.Once
	readErr           error
	permissionHandler func(context.Context, acpPermissionParams) acpPermissionOutcome
	active            *appServerRun
}

type appServerItem struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Text  string `json:"text"`
	Phase string `json:"phase"`
}

type appServerTurn struct {
	ID     string          `json:"id"`
	Status string          `json:"status"`
	Items  []appServerItem `json:"items"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type appServerProgress struct {
	items     map[string]appServerItem
	order     []string
	completed *appServerTurn
}

type appServerRun struct {
	threadID string
	turnID   string
	ctx      context.Context
	cancel   context.CancelFunc
	turns    map[string]*appServerProgress
	changed  chan struct{}
}

func NewCodexAppServerClient(ctx context.Context, spec CommandSpec, cwd string) (SessionClient, error) {
	if strings.TrimSpace(spec.Command) == "" {
		return nil, fmt.Errorf("codex app-server command is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, spec.Command, spec.Args...)
	cmd.Dir = cwd
	configureExternalProcess(cmd)
	// Stderr is diagnostic, never protocol data. Avoid unbounded buffering.
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = stdout.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	c := &codexAppServerClient{cmd: cmd, stdin: stdin, responses: map[string]chan rpcEnvelope{}, done: make(chan struct{}), waitDone: make(chan struct{})}
	go func() {
		c.readLoop(stdout)
		_ = cmd.Wait()
		close(c.waitDone)
	}()
	return c, nil
}

func (c *codexAppServerClient) Initialize(ctx context.Context) error {
	_, err := c.request(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "bqagent", "title": "bqAgent", "version": "1.0.0"}})
	if err != nil {
		return err
	}
	return c.write(map[string]any{"method": "initialized", "params": map[string]any{}})
}

func (c *codexAppServerClient) LoadSessionSupported() bool { return true }

func (c *codexAppServerClient) NewSession(ctx context.Context, cwd string) (string, error) {
	return c.openThread(ctx, "thread/start", map[string]any{"cwd": cwd})
}

func (c *codexAppServerClient) LoadSession(ctx context.Context, id, cwd string) (string, error) {
	return c.openThread(ctx, "thread/resume", map[string]any{"threadId": id, "cwd": cwd})
}

func (c *codexAppServerClient) openThread(ctx context.Context, method string, params any) (string, error) {
	response, err := c.request(ctx, method, params)
	if err != nil {
		return "", err
	}
	var result struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		return "", err
	}
	if strings.TrimSpace(result.Thread.ID) == "" {
		return "", fmt.Errorf("codex %s returned an empty thread ID", method)
	}
	return result.Thread.ID, nil
}

func (c *codexAppServerClient) Prompt(ctx context.Context, id, prompt string) (string, error) {
	runCtx, cancel := context.WithCancel(ctx)
	run := &appServerRun{threadID: id, ctx: runCtx, cancel: cancel, turns: map[string]*appServerProgress{}, changed: make(chan struct{}, 1)}
	c.mu.Lock()
	if c.active != nil {
		c.mu.Unlock()
		cancel()
		return "", fmt.Errorf("codex app-server turn already active")
	}
	c.active = run
	c.mu.Unlock()
	defer func() {
		cancel()
		c.mu.Lock()
		if c.active == run {
			c.active = nil
		}
		c.mu.Unlock()
	}()
	response, err := c.request(ctx, "turn/start", map[string]any{"threadId": id, "input": []map[string]any{{"type": "text", "text": prompt}}})
	if err != nil {
		// The request may already have reached the server. Closing is the only
		// reliable cancellation if no turn ID was acknowledged.
		_ = c.Close()
		return "", err
	}
	var result struct {
		Turn appServerTurn `json:"turn"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		_ = c.Close()
		return "", err
	}
	if result.Turn.ID == "" {
		_ = c.Close()
		return "", fmt.Errorf("codex turn/start returned an empty turn ID")
	}
	c.mu.Lock()
	run.turnID = result.Turn.ID
	c.mu.Unlock()
	for {
		c.mu.Lock()
		progress := run.turns[result.Turn.ID]
		var completed *appServerTurn
		var reply string
		if progress != nil {
			completed = progress.completed
			if completed != nil {
				reply = progress.reply()
			}
		}
		c.mu.Unlock()
		if completed != nil {
			if completed.Status != "completed" {
				message := completed.Status
				if completed.Error != nil {
					message += ": " + completed.Error.Message
				}
				return "", fmt.Errorf("codex turn %s", message)
			}
			if strings.TrimSpace(reply) == "" {
				return "", fmt.Errorf("codex returned an empty final response")
			}
			return reply, nil
		}
		select {
		case <-run.changed:
		case <-ctx.Done():
			interruptCtx, interruptCancel := context.WithTimeout(context.Background(), time.Second)
			_, _ = c.request(interruptCtx, "turn/interrupt", map[string]string{"threadId": id, "turnId": result.Turn.ID})
			interruptCancel()
			_ = c.Close()
			return "", ctx.Err()
		case <-c.done:
			// Check a queued completion before reporting a process exit.
			c.mu.Lock()
			p := run.turns[result.Turn.ID]
			finished := p != nil && p.completed != nil
			c.mu.Unlock()
			if !finished {
				return "", c.failure()
			}
		}
	}
}

func (p *appServerProgress) record(item appServerItem) {
	if item.Type != "agentMessage" || item.ID == "" {
		return
	}
	if _, exists := p.items[item.ID]; !exists {
		p.order = append(p.order, item.ID)
	}
	p.items[item.ID] = item
}

func (p *appServerProgress) reply() string {
	var final, legacy string
	for _, id := range p.order {
		item := p.items[id]
		if item.Phase == "final_answer" {
			final = item.Text
		}
		if item.Phase == "" {
			legacy = item.Text
		}
	}
	return firstNonEmpty(final, legacy)
}

func (c *codexAppServerClient) notification(envelope rpcEnvelope) {
	var params struct {
		ThreadID string        `json:"threadId"`
		TurnID   string        `json:"turnId"`
		ItemID   string        `json:"itemId"`
		Delta    string        `json:"delta"`
		Item     appServerItem `json:"item"`
		Turn     appServerTurn `json:"turn"`
	}
	if json.Unmarshal(envelope.Params, &params) != nil {
		return
	}
	if envelope.Method == "turn/completed" {
		params.TurnID = params.Turn.ID
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	run := c.active
	if run == nil || params.ThreadID != run.threadID || params.TurnID == "" || (run.turnID != "" && run.turnID != params.TurnID) {
		return
	}
	progress := run.turns[params.TurnID]
	if progress == nil {
		progress = &appServerProgress{items: map[string]appServerItem{}}
		run.turns[params.TurnID] = progress
	}
	switch envelope.Method {
	case "item/started", "item/completed":
		progress.record(params.Item)
	case "item/agentMessage/delta":
		item := progress.items[params.ItemID]
		item.ID, item.Type = params.ItemID, "agentMessage"
		item.Text += params.Delta
		progress.record(item)
	case "turn/completed":
		for _, item := range params.Turn.Items {
			progress.record(item)
		}
		progress.completed = &params.Turn
	default:
		return
	}
	select {
	case run.changed <- struct{}{}:
	default:
	}
}

func (c *codexAppServerClient) setContextPermissionHandler(handler func(context.Context, acpPermissionParams) acpPermissionOutcome) {
	c.mu.Lock()
	c.permissionHandler = handler
	c.mu.Unlock()
}

func (c *codexAppServerClient) Close() error {
	c.closeOnce.Do(func() {
		c.stop(io.ErrClosedPipe)
		_ = c.stdin.Close()
		if c.cmd.Cancel != nil {
			_ = c.cmd.Cancel()
		} else {
			_ = c.cmd.Process.Kill()
		}
		<-c.waitDone
	})
	return nil
}

func (c *codexAppServerClient) stop(err error) {
	c.stopOnce.Do(func() {
		c.mu.Lock()
		c.readErr = fmt.Errorf("codex app-server process exited: %w", err)
		if c.active != nil {
			c.active.cancel()
		}
		c.mu.Unlock()
		close(c.done)
	})
}

func (c *codexAppServerClient) failure() error { c.mu.Lock(); defer c.mu.Unlock(); return c.readErr }

func (c *codexAppServerClient) request(ctx context.Context, method string, params any) (rpcEnvelope, error) {
	id := c.nextID.Add(1)
	key := fmt.Sprint(id)
	ch := make(chan rpcEnvelope, 1)
	c.mu.Lock()
	c.responses[key] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.responses, key); c.mu.Unlock() }()
	select {
	case <-c.done:
		return rpcEnvelope{}, c.failure()
	case <-ctx.Done():
		return rpcEnvelope{}, ctx.Err()
	default:
	}
	if err := c.write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return rpcEnvelope{}, err
	}
	var response rpcEnvelope
	select {
	case response = <-ch:
	case <-ctx.Done():
		return rpcEnvelope{}, ctx.Err()
	case <-c.done:
		select {
		case response = <-ch:
		default:
			return rpcEnvelope{}, c.failure()
		}
	}
	if response.Error != nil {
		return rpcEnvelope{}, errors.New(response.Error.Message)
	}
	return response, nil
}

func (c *codexAppServerClient) write(payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.stdin.Write(append(data, '\n'))
	return err
}

func (c *codexAppServerClient) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, acpScannerInitialBufferSize), maxACPProtocolMessageBytes)
	for scanner.Scan() {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var envelope rpcEnvelope
		if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
			c.stop(fmt.Errorf("invalid JSON-RPC: %w", err))
			return
		}
		if len(envelope.ID) > 0 && envelope.Method != "" {
			go c.serverRequest(envelope)
			continue
		}
		if len(envelope.ID) > 0 {
			c.mu.Lock()
			ch := c.responses[string(bytes.TrimSpace(envelope.ID))]
			c.mu.Unlock()
			if ch != nil {
				select {
				case ch <- envelope:
				default:
				}
			}
		} else {
			c.notification(envelope)
		}
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	c.stop(err)
}

func (c *codexAppServerClient) serverRequest(envelope rpcEnvelope) {
	var params struct {
		ThreadID           string            `json:"threadId"`
		TurnID             string            `json:"turnId"`
		ItemID             string            `json:"itemId"`
		Command            string            `json:"command"`
		Reason             string            `json:"reason"`
		Permissions        json.RawMessage   `json:"permissions"`
		AvailableDecisions []json.RawMessage `json:"availableDecisions"`
	}
	if json.Unmarshal(envelope.Params, &params) != nil {
		_ = c.write(map[string]any{"id": envelope.ID, "error": map[string]any{"code": -32602, "message": "invalid request params"}})
		return
	}
	options := []ACPPermissionOption{{OptionID: "accept", Name: "允许本次", Kind: "allow_once"}, {OptionID: "acceptForSession", Name: "本会话允许", Kind: "allow_always"}, {OptionID: "decline", Name: "拒绝", Kind: "reject_once"}, {OptionID: "cancel", Name: "取消", Kind: "reject_once"}}
	permissions := envelope.Method == "item/permissions/requestApproval"
	switch envelope.Method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		if len(params.AvailableDecisions) > 0 {
			filtered := []ACPPermissionOption{}
			for _, raw := range params.AvailableDecisions {
				var decision string
				if json.Unmarshal(raw, &decision) != nil {
					continue
				}
				for _, option := range options {
					if option.OptionID == decision {
						filtered = append(filtered, option)
					}
				}
			}
			options = filtered
		}
	case "item/permissions/requestApproval":
		options = []ACPPermissionOption{{OptionID: "accept", Name: "允许本次请求的权限", Kind: "allow_once"}, {OptionID: "decline", Name: "拒绝", Kind: "reject_once"}}
	default:
		_ = c.write(map[string]any{"id": envelope.ID, "error": map[string]any{"code": -32601, "message": "bqAgent does not support this app-server request: " + envelope.Method}})
		return
	}
	c.mu.Lock()
	handler, run := c.permissionHandler, c.active
	valid := run != nil && run.ctx.Err() == nil && run.threadID == params.ThreadID && (run.turnID == "" || run.turnID == params.TurnID)
	c.mu.Unlock()
	decision := "cancel"
	if handler != nil && valid {
		tool, _ := json.Marshal(map[string]any{"toolCallId": params.ItemID, "title": firstNonEmpty(params.Command, params.Reason, envelope.Method), "rawInput": json.RawMessage(envelope.Params)})
		outcome := handler(run.ctx, acpPermissionParams{SessionID: params.ThreadID, ToolCall: tool, Options: options})
		if outcome.Outcome == "selected" && run.ctx.Err() == nil {
			for _, option := range options {
				if option.OptionID == outcome.OptionID {
					decision = option.OptionID
				}
			}
		}
	}
	result := map[string]any{"decision": decision}
	if permissions {
		result = map[string]any{"permissions": map[string]any{}, "scope": "turn"}
		if decision == "accept" && len(params.Permissions) > 0 {
			result["permissions"] = params.Permissions
		}
	}
	_ = c.write(map[string]any{"id": envelope.ID, "result": result})
}
