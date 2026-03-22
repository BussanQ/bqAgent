package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	serverchanclient "bqagent/internal/serverchan"
)

const (
	maxRequestBodySize = 1 << 20
	requestTimeout     = 2 * time.Minute
)

type HandlerOptions struct {
	Service             *Service
	ServerChanClient    *serverchanclient.Client
	BotWebhookProcessor *BotWebhookProcessor
}

type handler struct {
	service             *Service
	serverChanClient    *serverchanclient.Client
	botWebhookProcessor *BotWebhookProcessor
}

type chatResponse struct {
	SessionID          string `json:"session_id,omitempty"`
	Reply              string `json:"reply,omitempty"`
	ServerChanResponse string `json:"serverchan_response,omitempty"`
	Error              string `json:"error,omitempty"`
}

func NewHandler(options HandlerOptions) http.Handler {
	serverChanClient := options.ServerChanClient
	if serverChanClient == nil {
		serverChanClient = serverchanclient.NewClient(nil)
	}
	handler := &handler{
		service:             options.Service,
		serverChanClient:    serverChanClient,
		botWebhookProcessor: options.BotWebhookProcessor,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handler.handleHealth)
	mux.HandleFunc("/api/v1/chat", handler.handleChat)
	mux.HandleFunc("/api/v1/serverchan/chat", handler.handleServerChanChat)
	mux.HandleFunc("/api/v1/serverchan/bot/webhook", handler.handleServerChanBotWebhook)
	return mux
}

func (handler *handler) handleHealth(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
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
	writeJSON(writer, http.StatusOK, chatResponse{SessionID: response.SessionID, Reply: response.Reply})
}

func (handler *handler) handleServerChanChat(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
		return
	}

	values, err := readValues(writer, request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: err.Error()})
		return
	}
	serverChanRequest, err := serverchanclient.ParseChatRequest(values)
	if err != nil {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(request.Context(), requestTimeout)
	defer cancel()

	response, err := handler.service.HandleTurn(ctx, TurnRequest{SessionID: serverChanRequest.SessionID, Message: serverChanRequest.Message})
	if err != nil {
		writeError(writer, http.StatusInternalServerError, chatResponse{Error: err.Error()})
		return
	}

	title, desp := serverchanclient.BuildReply(serverChanRequest.Title, response.Reply)
	delivery, err := handler.serverChanClient.Send(ctx, serverChanRequest.SendKey, title, desp)
	if err != nil {
		writeError(writer, http.StatusBadGateway, chatResponse{SessionID: response.SessionID, Reply: response.Reply, Error: err.Error()})
		return
	}

	writeJSON(writer, http.StatusOK, chatResponse{SessionID: response.SessionID, Reply: response.Reply, ServerChanResponse: delivery})
}

func (handler *handler) handleServerChanBotWebhook(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writePlainText(writer, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if handler.botWebhookProcessor == nil || !handler.botWebhookProcessor.Configured() {
		writePlainText(writer, http.StatusServiceUnavailable, "serverchan bot is not configured")
		return
	}
	if !handler.botWebhookProcessor.VerifySecret(request.Header) {
		writePlainText(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !strings.Contains(strings.ToLower(request.Header.Get("Content-Type")), "application/json") {
		writePlainText(writer, http.StatusBadRequest, "invalid payload")
		return
	}

	request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBodySize)
	update, err := serverchanclient.ParseBotWebhookPayload(request.Body)
	if err != nil {
		if err == serverchanclient.ErrIgnoreBotUpdate {
			writePlainText(writer, http.StatusOK, "ok")
			return
		}
		writePlainText(writer, http.StatusBadRequest, "invalid payload")
		return
	}

	writePlainText(writer, http.StatusOK, "ok")
	go func(update serverchanclient.BotUpdate) {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		if err := handler.botWebhookProcessor.ProcessUpdate(ctx, update); err != nil {
			log.Printf("serverchan bot webhook processing failed: %v", err)
		}
	}(update)
}

func parseTurnRequest(values map[string]string) (TurnRequest, error) {
	message := strings.TrimSpace(values["message"])
	if message == "" {
		message = strings.TrimSpace(firstNonEmpty(values["desp"], values["text"]))
	}
	if message == "" {
		return TurnRequest{}, fmt.Errorf("message is required")
	}
	return TurnRequest{
		SessionID: strings.TrimSpace(firstNonEmpty(values["session_id"], values["session"])),
		Message:   message,
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
