package server

import (
	"context"
	"log"
	"net/http"
	"strings"
	"sync"

	serverchanclient "bqagent/internal/serverchan"
)

type ServerChanChannel struct {
	service             *Service
	serverChanClient    *serverchanclient.Client
	botWebhookProcessor *BotWebhookProcessor
	runner              *ChannelTurnRunner
	mu                  sync.Mutex
	baseCtx             context.Context
	turns               sync.WaitGroup
}

func NewServerChanChannel(service *Service, serverChanClient *serverchanclient.Client, botWebhookProcessor *BotWebhookProcessor) *ServerChanChannel {
	if serverChanClient == nil {
		serverChanClient = serverchanclient.NewClient(nil)
	}
	return &ServerChanChannel{
		service:             service,
		serverChanClient:    serverChanClient,
		botWebhookProcessor: botWebhookProcessor,
		runner:              NewChannelTurnRunner(service),
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

func (channel *ServerChanChannel) Start(ctx context.Context) {
	channel.mu.Lock()
	channel.baseCtx = ctx
	channel.mu.Unlock()
}

// WaitTurns blocks until all in-flight webhook goroutines finish.
func (channel *ServerChanChannel) WaitTurns() {
	channel.turns.Wait()
}

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

	var state ChannelConversationState
	var deliveredReply string
	var delivery string
	state, err = channel.runner.Process(ctx, ChannelTurnOptions{
		PeerKey: "serverchan:" + serverChanRequest.SendKey,
		Message: serverChanRequest.Message,
		LoadState: func() (ChannelConversationState, error) {
			return ChannelConversationState{SessionID: serverChanRequest.SessionID}, nil
		},
		SaveState: func(next ChannelConversationState) error {
			state = next
			return nil
		},
		SendReply: func(ctx context.Context, reply string) error {
			deliveredReply = sanitizeChannelReply(reply)
			title, desp := serverchanclient.BuildReply(serverChanRequest.Title, deliveredReply)
			result, sendErr := channel.serverChanClient.Send(ctx, serverChanRequest.SendKey, title, desp)
			if sendErr == nil {
				delivery = result
			}
			return sendErr
		},
	})
	if err != nil {
		statusCode := http.StatusInternalServerError
		if state.SessionID != "" || deliveredReply != "" {
			statusCode = http.StatusBadGateway
		}
		writeError(writer, statusCode, chatResponse{SessionID: state.SessionID, Reply: deliveredReply, Error: err.Error()})
		return
	}

	writeJSON(writer, http.StatusOK, chatResponse{SessionID: state.SessionID, Reply: deliveredReply, ServerChanResponse: delivery})
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
	channel.mu.Lock()
	baseCtx := channel.baseCtx
	channel.mu.Unlock()
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	channel.turns.Add(1)
	go func(update serverchanclient.BotUpdate) {
		defer channel.turns.Done()
		ctx, cancel := context.WithTimeout(baseCtx, ChannelTurnTimeout())
		defer cancel()
		if err := channel.botWebhookProcessor.ProcessUpdate(ctx, update); err != nil {
			log.Printf("serverchan bot webhook processing failed: %v", err)
		}
	}(update)
}
