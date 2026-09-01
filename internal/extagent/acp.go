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
)

type ACPClientFactory func(CommandSpec, string) (ACPClient, error)

type stdioACPClient struct {
	cmd                  *exec.Cmd
	stdin                io.WriteCloser
	responses            map[string]chan rpcEnvelope
	loadSessionSupported bool
	nextID               int64
	mu                   sync.Mutex
	writeMu              sync.Mutex
	collectors           map[string]*strings.Builder
	permissionHandler    acpPermissionHandler
}

type rpcEnvelope struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Message string `json:"message"`
}

type acpPermissionParams struct {
	SessionID  string                `json:"sessionId"`
	ToolCall   json.RawMessage       `json:"toolCall"`
	Options    []ACPPermissionOption `json:"-"`
	RawOptions []struct {
		OptionID string `json:"optionId"`
		Name     string `json:"name"`
		Kind     string `json:"kind"`
	} `json:"options"`
}

type acpPermissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

type acpPermissionHandler func(acpPermissionParams) acpPermissionOutcome

type acpPermissionAware interface {
	setPermissionHandler(acpPermissionHandler)
}

func NewACPClient(spec CommandSpec, cwd string) (ACPClient, error) {
	if strings.TrimSpace(spec.Command) == "" {
		return nil, fmt.Errorf("acp command is required")
	}
	cmd := exec.CommandContext(context.Background(), spec.Command, spec.Args...)
	cmd.Dir = cwd
	configureExternalProcess(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	client := &stdioACPClient{
		cmd:        cmd,
		stdin:      stdin,
		responses:  map[string]chan rpcEnvelope{},
		collectors: map[string]*strings.Builder{},
	}
	go client.readLoop(stdout)
	return client, nil
}

func (c *stdioACPClient) Initialize(ctx context.Context) error {
	response, err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{},
	})
	if err != nil {
		return err
	}
	var result struct {
		AgentCapabilities struct {
			LoadSession bool `json:"loadSession"`
		} `json:"agentCapabilities"`
	}
	if len(response.Result) > 0 {
		if err := json.Unmarshal(response.Result, &result); err != nil {
			return err
		}
	}
	c.loadSessionSupported = result.AgentCapabilities.LoadSession
	return nil
}

func (c *stdioACPClient) LoadSessionSupported() bool {
	return c != nil && c.loadSessionSupported
}

func (c *stdioACPClient) NewSession(ctx context.Context, cwd string) (string, error) {
	response, err := c.request(ctx, "session/new", map[string]any{
		"cwd":        cwd,
		"mcpServers": []any{},
	})
	if err != nil {
		return "", err
	}
	var result struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		return "", err
	}
	if strings.TrimSpace(result.SessionID) == "" {
		return "", fmt.Errorf("acp session/new returned empty sessionId")
	}
	return result.SessionID, nil
}

func (c *stdioACPClient) LoadSession(ctx context.Context, sessionID, cwd string) (string, error) {
	response, err := c.request(ctx, "session/load", map[string]any{
		"sessionId":  sessionID,
		"cwd":        cwd,
		"mcpServers": []any{},
	})
	if err != nil {
		return "", err
	}
	var result struct {
		SessionID string `json:"sessionId"`
	}
	if len(response.Result) == 0 {
		return sessionID, nil
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		return "", err
	}
	if strings.TrimSpace(result.SessionID) == "" {
		return sessionID, nil
	}
	return result.SessionID, nil
}

func (c *stdioACPClient) Prompt(ctx context.Context, sessionID, prompt string) (string, error) {
	c.mu.Lock()
	c.collectors[sessionID] = &strings.Builder{}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.collectors, sessionID)
		c.mu.Unlock()
	}()
	_, err := c.request(ctx, "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt": []map[string]any{
			{"type": "text", "text": prompt},
		},
	})
	if err != nil {
		if ctx.Err() != nil {
			_ = c.notify("session/cancel", map[string]any{"sessionId": sessionID})
		}
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if collector, ok := c.collectors[sessionID]; ok {
		return strings.TrimSpace(collector.String()), nil
	}
	return "", nil
}

func (c *stdioACPClient) setPermissionHandler(handler acpPermissionHandler) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.permissionHandler = handler
	c.mu.Unlock()
}

func (c *stdioACPClient) Close() error {
	if c == nil || c.cmd == nil || c.cmd.Process == nil {
		return nil
	}
	_ = c.stdin.Close()
	if c.cmd.ProcessState == nil {
		if c.cmd.Cancel != nil {
			_ = c.cmd.Cancel()
		} else {
			_ = c.cmd.Process.Kill()
		}
	}
	return c.cmd.Wait()
}

func (c *stdioACPClient) request(ctx context.Context, method string, params any) (rpcEnvelope, error) {
	id := atomic.AddInt64(&c.nextID, 1)
	responseKey := fmt.Sprintf("%d", id)
	responseCh := make(chan rpcEnvelope, 1)
	c.mu.Lock()
	c.responses[responseKey] = responseCh
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.responses, responseKey)
		c.mu.Unlock()
	}()

	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return rpcEnvelope{}, err
	}
	if err := c.writePayload(payload); err != nil {
		return rpcEnvelope{}, err
	}
	select {
	case <-ctx.Done():
		return rpcEnvelope{}, ctx.Err()
	case response := <-responseCh:
		if response.Error != nil {
			return rpcEnvelope{}, errors.New(response.Error.Message)
		}
		return response, nil
	}
}

func (c *stdioACPClient) notify(method string, params any) error {
	payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	if err != nil {
		return err
	}
	return c.writePayload(payload)
}

func (c *stdioACPClient) writePayload(payload []byte) error {
	if c == nil || c.stdin == nil {
		return io.ErrClosedPipe
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err := c.stdin.Write(append(payload, '\n'))
	return err
}

func (c *stdioACPClient) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	defer func() {
		err := scanner.Err()
		if err == nil {
			err = io.EOF
		}
		c.failPendingRequests(fmt.Errorf("ACP process exited: %w", err))
	}()
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var envelope rpcEnvelope
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			continue
		}
		if len(envelope.ID) > 0 && envelope.Method != "" {
			go c.handleAgentRequest(envelope)
			continue
		}
		if len(envelope.ID) > 0 {
			responseKey := string(bytes.TrimSpace(envelope.ID))
			c.mu.Lock()
			ch := c.responses[responseKey]
			c.mu.Unlock()
			if ch != nil {
				ch <- envelope
			}
			continue
		}
		if envelope.Method == "session/update" {
			c.handleSessionUpdate(envelope.Params)
		}
	}
}

func (c *stdioACPClient) failPendingRequests(err error) {
	if c == nil || err == nil {
		return
	}
	c.mu.Lock()
	channels := make([]chan rpcEnvelope, 0, len(c.responses))
	for _, responseCh := range c.responses {
		channels = append(channels, responseCh)
	}
	c.mu.Unlock()
	failure := rpcEnvelope{Error: &rpcError{Message: err.Error()}}
	for _, responseCh := range channels {
		select {
		case responseCh <- failure:
		default:
		}
	}
}

func (c *stdioACPClient) handleAgentRequest(envelope rpcEnvelope) {
	if envelope.Method != "session/request_permission" {
		_ = c.writeRPCError(envelope.ID, -32601, "method not found")
		return
	}
	var params acpPermissionParams
	if err := json.Unmarshal(envelope.Params, &params); err != nil {
		_ = c.writeRPCError(envelope.ID, -32602, "invalid permission request")
		return
	}
	params.Options = make([]ACPPermissionOption, 0, len(params.RawOptions))
	for _, option := range params.RawOptions {
		params.Options = append(params.Options, ACPPermissionOption{OptionID: option.OptionID, Name: option.Name, Kind: option.Kind})
	}
	c.mu.Lock()
	handler := c.permissionHandler
	c.mu.Unlock()
	outcome := acpPermissionOutcome{Outcome: "cancelled"}
	if handler != nil {
		outcome = handler(params)
	}
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(envelope.ID),
		"result":  map[string]any{"outcome": outcome},
	})
	if err == nil {
		_ = c.writePayload(payload)
	}
}

func (c *stdioACPClient) writeRPCError(id json.RawMessage, code int, message string) error {
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"error":   map[string]any{"code": code, "message": message},
	})
	if err != nil {
		return err
	}
	return c.writePayload(payload)
}

func (c *stdioACPClient) handleSessionUpdate(raw json.RawMessage) {
	var payload struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			SessionUpdate string          `json:"sessionUpdate"`
			Content       json.RawMessage `json:"content"`
		} `json:"update"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return
	}
	// ACP uses session/update for thoughts, tool activity, and extension-defined
	// updates too. Only agent_message_chunk is user-visible assistant output.
	if payload.Update.SessionUpdate != "agent_message_chunk" {
		return
	}
	text := extractACPText(payload.Update.Content)
	if strings.TrimSpace(text) == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if collector := c.collectors[payload.SessionID]; collector != nil {
		collector.WriteString(text)
	}
}

func extractACPText(raw json.RawMessage) string {
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return ""
	}
	return extractTextRecursive(generic)
}

func extractTextRecursive(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case map[string]any:
		if text, ok := typed["text"].(string); ok {
			return text
		}
		if content, ok := typed["content"]; ok {
			return extractTextRecursive(content)
		}
	case []any:
		var builder strings.Builder
		for _, item := range typed {
			builder.WriteString(extractTextRecursive(item))
		}
		return builder.String()
	}
	return ""
}
