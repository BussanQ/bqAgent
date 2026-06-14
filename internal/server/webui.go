package server

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
)

//go:embed webui/index.html
var webUIIndex []byte

// WebUIChannel serves a self-contained browser chat page at "/" and streams
// assistant replies token-by-token over Server-Sent Events. It reuses the
// shared Service turn machinery (sessions, per-session locking, memory), so the
// only state it owns is whether it is enabled.
type WebUIChannel struct {
	service *Service
	enabled bool
}

func NewWebUIChannel(service *Service, enabled bool) *WebUIChannel {
	return &WebUIChannel{service: service, enabled: enabled}
}

func (channel *WebUIChannel) Name() string {
	return "webui"
}

func (channel *WebUIChannel) Enabled() bool {
	return channel != nil && channel.service != nil && channel.enabled
}

func (channel *WebUIChannel) RegisterRoutes(mux *http.ServeMux) {
	if !channel.Enabled() || mux == nil {
		return
	}
	mux.HandleFunc("/", channel.handleIndex)
	mux.HandleFunc("/api/v1/webui/chat", channel.handleStreamChat)
}

// Start has no work to do: the web UI is purely request-driven.
func (channel *WebUIChannel) Start(ctx context.Context) {}

func (channel *WebUIChannel) handleIndex(writer http.ResponseWriter, request *http.Request) {
	// "/" is a subtree match, so it also catches any path not claimed by a more
	// specific route; only the exact root serves the page.
	if request.URL.Path != "/" {
		http.NotFound(writer, request)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	if request.Method == http.MethodHead {
		return
	}
	_, _ = writer.Write(webUIIndex)
}

func (channel *WebUIChannel) handleStreamChat(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
		return
	}
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeError(writer, http.StatusInternalServerError, chatResponse{Error: "streaming is not supported"})
		return
	}

	values, err := readValues(writer, request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: err.Error()})
		return
	}
	turnRequest, err := parseTurnRequest(values)
	if err != nil {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: err.Error()})
		return
	}

	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Connection", "keep-alive")
	// Disable proxy buffering (e.g. nginx) so events arrive incrementally.
	writer.Header().Set("X-Accel-Buffering", "no")
	writer.WriteHeader(http.StatusOK)
	flusher.Flush()

	// A web turn streams tokens and may run several tool iterations, so bound it
	// by the channel turn timeout rather than the short JSON request timeout.
	ctx, cancel := context.WithTimeout(request.Context(), ChannelTurnTimeout())
	defer cancel()

	sink := &sseTokenWriter{writer: writer, flusher: flusher}
	response, err := channel.service.HandleTurnWithOptions(ctx, TurnRequest{
		SessionID: turnRequest.SessionID,
		Message:   turnRequest.Message,
	}, TurnOptions{Stream: true, TokenSink: sink})
	if err != nil {
		writeSSEEvent(writer, flusher, "error", map[string]string{"error": err.Error()})
		return
	}

	writeSSEEvent(writer, flusher, "done", map[string]string{
		"session_id": response.SessionID,
		"reply":      sanitizeChannelReply(response.Reply),
	})
}

// sseTokenWriter turns each streamed token chunk into a `data:` SSE event. The
// agent serializes writes to this sink, so no extra locking is needed here.
type sseTokenWriter struct {
	writer  http.ResponseWriter
	flusher http.Flusher
}

func (sink *sseTokenWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	payload, err := json.Marshal(map[string]string{"delta": string(data)})
	if err != nil {
		return 0, err
	}
	if _, err := fmt.Fprintf(sink.writer, "data: %s\n\n", payload); err != nil {
		return 0, err
	}
	sink.flusher.Flush()
	return len(data), nil
}

func writeSSEEvent(writer http.ResponseWriter, flusher http.Flusher, event string, payload any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		encoded = []byte("{}")
	}
	_, _ = fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", event, encoded)
	flusher.Flush()
}
