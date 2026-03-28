package extagent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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
	responses            map[int64]chan rpcEnvelope
	loadSessionSupported bool
	nextID               int64
	mu                   sync.Mutex
	collectors           map[string]*strings.Builder
}

type rpcEnvelope struct {
	ID     *int64          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func NewACPClient(spec CommandSpec, cwd string) (ACPClient, error) {
	if strings.TrimSpace(spec.Command) == "" {
		return nil, fmt.Errorf("acp command is required")
	}
	cmd := exec.Command(spec.Command, spec.Args...)
	cmd.Dir = cwd
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
		responses:  map[int64]chan rpcEnvelope{},
		collectors: map[string]*strings.Builder{},
	}
	go client.readLoop(stdout)
	return client, nil
}

func (c *stdioACPClient) Initialize(ctx context.Context) error {
	response, err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion":   1,
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
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if collector, ok := c.collectors[sessionID]; ok {
		return strings.TrimSpace(collector.String()), nil
	}
	return "", nil
}

func (c *stdioACPClient) Close() error {
	if c == nil || c.cmd == nil || c.cmd.Process == nil {
		return nil
	}
	_ = c.stdin.Close()
	if c.cmd.ProcessState == nil {
		_ = c.cmd.Process.Kill()
	}
	return c.cmd.Wait()
}

func (c *stdioACPClient) request(ctx context.Context, method string, params any) (rpcEnvelope, error) {
	id := atomic.AddInt64(&c.nextID, 1)
	responseCh := make(chan rpcEnvelope, 1)
	c.mu.Lock()
	c.responses[id] = responseCh
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.responses, id)
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
	if _, err := c.stdin.Write(append(payload, '\n')); err != nil {
		return rpcEnvelope{}, err
	}
	select {
	case <-ctx.Done():
		return rpcEnvelope{}, ctx.Err()
	case response := <-responseCh:
		if response.Error != nil {
			return rpcEnvelope{}, fmt.Errorf(response.Error.Message)
		}
		return response, nil
	}
}

func (c *stdioACPClient) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var envelope rpcEnvelope
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			continue
		}
		if envelope.ID != nil {
			c.mu.Lock()
			ch := c.responses[*envelope.ID]
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
