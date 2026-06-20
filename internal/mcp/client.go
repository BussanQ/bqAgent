package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	defaultClientTimeout = 60 * time.Second
	protocolVersion      = "2025-06-18"
	clientName           = "bqagent"
	clientVersion        = "1.0"
)

// Client speaks the MCP Streamable HTTP transport against a single server
// endpoint. It is safe for concurrent use: the agent runs tool calls in
// parallel, so each CallTool may run on its own goroutine.
type Client struct {
	httpClient *http.Client
	url        string
	headers    map[string]string

	mu        sync.Mutex
	sessionID string
	nextID    int
}

// ToolSpec is a tool advertised by an MCP server via tools/list.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// NewClient builds a Client for the given endpoint. A nil httpClient gets a
// default with a sane timeout (mirrors the web_fetch/web_search convention).
func NewClient(httpClient *http.Client, url string, headers map[string]string) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultClientTimeout}
	}
	return &Client{httpClient: httpClient, url: url, headers: headers}
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int   `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

// Initialize performs the MCP handshake: initialize, capture the session id,
// then send the initialized notification.
func (c *Client) Initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": clientName, "version": clientVersion},
	}
	if _, err := c.call(ctx, "initialize", params); err != nil {
		return err
	}
	// notifications/initialized has no id and expects no result.
	if err := c.notify(ctx, "notifications/initialized", map[string]any{}); err != nil {
		return err
	}
	return nil
}

// ListTools returns the tools advertised by the server.
func (c *Client) ListTools(ctx context.Context) ([]ToolSpec, error) {
	result, err := c.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var decoded struct {
		Tools []ToolSpec `json:"tools"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		return nil, fmt.Errorf("decode tools/list: %w", err)
	}
	return decoded.Tools, nil
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// CallTool invokes a tool and returns its textual content. An MCP-reported
// tool error (isError=true) is returned as a Go error.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	if args == nil {
		args = map[string]any{}
	}
	result, err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", err
	}
	var decoded struct {
		Content []toolContent `json:"content"`
		IsError bool          `json:"isError"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		return "", fmt.Errorf("decode tools/call: %w", err)
	}
	text := flattenContent(decoded.Content)
	if decoded.IsError {
		if text == "" {
			text = "tool reported an error"
		}
		return "", fmt.Errorf("%s", text)
	}
	return text, nil
}

func flattenContent(parts []toolContent) string {
	var builder strings.Builder
	for _, part := range parts {
		switch part.Type {
		case "text", "":
			builder.WriteString(part.Text)
		default:
			fmt.Fprintf(&builder, "[%s content omitted]", part.Type)
		}
		builder.WriteString("\n")
	}
	return strings.TrimRight(builder.String(), "\n")
}

// call sends a request expecting a response and returns its result payload.
func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()

	payload := rpcRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: params}
	response, err := c.do(ctx, payload, id)
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, fmt.Errorf("%s: empty response", method)
	}
	if response.Error != nil {
		return nil, fmt.Errorf("%s: server error %d: %s", method, response.Error.Code, response.Error.Message)
	}
	return response.Result, nil
}

// notify sends a notification (no id) and ignores any response body.
func (c *Client) notify(ctx context.Context, method string, params any) error {
	payload := rpcRequest{JSONRPC: "2.0", Method: method, Params: params}
	_, err := c.do(ctx, payload, 0)
	return err
}

func (c *Client) do(ctx context.Context, payload rpcRequest, wantID int) (*rpcResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for key, value := range c.headers {
		request.Header.Set(key, value)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", protocolVersion)
	c.mu.Lock()
	sessionID := c.sessionID
	c.mu.Unlock()
	if sessionID != "" {
		request.Header.Set("Mcp-Session-Id", sessionID)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("mcp request failed: %w", err)
	}
	defer response.Body.Close()

	if id := response.Header.Get("Mcp-Session-Id"); id != "" {
		c.mu.Lock()
		c.sessionID = id
		c.mu.Unlock()
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return nil, fmt.Errorf("mcp request failed: %s: %s", response.Status, strings.TrimSpace(string(snippet)))
	}

	// Notifications / accepted-with-no-body.
	if response.StatusCode == http.StatusNoContent {
		return nil, nil
	}

	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(response.Header.Get("Content-Type"), ";", 2)[0]))
	switch {
	case strings.HasPrefix(mediaType, "text/event-stream"):
		return readSSEResponse(response.Body, wantID)
	case strings.HasPrefix(mediaType, "application/json"):
		return readJSONResponse(response.Body)
	default:
		// Some servers reply 202 with an empty body for notifications.
		data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if len(bytes.TrimSpace(data)) == 0 {
			return nil, nil
		}
		var decoded rpcResponse
		if err := json.Unmarshal(data, &decoded); err != nil {
			return nil, fmt.Errorf("decode response (%s): %w", mediaType, err)
		}
		return &decoded, nil
	}
}

func readJSONResponse(body io.Reader) (*rpcResponse, error) {
	data, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	var decoded rpcResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, fmt.Errorf("decode json response: %w", err)
	}
	return &decoded, nil
}

// readSSEResponse scans an event stream and returns the JSON-RPC message whose
// id matches wantID, ignoring unrelated notifications/pings.
func readSSEResponse(body io.Reader, wantID int) (*rpcResponse, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var data strings.Builder
	flush := func() (*rpcResponse, bool, error) {
		payload := strings.TrimSpace(data.String())
		data.Reset()
		if payload == "" {
			return nil, false, nil
		}
		var decoded rpcResponse
		if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
			return nil, false, nil // skip non-JSON-RPC events
		}
		if decoded.ID != nil && *decoded.ID == wantID {
			return &decoded, true, nil
		}
		return nil, false, nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" { // event boundary
			if resp, ok, err := flush(); err != nil || ok {
				return resp, err
			}
			continue
		}
		if strings.HasPrefix(line, ":") { // comment / keep-alive
			continue
		}
		if value, found := strings.CutPrefix(line, "data:"); found {
			data.WriteString(strings.TrimPrefix(value, " "))
			data.WriteString("\n")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read event stream: %w", err)
	}
	if resp, ok, err := flush(); err != nil || ok {
		return resp, err
	}
	return nil, fmt.Errorf("event stream ended without a response for id %d", wantID)
}
