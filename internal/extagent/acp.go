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

	"bqagent/internal/media"
)

// ACPClientFactory creates a client whose subprocess lifetime is bound to ctx.
// Callers must Close the client; cancelling ctx also terminates the process.
type ACPClientFactory func(context.Context, CommandSpec, string) (ACPClient, error)

const (
	acpScannerInitialBufferSize = 64 << 10
	maxACPProtocolMessageBytes  = 16 << 20
)

type stdioACPClient struct {
	cmd                    *exec.Cmd
	stdin                  io.WriteCloser
	responses              map[string]chan rpcEnvelope
	loadSessionSupported   bool
	imagePromptsSupported  bool
	closeSessionSupported  bool
	deleteSessionSupported bool
	forkSessionSupported   bool
	modelSessionID         string
	usageSessionID         string
	contextReading         *ContextUsage
	modelOptions           acpModels
	modelsChanged          chan struct{}
	commandReports         map[string][]NativeCommand
	commandsChanged        chan struct{}
	nextID                 int64
	mu                     sync.Mutex
	writeMu                sync.Mutex
	collectors             map[string]*strings.Builder
	plans                  map[string]*PlanUpdate
	activity               map[string]ActivitySink
	permissionHandler      acpPermissionHandler
}

type rpcEnvelope struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message"`
}

type acpPermissionParams struct {
	published  func()
	receipt    *permissionReceipt
	Dialog     *AgentDialog          `json:"-"`
	SessionID  string                `json:"sessionId"`
	ToolCall   json.RawMessage       `json:"toolCall"`
	Options    []ACPPermissionOption `json:"-"`
	RawOptions []struct {
		OptionID string `json:"optionId"`
		Name     string `json:"name"`
		Kind     string `json:"kind"`
	} `json:"options"`
}

// Native prompts publish their disposition only after the originating RPC
// writer confirms the decision. Other adapters resolve at user submission.
type permissionReceipt struct {
	revoked atomic.Bool
	publish func(delivered bool)
}

type acpPermissionOutcome struct {
	Answers  []QuestionAnswer `json:"-"`
	Value    *string          `json:"-"`
	Outcome  string           `json:"outcome"`
	OptionID string           `json:"optionId,omitempty"`
}

type acpPermissionHandler func(acpPermissionParams) acpPermissionOutcome

type acpPermissionAware interface {
	setPermissionHandler(acpPermissionHandler)
}

func NewACPClient(ctx context.Context, spec CommandSpec, cwd string) (ACPClient, error) {
	if strings.TrimSpace(spec.Command) == "" {
		return nil, fmt.Errorf("acp command is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, spec.Command, spec.Args...)
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
			LoadSession        bool `json:"loadSession"`
			PromptCapabilities struct {
				Image bool `json:"image"`
			} `json:"promptCapabilities"`
			SessionCapabilities struct {
				Close  *struct{} `json:"close"`
				Delete *struct{} `json:"delete"`
				Fork   *struct{} `json:"fork"`
			} `json:"sessionCapabilities"`
		} `json:"agentCapabilities"`
	}
	if len(response.Result) > 0 {
		if err := json.Unmarshal(response.Result, &result); err != nil {
			return err
		}
	}
	c.loadSessionSupported = result.AgentCapabilities.LoadSession
	c.imagePromptsSupported = result.AgentCapabilities.PromptCapabilities.Image
	c.closeSessionSupported = result.AgentCapabilities.SessionCapabilities.Close != nil
	c.deleteSessionSupported = result.AgentCapabilities.SessionCapabilities.Delete != nil
	c.forkSessionSupported = result.AgentCapabilities.SessionCapabilities.Fork != nil
	return nil
}

func (c *stdioACPClient) LoadSessionSupported() bool {
	return c != nil && c.loadSessionSupported
}

func (c *stdioACPClient) SupportsImages() bool { return c.imagePromptsSupported }

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
	c.rememberModels(result.SessionID, response.Result)
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
		result.SessionID = sessionID
	}
	c.rememberModels(result.SessionID, response.Result)
	return result.SessionID, nil
}

func (c *stdioACPClient) Prompt(ctx context.Context, sessionID, prompt string) (string, error) {
	return c.promptWithImages(ctx, sessionID, prompt, nil)
}

func (c *stdioACPClient) promptWithImages(ctx context.Context, sessionID, prompt string, images []media.Image) (string, error) {
	if len(images) > 0 && !c.SupportsImages() {
		return "", fmt.Errorf("ACP Agent 未声明图片输入能力")
	}
	blocks := make([]map[string]any, 0, len(images)+1)
	if prompt != "" || len(images) == 0 {
		blocks = append(blocks, map[string]any{"type": "text", "text": prompt})
	}
	for _, image := range images {
		blocks = append(blocks, map[string]any{"type": "image", "mimeType": image.ContentType(), "data": image.Base64()})
	}
	c.mu.Lock()
	c.collectors[sessionID] = &strings.Builder{}
	sink := ActivitySinkFromContext(ctx)
	var replay *ContextUsage
	if sink != nil {
		if c.activity == nil {
			c.activity = make(map[string]ActivitySink)
		}
		c.activity[sessionID] = sink
		if c.usageSessionID == sessionID {
			replay = c.contextReading
		}
	}
	c.mu.Unlock()
	if replay != nil {
		sink(Activity{NativeSession: sessionID, ContextUsage: replay})
	}
	defer func() {
		c.mu.Lock()
		delete(c.collectors, sessionID)
		delete(c.plans, sessionID)
		delete(c.activity, sessionID)
		c.mu.Unlock()
	}()
	_, err := c.request(ctx, "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt":    blocks,
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
		if reply := strings.TrimSpace(collector.String()); reply != "" {
			return reply, nil
		}
		if p := c.plans[sessionID]; p != nil {
			return p.Plan.ReplyText(p.Text), nil
		}
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
			return response, errors.New(response.Error.Message)
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
	scanner.Buffer(make([]byte, acpScannerInitialBufferSize), maxACPProtocolMessageBytes)
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
			Size, Used                      *int64
			AvailableCommands               []commandInfo   `json:"availableCommands"`
			Entries                         []acpPlanEntry  `json:"entries"`
			SessionUpdate                   string          `json:"sessionUpdate"`
			MessageID                       string          `json:"messageId"`
			Content                         json.RawMessage `json:"content"`
			ToolCallID, Title, Status, Kind string
			RawInput, RawOutput             json.RawMessage
		} `json:"update"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return
	}
	if payload.Update.SessionUpdate == "available_commands_update" {
		c.rememberCommands(payload.SessionID, payload.Update.AvailableCommands)
		return
	}
	if payload.Update.SessionUpdate == "config_option_update" {
		var params struct{ Update json.RawMessage }
		if json.Unmarshal(raw, &params) == nil {
			c.rememberModels(payload.SessionID, params.Update)
		}
		return
	}
	c.mu.Lock()
	sink := c.activity[payload.SessionID]
	collector := c.collectors[payload.SessionID]
	var event Activity
	switch payload.Update.SessionUpdate {
	case "usage_update":
		size, used := payload.Update.Size, payload.Update.Used
		if size != nil && *size > 0 && used != nil && *used >= 0 {
			u := &ContextUsage{Window: *size, Used: *used, Estimated: true, Captured: time.Now().UnixMicro()}
			if c.contextReading != nil {
				u.Captured = max(u.Captured, c.contextReading.Captured+1)
			}
			if c.modelSessionID == payload.SessionID {
				u.Model = c.modelOptions.Selected
			}
			c.usageSessionID, c.contextReading = payload.SessionID, u
			event = Activity{NativeSession: payload.SessionID, ContextUsage: u}
		}
	case "plan":
		if collector != nil && payload.Update.Entries != nil {
			event.Plan = acpSteps(payload.Update.Entries)
			if c.plans == nil {
				c.plans = make(map[string]*PlanUpdate)
			}
			c.plans[payload.SessionID] = event.Plan
		}
	case "agent_message_chunk":
		if collector != nil {
			event.TextID = payload.Update.MessageID
			event.Text = extractACPText(payload.Update.Content)
			collector.WriteString(event.Text)
		}
	case "tool_call", "tool_call_update":
		if sink != nil {
			update := payload.Update
			event = Activity{CallID: update.ToolCallID, Name: firstNonEmpty(update.Title, update.Kind), Status: "running", Input: activityInput(update.RawInput), Output: firstNonEmpty(activityOutput(update.RawOutput), extractACPText(update.Content))}
			event.Edits = acpFileEdits(update.Content)
			switch update.Status {
			case "completed":
				event.Status = "succeeded"
			case "failed":
				event.Status = "failed"
			case "cancelled":
				event.Status = "killed"
			}
		}
	}
	c.mu.Unlock()
	if sink != nil && (event.Text != "" || event.CallID != "" || event.Plan != nil || event.ContextUsage != nil) {
		sink(event)
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
