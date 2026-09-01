package server

import (
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bqagent/internal/agent"
)

const (
	// WebUI turns run until completion by default. Positive WEBUI_STAGE_*
	// overrides can opt back into persisted stage checkpoints.
	defaultWebUIStageTimeout       time.Duration = 0
	defaultWebUIStageMaxIterations               = 0
	maxWebUIRequestBodyBytes                     = 21 << 20
	maxWebUIImages                               = 4
	maxWebUIImageBytes                           = 3 << 20
	maxWebUITotalImageBytes                      = 8 << 20
	maxWebUIImagePixels                          = 20_000_000
	maxWebUIFiles                                = 5
	maxWebUIFileBytes                            = 2 << 20
	maxWebUITotalFileBytes                       = 6 << 20
)

type webUIChatRequest struct {
	WorkspaceID      string              `json:"workspace_id,omitempty"`
	SessionID        string              `json:"session_id"`
	TurnID           string              `json:"turn_id"`
	Message          string              `json:"message"`
	ReasoningEffort  string              `json:"reasoning_effort,omitempty"`
	Mode             string              `json:"mode,omitempty"`
	ConversationType string              `json:"conversation_type,omitempty"`
	Images           []webUIImagePayload `json:"images,omitempty"`
	Files            []webUIFilePayload  `json:"files,omitempty"`
}

type webUIDoneEvent struct {
	WorkspaceID      string             `json:"workspace_id,omitempty"`
	SessionID        string             `json:"session_id"`
	RunID            string             `json:"run_id,omitempty"`
	Reply            string             `json:"reply"`
	ReplyKind        string             `json:"reply_kind,omitempty"`
	APIType          string             `json:"api_type"`
	Model            string             `json:"model"`
	Mode             ChatMode           `json:"mode"`
	ConversationType ConversationType   `json:"conversation_type"`
	Group            *GroupInfo         `json:"group,omitempty"`
	Generation       *GenerationMetrics `json:"generation,omitempty"`
}

type webUIImagePayload struct {
	MIMEType   string `json:"mime_type"`
	DataBase64 string `json:"data_base64"`
}

type webUIFilePayload struct {
	Name       string  `json:"name,omitempty"`
	DataBase64 *string `json:"data_base64,omitempty"`
	Path       string  `json:"path,omitempty"`
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

//go:embed webui/dist
var webUIDist embed.FS

var webUIFiles = mustWebUISub(webUIDist, "webui/dist")
var webUIIndex = mustReadWebUIFile("index.html")
var webUIFavicon = mustReadWebUIFile("favicon.ico")
var webUIFaviconSVG = mustReadWebUIFile("favicon.svg")

func mustWebUISub(root fs.FS, directory string) fs.FS {
	sub, err := fs.Sub(root, directory)
	if err != nil {
		panic(fmt.Sprintf("prepare embedded WebUI: %v", err))
	}
	return sub
}

func mustReadWebUIFile(name string) []byte {
	data, err := fs.ReadFile(webUIFiles, name)
	if err != nil {
		panic(fmt.Sprintf("read embedded WebUI file %q: %v", name, err))
	}
	return data
}

// WebUIChannel serves a self-contained browser chat page at "/" and streams
// assistant replies token-by-token over Server-Sent Events. It reuses the
// shared Service turn machinery and can resolve a workspace-specific Service
// when a registry is configured.
type WebUIChannel struct {
	service    *Service
	workspaces *WorkspaceRegistry
	enabled    bool
}

func NewWebUIChannel(service *Service, enabled bool) *WebUIChannel {
	return &WebUIChannel{service: service, enabled: enabled}
}

func NewWebUIChannelWithWorkspaces(service *Service, workspaces *WorkspaceRegistry, enabled bool) *WebUIChannel {
	return &WebUIChannel{service: service, workspaces: workspaces, enabled: enabled}
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
	mux.HandleFunc("/assets/", channel.handleAsset)
	mux.HandleFunc("/favicon.ico", channel.handleFavicon)
	mux.HandleFunc("/favicon.svg", channel.handleFaviconSVG)
	mux.HandleFunc("/api/v1/webui/chat", channel.handleStreamChat)
	mux.HandleFunc("/api/v1/webui/workspace", channel.handleWorkspaceList)
	mux.HandleFunc("/api/v1/webui/workspace/config", channel.handleWorkspaceConfigCreate)
	mux.HandleFunc("/api/v1/webui/workspace/preview", channel.handleWorkspacePreview)
	mux.HandleFunc("/api/v1/webui/workspaces", channel.handleWorkspaces)
	mux.HandleFunc("/api/v1/webui/workspaces/directories", channel.handleWorkspaceDirectories)
	mux.HandleFunc("/api/v1/webui/workspaces/open", channel.handleWorkspaceOpen)
}

func (channel *WebUIChannel) handleAsset(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
		return
	}
	name := strings.TrimPrefix(request.URL.Path, "/")
	if !strings.HasPrefix(name, "assets/") || !fs.ValidPath(name) || strings.Contains(name, "\\") {
		http.NotFound(writer, request)
		return
	}
	data, err := fs.ReadFile(webUIFiles, name)
	if err != nil {
		http.NotFound(writer, request)
		return
	}
	contentType := mime.TypeByExtension(path.Ext(name))
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	writer.WriteHeader(http.StatusOK)
	if request.Method == http.MethodHead {
		return
	}
	_, _ = writer.Write(data)
}

func (channel *WebUIChannel) resolveService(workspaceID string) (*Service, WorkspaceInfo, error) {
	if channel == nil || channel.service == nil {
		return nil, WorkspaceInfo{}, fmt.Errorf("service is unavailable")
	}
	if channel.workspaces == nil {
		if strings.TrimSpace(workspaceID) != "" {
			return nil, WorkspaceInfo{}, os.ErrNotExist
		}
		return channel.service, WorkspaceInfo{}, nil
	}
	return channel.workspaces.Resolve(workspaceID)
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
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	if request.Method == http.MethodHead {
		return
	}
	_, _ = writer.Write(webUIIndex)
}

func (channel *WebUIChannel) handleFavicon(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
		return
	}
	writer.Header().Set("Content-Type", "image/x-icon")
	writer.Header().Set("Cache-Control", "public, max-age=86400")
	writer.WriteHeader(http.StatusOK)
	if request.Method == http.MethodHead {
		return
	}
	_, _ = writer.Write(webUIFavicon)
}

func (channel *WebUIChannel) handleFaviconSVG(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
		return
	}
	writer.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	writer.Header().Set("Cache-Control", "public, max-age=86400")
	writer.WriteHeader(http.StatusOK)
	if request.Method == http.MethodHead {
		return
	}
	_, _ = writer.Write(webUIFaviconSVG)
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
	reasoningEffort, err := agent.ParseReasoningEffort(payload.ReasoningEffort)
	if err != nil {
		return TurnRequest{}, &webUIRequestError{Status: http.StatusBadRequest, Err: err}
	}
	var mode ChatMode
	if strings.TrimSpace(payload.Mode) != "" {
		mode, err = parseChatMode(payload.Mode)
		if err != nil {
			return TurnRequest{}, &webUIRequestError{Status: http.StatusBadRequest, Err: err}
		}
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
	if len(payload.Files) > maxWebUIFiles {
		return TurnRequest{}, &webUIRequestError{Status: http.StatusBadRequest, Err: fmt.Errorf("at most %d files are allowed", maxWebUIFiles)}
	}
	files := make([]FileAttachment, 0, len(payload.Files))
	totalFileBytes := 0
	for index, filePayload := range payload.Files {
		fileAttachment, err := decodeWebUIFile(filePayload)
		if err != nil {
			return TurnRequest{}, &webUIRequestError{Status: http.StatusBadRequest, Err: fmt.Errorf("file %d: %w", index+1, err)}
		}
		totalFileBytes += len(fileAttachment.Data)
		if totalFileBytes > maxWebUITotalFileBytes {
			return TurnRequest{}, &webUIRequestError{Status: http.StatusRequestEntityTooLarge, Err: fmt.Errorf("decoded file data exceeds %d bytes", maxWebUITotalFileBytes)}
		}
		files = append(files, fileAttachment)
	}
	if strings.TrimSpace(payload.Message) == "" && len(images) == 0 && len(files) == 0 {
		return TurnRequest{}, &webUIRequestError{Status: http.StatusBadRequest, Err: fmt.Errorf("message, images, or files are required")}
	}
	return TurnRequest{
		WorkspaceID:      strings.TrimSpace(payload.WorkspaceID),
		SessionID:        strings.TrimSpace(payload.SessionID),
		TurnID:           strings.TrimSpace(payload.TurnID),
		Message:          payload.Message,
		Images:           images,
		Files:            files,
		ReasoningEffort:  reasoningEffort,
		Mode:             mode,
		ConversationType: ConversationType(strings.TrimSpace(payload.ConversationType)),
	}, nil
}

func decodeWebUIFile(payload webUIFilePayload) (FileAttachment, error) {
	name := strings.TrimSpace(payload.Name)
	path := strings.TrimSpace(payload.Path)
	hasUpload := payload.DataBase64 != nil
	hasPath := path != ""
	if hasUpload == hasPath {
		return FileAttachment{}, fmt.Errorf("provide either name with data_base64 or path")
	}
	if hasPath {
		if name != "" {
			return FileAttachment{}, fmt.Errorf("name is not allowed with path")
		}
		return FileAttachment{Path: path}, nil
	}
	if name == "" {
		return FileAttachment{}, fmt.Errorf("uploaded file name is required")
	}
	encoded := strings.TrimSpace(*payload.DataBase64)
	if strings.HasPrefix(strings.ToLower(encoded), "data:") {
		return FileAttachment{}, fmt.Errorf("data URI wrappers are not accepted")
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(maxWebUIFileBytes) {
		return FileAttachment{}, fmt.Errorf("file exceeds %d decoded bytes", maxWebUIFileBytes)
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return FileAttachment{}, fmt.Errorf("invalid base64 data: %w", err)
	}
	if len(data) > maxWebUIFileBytes {
		return FileAttachment{}, fmt.Errorf("file exceeds %d decoded bytes", maxWebUIFileBytes)
	}
	return FileAttachment{Name: name, Data: data}, nil
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
	service, workspaceInfo, err := channel.resolveService(turnRequest.WorkspaceID)
	if err != nil {
		writeError(writer, http.StatusNotFound, chatResponse{Error: "workspace not found"})
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Connection", "keep-alive")
	// Disable proxy buffering (e.g. nginx) so events arrive incrementally.
	writer.Header().Set("X-Accel-Buffering", "no")
	writer.WriteHeader(http.StatusOK)
	flusher.Flush()

	stream := &sseStream{writer: writer, flusher: flusher}
	// WebUI follows the browser request lifetime and the service-wide runaway
	// valve; the tighter channel budgets are reserved for message delivery paths.
	response, err := service.HandleTurnWithOptions(request.Context(), TurnRequest{
		SessionID:        turnRequest.SessionID,
		Message:          turnRequest.Message,
		TurnID:           turnRequest.TurnID,
		Images:           turnRequest.Images,
		Files:            turnRequest.Files,
		ReasoningEffort:  turnRequest.ReasoningEffort,
		Mode:             turnRequest.Mode,
		ConversationType: turnRequest.ConversationType,
	}, TurnOptions{
		Stream:         true,
		TokenSink:      sseEventWriter{stream: stream, event: "message"},
		ProgressWriter: sseEventWriter{stream: stream, event: "progress"},
		ToolEventSink:  sseToolEventSink{stream: stream},
		GroupEventSink: sseGroupEventSink{stream: stream},
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

	var group *GroupInfo
	if response.ConversationType == ConversationTypeGroup {
		if info, infoErr := service.GroupInfoForSession(response.SessionID); infoErr == nil {
			group = &info
		}
	}
	writeSSEEvent(writer, flusher, "done", webUIDoneEvent{
		WorkspaceID:      workspaceInfo.ID,
		SessionID:        response.SessionID,
		RunID:            response.RunID,
		Reply:            sanitizeChannelReply(response.Reply),
		ReplyKind:        response.ReplyKind,
		APIType:          string(service.apiType),
		Model:            response.Model,
		Mode:             response.Mode,
		ConversationType: response.ConversationType,
		Group:            group,
		Generation:       response.Generation,
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

type sseGroupEventSink struct {
	stream *sseStream
}

func (sink sseGroupEventSink) EmitGroupEvent(event GroupEvent) {
	if sink.stream == nil || event.Kind == "" {
		return
	}
	_ = sink.stream.writeEvent(event.Kind, event)
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
