package server

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bqagent/internal/agent"
)

const (
	defaultWebUIStageTimeout       = 90 * time.Second
	defaultWebUIStageMaxIterations = 20
)

var webUIStageTimeoutNanos atomic.Int64
var webUIStageMaxIterations atomic.Int64

func WebUIStageTimeout() time.Duration {
	if value := webUIStageTimeoutNanos.Load(); value > 0 {
		return time.Duration(value)
	}
	return defaultWebUIStageTimeout
}

func SetWebUIStageTimeout(timeout time.Duration) {
	if timeout <= 0 {
		webUIStageTimeoutNanos.Store(0)
		return
	}
	webUIStageTimeoutNanos.Store(int64(timeout))
}

func WebUIStageMaxIterations() int {
	if value := webUIStageMaxIterations.Load(); value > 0 {
		return int(value)
	}
	return defaultWebUIStageMaxIterations
}

func SetWebUIStageMaxIterations(maxIterations int) {
	if maxIterations <= 0 {
		webUIStageMaxIterations.Store(0)
		return
	}
	webUIStageMaxIterations.Store(int64(maxIterations))
}

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
	ctx, cancel := context.WithTimeout(request.Context(), ChannelTurnTimeout())
	defer cancel()

	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Connection", "keep-alive")
	// Disable proxy buffering (e.g. nginx) so events arrive incrementally.
	writer.Header().Set("X-Accel-Buffering", "no")
	writer.WriteHeader(http.StatusOK)
	flusher.Flush()

	stream := &sseStream{writer: writer, flusher: flusher}
	response, err := channel.service.HandleTurnWithOptions(ctx, TurnRequest{
		SessionID: turnRequest.SessionID,
		Message:   turnRequest.Message,
		TurnID:    turnRequest.TurnID,
	}, TurnOptions{
		Stream:         true,
		TokenSink:      sseEventWriter{stream: stream, event: "message"},
		ProgressWriter: sseEventWriter{stream: stream, event: "progress"},
		MaxIterations:  ChannelMaxIterations(),
		Stage: agent.StageConfig{
			MaxIterations:     WebUIStageMaxIterations(),
			Timeout:           WebUIStageTimeout(),
			LoopProtection:    true,
			ImmediateProgress: true,
			EmitProgress:      true,
		},
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			writeSSEEvent(writer, flusher, "stopped", map[string]string{"message": "已停止生成。"})
			return
		}
		writeSSEEvent(writer, flusher, "error", map[string]string{"error": err.Error()})
		return
	}

	writeSSEEvent(writer, flusher, "done", map[string]string{
		"session_id": response.SessionID,
		"run_id":     response.RunID,
		"reply":      sanitizeChannelReply(response.Reply),
	})
}

type sseStream struct {
	mu      sync.Mutex
	writer  http.ResponseWriter
	flusher http.Flusher
}

type sseEventWriter struct {
	stream *sseStream
	event  string
}

func (sink sseEventWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	if sink.stream == nil {
		return 0, io.ErrClosedPipe
	}
	message := string(data)
	payloadValue := map[string]string{"delta": message}
	if sink.event == "progress" {
		message = strings.TrimSpace(message)
		if message == "" {
			return len(data), nil
		}
		payloadValue = map[string]string{"message": message}
	}
	payload, err := json.Marshal(payloadValue)
	if err != nil {
		return 0, err
	}
	sink.stream.mu.Lock()
	defer sink.stream.mu.Unlock()
	prefix := ""
	if sink.event != "" && sink.event != "message" {
		prefix = "event: " + sink.event + "\n"
	}
	if _, err := fmt.Fprintf(sink.stream.writer, "%sdata: %s\n\n", prefix, payload); err != nil {
		return 0, err
	}
	sink.stream.flusher.Flush()
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
