package qq

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

func TestGatewayClientGetGatewayURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Fatalf("method = %s", request.Method)
		}
		if request.URL.Path != "/gateway" {
			t.Fatalf("path = %s", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "QQBot token-1" {
			t.Fatalf("Authorization = %q", request.Header.Get("Authorization"))
		}
		_, _ = writer.Write([]byte(`{"url":"ws://gateway.example"}`))
	}))
	defer server.Close()

	client := NewGatewayClient(fakeTokenSource{token: "token-1", configured: true}, server.URL, server.Client())
	url, err := client.GetGatewayURL(context.Background())
	if err != nil {
		t.Fatalf("GetGatewayURL() error = %v", err)
	}
	if url != "ws://gateway.example" {
		t.Fatalf("url = %q", url)
	}
}

func TestGatewayClientIdentifyAndDispatch(t *testing.T) {
	updates := make(chan Update, 1)
	identify := make(chan GatewayPayload, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		conn, err := websocket.Accept(writer, request, nil)
		if err != nil {
			t.Fatalf("Accept() error = %v", err)
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		writeWSPayload(t, conn, GatewayPayload{Op: opHello, D: rawJSON(`{"heartbeat_interval":50}`)})
		payload := readWSPayload(t, conn)
		identify <- payload
		writeWSPayload(t, conn, GatewayPayload{Op: opDispatch, T: "READY", S: int64Ptr(1), D: rawJSON(`{"session_id":"session-1"}`)})
		writeWSPayload(t, conn, GatewayPayload{ID: "event-1", Op: opDispatch, T: "C2C_MESSAGE_CREATE", S: int64Ptr(2), D: rawJSON(`{"author":{"user_openid":"user-1"},"content":"hello","id":"message-1"}`)})
		<-request.Context().Done()
	}))
	defer wsServer.Close()

	apiServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`{"url":` + jsonQuote(httpToWS(wsServer.URL)) + `}`))
	}))
	defer apiServer.Close()

	client := NewGatewayClient(fakeTokenSource{token: "token-1", configured: true}, apiServer.URL, apiServer.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	state, err := client.Connect(ctx, GatewaySessionState{}, func(_ context.Context, update Update) error {
		updates <- update
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Connect() error = %v, want context.Canceled", err)
	}

	payload := <-identify
	if payload.Op != opIdentify {
		t.Fatalf("identify op = %d", payload.Op)
	}
	var data identifyData
	if err := json.Unmarshal(payload.D, &data); err != nil {
		t.Fatalf("decode identify: %v", err)
	}
	if data.Token != "QQBot token-1" || data.Intents != IntentGroupAndC2C || data.Shard != [2]int{0, 1} {
		t.Fatalf("identify = %+v", data)
	}
	update := <-updates
	if update.PeerKey != "qq:c2c:user-1" || update.Text != "hello" || update.DedupeKey != "event-1" {
		t.Fatalf("update = %+v", update)
	}
	if state.SessionID != "session-1" || state.Seq != 2 {
		t.Fatalf("state = %+v", state)
	}
}

func TestGatewayClientReconnectOpcode(t *testing.T) {
	client, closeServer := newGatewayOpcodeTestClient(t, GatewayPayload{Op: opReconnect})
	defer closeServer()
	_, err := client.Connect(context.Background(), GatewaySessionState{}, nil)
	if !errors.Is(err, ErrGatewayReconnect) {
		t.Fatalf("Connect() error = %v, want ErrGatewayReconnect", err)
	}
}

func TestGatewayClientInvalidSessionOpcode(t *testing.T) {
	client, closeServer := newGatewayOpcodeTestClient(t, GatewayPayload{Op: opInvalidState})
	defer closeServer()
	state, err := client.Connect(context.Background(), GatewaySessionState{SessionID: "session-1", Seq: 42}, nil)
	if !errors.Is(err, ErrGatewayInvalidSession) {
		t.Fatalf("Connect() error = %v, want ErrGatewayInvalidSession", err)
	}
	if state.SessionID != "" || state.Seq != 0 {
		t.Fatalf("state = %+v, want cleared", state)
	}
}

func newGatewayOpcodeTestClient(t *testing.T, payload GatewayPayload) (*GatewayClient, func()) {
	t.Helper()
	wsServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		conn, err := websocket.Accept(writer, request, nil)
		if err != nil {
			t.Fatalf("Accept() error = %v", err)
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		writeWSPayload(t, conn, GatewayPayload{Op: opHello, D: rawJSON(`{"heartbeat_interval":1000}`)})
		_ = readWSPayload(t, conn)
		writeWSPayload(t, conn, payload)
	}))
	apiServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`{"url":` + jsonQuote(httpToWS(wsServer.URL)) + `}`))
	}))
	client := NewGatewayClient(fakeTokenSource{token: "token-1", configured: true}, apiServer.URL, apiServer.Client())
	return client, func() {
		apiServer.Close()
		wsServer.Close()
	}
}

func writeWSPayload(t *testing.T, conn *websocket.Conn, payload GatewayPayload) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, encodeGatewayPayload(payload)); err != nil {
		t.Fatalf("ws write: %v", err)
	}
}

func readWSPayload(t *testing.T, conn *websocket.Conn) GatewayPayload {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	var payload GatewayPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode ws payload: %v", err)
	}
	return payload
}

func rawJSON(value string) json.RawMessage {
	return json.RawMessage(value)
}

func int64Ptr(value int64) *int64 {
	return &value
}

func httpToWS(value string) string {
	return "ws" + strings.TrimPrefix(value, "http")
}

func jsonQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
