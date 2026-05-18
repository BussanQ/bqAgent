package qq

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"nhooyr.io/websocket"
)

const (
	opDispatch     = 0
	opHeartbeat    = 1
	opIdentify     = 2
	opResume       = 6
	opReconnect    = 7
	opInvalidState = 9
	opHello        = 10
	opHeartbeatACK = 11

	IntentGroupAndC2C = 1 << 25
)

var (
	ErrGatewayReconnect      = errors.New("qq gateway reconnect requested")
	ErrGatewayInvalidSession = errors.New("qq gateway invalid session")
)

type GatewayClient struct {
	httpClient *http.Client
	baseURL    string
	tokens     TokenSource
}

type GatewayURLResponse struct {
	URL string `json:"url"`
}

type helloData struct {
	HeartbeatInterval int `json:"heartbeat_interval"`
}

type identifyData struct {
	Token   string `json:"token"`
	Intents int    `json:"intents"`
	Shard   [2]int `json:"shard"`
}

type resumeData struct {
	Token     string `json:"token"`
	SessionID string `json:"session_id"`
	Seq       int64  `json:"seq"`
}

type readyData struct {
	SessionID string `json:"session_id"`
}

func NewGatewayClient(tokenSource TokenSource, apiBaseURL string, httpClient *http.Client) *GatewayClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	if strings.TrimSpace(apiBaseURL) == "" {
		apiBaseURL = DefaultAPIBaseURL
	}
	return &GatewayClient{
		httpClient: httpClient,
		baseURL:    strings.TrimRight(strings.TrimSpace(apiBaseURL), "/"),
		tokens:     tokenSource,
	}
}

func (client *GatewayClient) Configured() bool {
	return client != nil && client.tokens != nil && client.tokens.Configured()
}

func (client *GatewayClient) GetGatewayURL(ctx context.Context) (string, error) {
	if !client.Configured() {
		return "", fmt.Errorf("qq gateway client is not configured")
	}
	token, err := client.tokens.Token(ctx)
	if err != nil {
		return "", err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", fmt.Errorf("qq access token is required")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.baseURL+"/gateway", nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "QQBot "+token)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("qq gateway request failed: %s", strings.TrimSpace(string(payload)))
	}
	var decoded GatewayURLResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return "", err
	}
	if strings.TrimSpace(decoded.URL) == "" {
		return "", fmt.Errorf("qq gateway response missing url")
	}
	return strings.TrimSpace(decoded.URL), nil
}

func (client *GatewayClient) Connect(ctx context.Context, state GatewaySessionState, handler func(context.Context, Update) error) (GatewaySessionState, error) {
	if !client.Configured() {
		return state, fmt.Errorf("qq gateway client is not configured")
	}
	accessToken, err := client.tokens.Token(ctx)
	if err != nil {
		return state, err
	}
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return state, fmt.Errorf("qq access token is required")
	}
	gatewayURL, err := client.GetGatewayURL(ctx)
	if err != nil {
		return state, err
	}
	conn, _, err := websocket.Dial(ctx, gatewayURL, &websocket.DialOptions{HTTPHeader: http.Header{"User-Agent": []string{"bqagent"}}})
	if err != nil {
		return state, err
	}
	defer conn.Close(websocket.StatusNormalClosure, "closing")

	hello, err := readGatewayPayload(ctx, conn)
	if err != nil {
		return state, err
	}
	if hello.Op != opHello {
		return state, fmt.Errorf("qq gateway expected hello op, got %d", hello.Op)
	}
	var helloData helloData
	if err := json.Unmarshal(hello.D, &helloData); err != nil {
		return state, err
	}
	interval := time.Duration(helloData.HeartbeatInterval) * time.Millisecond
	if interval <= 0 {
		interval = 30 * time.Second
	}

	var writeMu sync.Mutex
	var stateMu sync.Mutex
	if strings.TrimSpace(state.SessionID) != "" && state.Seq > 0 {
		if err := writeGatewayPayload(ctx, conn, &writeMu, gatewayPayload(opResume, resumeData{Token: "QQBot " + accessToken, SessionID: state.SessionID, Seq: state.Seq})); err != nil {
			return state, err
		}
	} else {
		if err := writeGatewayPayload(ctx, conn, &writeMu, gatewayPayload(opIdentify, identifyData{Token: "QQBot " + accessToken, Intents: IntentGroupAndC2C, Shard: [2]int{0, 1}})); err != nil {
			return state, err
		}
	}

	heartbeatCtx, heartbeatCancel := context.WithCancel(ctx)
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				stateMu.Lock()
				seq := state.Seq
				stateMu.Unlock()
				var data any
				if seq > 0 {
					data = seq
				}
				_ = writeGatewayPayload(ctx, conn, &writeMu, gatewayPayload(opHeartbeat, data))
			}
		}
	}()
	defer func() {
		heartbeatCancel()
		<-heartbeatDone
	}()

	for {
		payload, err := readGatewayPayload(ctx, conn)
		if err != nil {
			if ctx.Err() != nil {
				return state, ctx.Err()
			}
			return state, err
		}
		if payload.S != nil {
			stateMu.Lock()
			state.Seq = *payload.S
			stateMu.Unlock()
		}
		switch payload.Op {
		case opDispatch:
			if strings.TrimSpace(payload.T) == "READY" {
				var ready readyData
				if err := json.Unmarshal(payload.D, &ready); err != nil {
					return state, err
				}
				stateMu.Lock()
				state.SessionID = strings.TrimSpace(ready.SessionID)
				stateMu.Unlock()
				continue
			}
			update, err := ParseGatewayDispatchPayload(payload)
			if err != nil {
				if errors.Is(err, ErrIgnoreUpdate) {
					continue
				}
				return state, err
			}
			if handler != nil {
				if err := handler(ctx, update); err != nil {
					return state, err
				}
			}
		case opReconnect:
			return state, ErrGatewayReconnect
		case opInvalidState:
			stateMu.Lock()
			state.SessionID = ""
			state.Seq = 0
			stateMu.Unlock()
			return state, ErrGatewayInvalidSession
		case opHeartbeatACK:
			continue
		}
	}
}

func readGatewayPayload(ctx context.Context, conn *websocket.Conn) (GatewayPayload, error) {
	_, data, err := conn.Read(ctx)
	if err != nil {
		return GatewayPayload{}, err
	}
	var payload GatewayPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return GatewayPayload{}, err
	}
	return payload, nil
}

func writeGatewayPayload(ctx context.Context, conn *websocket.Conn, mu *sync.Mutex, payload GatewayPayload) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	return conn.Write(ctx, websocket.MessageText, data)
}

func gatewayPayload(op int, data any) GatewayPayload {
	var raw json.RawMessage
	if data != nil {
		encoded, _ := json.Marshal(data)
		raw = encoded
	} else {
		raw = json.RawMessage("null")
	}
	return GatewayPayload{Op: op, D: raw}
}

func encodeGatewayPayload(payload GatewayPayload) []byte {
	data, _ := json.Marshal(payload)
	return bytes.TrimSpace(data)
}
