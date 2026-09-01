package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"bqagent/internal/providerconfig"
)

const (
	maxRequestBodySize = 1 << 20
	requestTimeout     = 2 * time.Minute
)

type HandlerOptions struct {
	Service    *Service
	Workspaces *WorkspaceRegistry
	Channels   []Channel
}

type handler struct {
	service    *Service
	workspaces *WorkspaceRegistry
	providers  *providerconfig.Store
	providerMu sync.Mutex
}

type chatResponse struct {
	SessionID          string           `json:"session_id,omitempty"`
	RunID              string           `json:"run_id,omitempty"`
	Reply              string           `json:"reply,omitempty"`
	ServerChanResponse string           `json:"serverchan_response,omitempty"`
	Error              string           `json:"error,omitempty"`
	ConversationType   ConversationType `json:"conversation_type,omitempty"`
}

type statusResponse struct {
	Status string         `json:"status"`
	LLM    RuntimeLLMInfo `json:"llm"`
}

func NewHandler(options HandlerOptions) http.Handler {
	handler := &handler{service: options.Service, workspaces: options.Workspaces}
	if options.Service != nil && strings.TrimSpace(options.Service.agentDir) != "" {
		handler.providers = providerconfig.NewStore(options.Service.agentDir)
		handler.applySavedProvider(options.Service)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handler.handleHealth)
	mux.HandleFunc("/api/v1/status", handler.handleStatus)
	mux.HandleFunc("/api/v1/webui/providers", handler.handleProviders)
	mux.HandleFunc("/api/v1/webui/provider-selection", handler.handleProviderSelection)
	mux.HandleFunc("/api/v1/webui/provider-models", handler.handleProviderModels)
	mux.HandleFunc("/api/v1/webui/conversations", handler.handleConversations)
	mux.HandleFunc("/api/v1/webui/conversations/", handler.handleConversationHistory)
	mux.HandleFunc("/api/v1/webui/group/participants", handler.handleGroupParticipants)
	mux.HandleFunc("/api/v1/chat", handler.handleChat)
	mux.HandleFunc("/api/v1/chat/stop", handler.handleStopTurn)
	mux.HandleFunc("/api/v1/runs/", handler.handleRun)
	for _, channel := range options.Channels {
		if channel == nil || !channel.Enabled() {
			continue
		}
		channel.RegisterRoutes(mux)
	}
	return withRequestLogging(mux)
}

func (handler *handler) serviceForWorkspace(workspaceID string) (*Service, error) {
	if handler == nil || handler.service == nil {
		return nil, fmt.Errorf("service is unavailable")
	}
	if handler.workspaces == nil || strings.TrimSpace(workspaceID) == "" {
		return handler.service, nil
	}
	service, _, err := handler.workspaces.Resolve(workspaceID)
	return service, err
}

type responseRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (recorder *responseRecorder) WriteHeader(statusCode int) {
	recorder.statusCode = statusCode
	recorder.ResponseWriter.WriteHeader(statusCode)
}

// Flush keeps streaming (SSE) handlers working through the logging wrapper:
// embedding the ResponseWriter interface does not promote the underlying
// Flusher, so we delegate explicitly when the wrapped writer supports it.
func (recorder *responseRecorder) Flush() {
	if flusher, ok := recorder.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func withRequestLogging(next http.Handler) http.Handler {
	if next == nil {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			http.NotFound(writer, request)
		})
	}

	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		startedAt := time.Now()
		recorder := &responseRecorder{ResponseWriter: writer, statusCode: http.StatusOK}
		next.ServeHTTP(recorder, request)
		log.Printf("%s %s %d %s remote=%s", request.Method, request.URL.Path, recorder.statusCode, time.Since(startedAt).Round(time.Millisecond), request.RemoteAddr)
	})
}

func (handler *handler) handleHealth(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

func (handler *handler) handleStatus(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
		return
	}
	service, err := handler.serviceForWorkspace(request.URL.Query().Get("workspace_id"))
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, os.ErrNotExist) {
			status = http.StatusNotFound
		}
		writeError(writer, status, chatResponse{Error: err.Error()})
		return
	}
	sessionID := request.URL.Query().Get("session_id")
	writeJSON(writer, http.StatusOK, statusResponse{Status: "ok", LLM: service.RuntimeLLMInfoForSession(sessionID)})
}

func (handler *handler) handleChat(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
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

	ctx, cancel := context.WithTimeout(request.Context(), requestTimeout)
	defer cancel()

	response, err := handler.service.HandleTurn(ctx, turnRequest)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, chatResponse{Error: err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, chatResponse{SessionID: response.SessionID, RunID: response.RunID, Reply: response.Reply, ConversationType: response.ConversationType})
}

func (handler *handler) handleStopTurn(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
		return
	}
	values, err := readValues(writer, request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: err.Error()})
		return
	}
	turnID := strings.TrimSpace(values["turn_id"])
	if !validTurnID(turnID) {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: "valid turn_id is required"})
		return
	}
	service, err := handler.serviceForWorkspace(values["workspace_id"])
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, os.ErrNotExist) {
			status = http.StatusNotFound
		}
		writeError(writer, status, chatResponse{Error: err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"stopped": service.StopTurn(turnID)})
}

func (handler *handler) handleRun(writer http.ResponseWriter, request *http.Request) {
	service, err := handler.serviceForWorkspace(request.URL.Query().Get("workspace_id"))
	if errors.Is(err, os.ErrNotExist) {
		writeError(writer, http.StatusNotFound, chatResponse{Error: err.Error()})
		return
	}
	if err != nil || service.traceStore == nil {
		writeError(writer, http.StatusServiceUnavailable, chatResponse{Error: "trace store unavailable"})
		return
	}
	path := strings.TrimPrefix(request.URL.Path, "/api/v1/runs/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: "run id is required"})
		return
	}
	runID := parts[0]
	if request.Method == http.MethodGet && len(parts) == 1 {
		meta, err := service.traceStore.Load(runID)
		if err != nil {
			writeError(writer, http.StatusNotFound, chatResponse{Error: err.Error()})
			return
		}
		writeJSON(writer, http.StatusOK, meta)
		return
	}
	if request.Method == http.MethodPost && len(parts) == 2 && parts[1] == "feedback" {
		values, err := readValues(writer, request)
		if err != nil {
			writeError(writer, http.StatusBadRequest, chatResponse{Error: err.Error()})
			return
		}
		feedback, err := service.traceStore.AddFeedback(runID, values["rating"], values["comment"], "http")
		if err != nil {
			writeError(writer, http.StatusBadRequest, chatResponse{Error: err.Error()})
			return
		}
		writeJSON(writer, http.StatusOK, feedback)
		return
	}
	writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
}

func parseTurnRequest(values map[string]string) (TurnRequest, error) {
	message := strings.TrimSpace(values["message"])
	if message == "" {
		message = strings.TrimSpace(firstNonEmpty(values["desp"], values["text"]))
	}
	if message == "" {
		return TurnRequest{}, fmt.Errorf("message is required")
	}
	turnID := strings.TrimSpace(values["turn_id"])
	if turnID != "" && !validTurnID(turnID) {
		return TurnRequest{}, fmt.Errorf("invalid turn_id")
	}
	var conversationType ConversationType
	if rawConversationType := strings.TrimSpace(values["conversation_type"]); rawConversationType != "" {
		parsedConversationType, parseErr := parseConversationType(rawConversationType)
		if parseErr != nil {
			return TurnRequest{}, parseErr
		}
		conversationType = parsedConversationType
	}
	return TurnRequest{
		SessionID:        strings.TrimSpace(firstNonEmpty(values["session_id"], values["session"])),
		Message:          message,
		TurnID:           turnID,
		ConversationType: conversationType,
	}, nil
}

func readValues(writer http.ResponseWriter, request *http.Request) (map[string]string, error) {
	values := make(map[string]string)
	if request.Body == nil {
		return values, nil
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBodySize)
	contentType := strings.ToLower(request.Header.Get("Content-Type"))
	if strings.Contains(contentType, "application/json") {
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			return nil, err
		}
		for key, value := range payload {
			switch typed := value.(type) {
			case string:
				values[key] = typed
			case nil:
				continue
			default:
				values[key] = fmt.Sprint(typed)
			}
		}
		return values, nil
	}

	if err := request.ParseForm(); err != nil {
		return nil, err
	}
	for key, entries := range request.Form {
		if len(entries) == 0 {
			continue
		}
		values[key] = entries[0]
	}
	return values, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func writeError(writer http.ResponseWriter, statusCode int, payload chatResponse) {
	writeJSON(writer, statusCode, payload)
}

func writeJSON(writer http.ResponseWriter, statusCode int, payload any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(statusCode)
	if err := json.NewEncoder(writer).Encode(payload); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

func writePlainText(writer http.ResponseWriter, statusCode int, body string) {
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.WriteHeader(statusCode)
	_, _ = writer.Write([]byte(body))
}
