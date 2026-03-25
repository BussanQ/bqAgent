package server

import (
	"context"
	"log"
	"net/http"
	"strings"

	serverchanclient "bqagent/internal/serverchan"
)

type ServerChanChannel struct {
	service             *Service
	serverChanClient    *serverchanclient.Client
	botWebhookProcessor *BotWebhookProcessor
}

func NewServerChanChannel(service *Service, serverChanClient *serverchanclient.Client, botWebhookProcessor *BotWebhookProcessor) *ServerChanChannel {
	if serverChanClient == nil {
		serverChanClient = serverchanclient.NewClient(nil)
	}
	return &ServerChanChannel{
		service:             service,
		serverChanClient:    serverChanClient,
		botWebhookProcessor: botWebhookProcessor,
	}
}

func (channel *ServerChanChannel) Name() string {
	return "serverchan"
}

func (channel *ServerChanChannel) Enabled() bool {
	return channel != nil && channel.service != nil
}

func (channel *ServerChanChannel) RegisterRoutes(mux *http.ServeMux) {
	if !channel.Enabled() || mux == nil {
		return
	}
	mux.HandleFunc("/api/v1/serverchan/chat", channel.handleChat)
	mux.HandleFunc("/api/v1/serverchan/bot/webhook", channel.handleBotWebhook)
}

func (channel *ServerChanChannel) Start(context.Context) {}

func (channel *ServerChanChannel) handleChat(writer http.ResponseWriter, request *http.Request) {
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

	response, err := channel.service.HandleTurn(ctx, TurnRequest{SessionID: serverChanRequest.SessionID, Message: serverChanRequest.Message})
	if err != nil {
		writeError(writer, http.StatusInternalServerError, chatResponse{Error: err.Error()})
		return
	}
	response.Reply = sanitizeChannelReply(response.Reply)

	title, desp := serverchanclient.BuildReply(serverChanRequest.Title, response.Reply)
	delivery, err := channel.serverChanClient.Send(ctx, serverChanRequest.SendKey, title, desp)
	if err != nil {
		writeError(writer, http.StatusBadGateway, chatResponse{SessionID: response.SessionID, Reply: response.Reply, Error: err.Error()})
		return
	}

	writeJSON(writer, http.StatusOK, chatResponse{SessionID: response.SessionID, Reply: response.Reply, ServerChanResponse: delivery})
}

func (channel *ServerChanChannel) handleBotWebhook(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writePlainText(writer, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if channel.botWebhookProcessor == nil || !channel.botWebhookProcessor.Configured() {
		writePlainText(writer, http.StatusServiceUnavailable, "serverchan bot is not configured")
		return
	}
	if !channel.botWebhookProcessor.VerifySecret(request.Header) {
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
		if err := channel.botWebhookProcessor.ProcessUpdate(ctx, update); err != nil {
			log.Printf("serverchan bot webhook processing failed: %v", err)
		}
	}(update)
}
