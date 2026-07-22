package server

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
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
	maxWebUIRequestBodyBytes       = 12 << 20
	maxWebUIImages                 = 4
	maxWebUIImageBytes             = 3 << 20
	maxWebUITotalImageBytes        = 8 << 20
	maxWebUIImagePixels            = 20_000_000
)

type webUIChatRequest struct {
	SessionID string              `json:"session_id"`
	TurnID    string              `json:"turn_id"`
	Message   string              `json:"message"`
	Images    []webUIImagePayload `json:"images,omitempty"`
}

type webUIImagePayload struct {
	MIMEType   string `json:"mime_type"`
	DataBase64 string `json:"data_base64"`
}

type webUIRequestError struct {
	Status int
	Err    error
}

func (err *webUIRequestError) Error() string {
	if err == nil || err.Err == nil {
		return "invalid webui request"
	}
	return err.Err.Error()
}

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

func decodeWebUIChatRequest(writer http.ResponseWriter, request *http.Request) (TurnRequest, error) {
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(request.Header.Get("Content-Type"), ";")[0]))
	if contentType != "application/json" {
		return TurnRequest{}, &webUIRequestError{Status: http.StatusUnsupportedMediaType, Err: fmt.Errorf("content-type must be application/json")}
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxWebUIRequestBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var payload webUIChatRequest
	if err := decoder.Decode(&payload); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return TurnRequest{}, &webUIRequestError{Status: http.StatusRequestEntityTooLarge, Err: fmt.Errorf("webui request body exceeds %d bytes", maxWebUIRequestBodyBytes)}
		}
		return TurnRequest{}, &webUIRequestError{Status: http.StatusBadRequest, Err: fmt.Errorf("invalid JSON request: %w", err)}
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return TurnRequest{}, &webUIRequestError{Status: http.StatusBadRequest, Err: fmt.Errorf("request must contain exactly one JSON object")}
		}
		return TurnRequest{}, &webUIRequestError{Status: http.StatusBadRequest, Err: fmt.Errorf("invalid trailing JSON data: %w", err)}
	}
	if len(payload.Images) > maxWebUIImages {
		return TurnRequest{}, &webUIRequestError{Status: http.StatusBadRequest, Err: fmt.Errorf("at most %d images are allowed", maxWebUIImages)}
	}
	images := make([]agent.ImageAttachment, 0, len(payload.Images))
	totalBytes := 0
	for index, imagePayload := range payload.Images {
		imageAttachment, err := decodeWebUIImage(imagePayload)
		if err != nil {
			return TurnRequest{}, &webUIRequestError{Status: http.StatusBadRequest, Err: fmt.Errorf("image %d: %w", index+1, err)}
		}
		totalBytes += len(imageAttachment.Data)
		if totalBytes > maxWebUITotalImageBytes {
			return TurnRequest{}, &webUIRequestError{Status: http.StatusRequestEntityTooLarge, Err: fmt.Errorf("decoded image data exceeds %d bytes", maxWebUITotalImageBytes)}
		}
		images = append(images, imageAttachment)
	}
	if strings.TrimSpace(payload.Message) == "" && len(images) == 0 {
		return TurnRequest{}, &webUIRequestError{Status: http.StatusBadRequest, Err: fmt.Errorf("message or images are required")}
	}
	return TurnRequest{SessionID: strings.TrimSpace(payload.SessionID), TurnID: strings.TrimSpace(payload.TurnID), Message: payload.Message, Images: images}, nil
}

func decodeWebUIImage(payload webUIImagePayload) (agent.ImageAttachment, error) {
	mimeType := strings.ToLower(strings.TrimSpace(payload.MIMEType))
	allowed := map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true}
	if !allowed[mimeType] {
		return agent.ImageAttachment{}, fmt.Errorf("unsupported MIME type %q", mimeType)
	}
	encoded := strings.TrimSpace(payload.DataBase64)
	if encoded == "" {
		return agent.ImageAttachment{}, fmt.Errorf("image data is empty")
	}
	if strings.HasPrefix(strings.ToLower(encoded), "data:") {
		return agent.ImageAttachment{}, fmt.Errorf("data URI wrappers are not accepted")
	}
	maxEncodedLength := base64.StdEncoding.EncodedLen(maxWebUIImageBytes)
	if len(encoded) > maxEncodedLength {
		return agent.ImageAttachment{}, fmt.Errorf("image exceeds %d decoded bytes", maxWebUIImageBytes)
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return agent.ImageAttachment{}, fmt.Errorf("invalid base64 data: %w", err)
	}
	if len(data) == 0 || len(data) > maxWebUIImageBytes {
		return agent.ImageAttachment{}, fmt.Errorf("image size must be between 1 and %d bytes", maxWebUIImageBytes)
	}
	detectedMIME := http.DetectContentType(data)
	if detectedMIME != mimeType {
		return agent.ImageAttachment{}, fmt.Errorf("declared MIME %q does not match detected MIME %q", mimeType, detectedMIME)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return agent.ImageAttachment{}, fmt.Errorf("invalid image data: %w", err)
	}
	if config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > maxWebUIImagePixels {
		return agent.ImageAttachment{}, fmt.Errorf("image dimensions exceed %d pixels", maxWebUIImagePixels)
	}
	return agent.ImageAttachment{MIMEType: mimeType, Data: data}, nil
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

	turnRequest, err := decodeWebUIChatRequest(writer, request)
	if err != nil {
		status := http.StatusBadRequest
		var requestErr *webUIRequestError
		if errors.As(err, &requestErr) && requestErr.Status > 0 {
			status = requestErr.Status
		}
		writeError(writer, status, chatResponse{Error: err.Error()})
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
		Images:    turnRequest.Images,
	}, TurnOptions{
		Stream:         true,
		TokenSink:      sseEventWriter{stream: stream, event: "message"},
		ProgressWriter: sseEventWriter{stream: stream, event: "progress"},
		ToolEventSink:  sseToolEventSink{stream: stream},
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

type sseToolEventSink struct {
	stream *sseStream
}

func (sink sseToolEventSink) EmitToolEvent(event agent.ToolEvent) {
	if sink.stream == nil || event.Kind == "" {
		return
	}
	_ = sink.stream.writeEvent(event.Kind, event)
}

func (stream *sseStream) writeEvent(event string, value any) error {
	if stream == nil {
		return io.ErrClosedPipe
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	prefix := ""
	if event != "" && event != "message" {
		prefix = "event: " + event + "\n"
	}
	if _, err := fmt.Fprintf(stream.writer, "%sdata: %s\n\n", prefix, payload); err != nil {
		return err
	}
	stream.flusher.Flush()
	return nil
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
	if err := sink.stream.writeEvent(sink.event, payloadValue); err != nil {
		return 0, err
	}
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
