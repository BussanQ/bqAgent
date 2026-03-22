package server

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	serverchanclient "bqagent/internal/serverchan"
)

type BotWebhookProcessor struct {
	service *Service
	client  *serverchanclient.BotClient
	states  *serverchanclient.BotStateStore
	secret  string
	locker  *KeyedLocker
}

func NewBotWebhookProcessor(service *Service, client *serverchanclient.BotClient, states *serverchanclient.BotStateStore, secret string) *BotWebhookProcessor {
	return &BotWebhookProcessor{
		service: service,
		client:  client,
		states:  states,
		secret:  secret,
		locker:  NewKeyedLocker(),
	}
}

func (processor *BotWebhookProcessor) Configured() bool {
	return processor != nil && processor.service != nil && processor.client != nil && processor.client.Configured() && processor.states != nil
}

func (processor *BotWebhookProcessor) VerifySecret(header http.Header) bool {
	if processor == nil {
		return false
	}
	return serverchanclient.VerifyWebhookSecret(header, processor.secret)
}

func (processor *BotWebhookProcessor) ProcessUpdate(ctx context.Context, update serverchanclient.BotUpdate) error {
	if !processor.Configured() {
		return fmt.Errorf("serverchan bot is not configured")
	}
	unlock := processor.locker.Lock(strconv.FormatInt(update.ChatID, 10))
	defer unlock()

	state, err := processor.states.Load(update.ChatID)
	if err != nil {
		return err
	}
	if update.UpdateID <= state.LastCompletedUpdateID {
		return nil
	}
	if state.PendingUpdateID == update.UpdateID && state.PendingReply != "" {
		if _, err := processor.client.SendMessage(ctx, update.ChatID, state.PendingReply); err != nil {
			state.LastError = err.Error()
			_ = processor.states.Save(state)
			return err
		}
		state.LastCompletedUpdateID = update.UpdateID
		state.PendingUpdateID = 0
		state.PendingReply = ""
		state.LastError = ""
		return processor.states.Save(state)
	}

	response, err := processor.service.HandleTurn(ctx, TurnRequest{SessionID: state.SessionID, Message: update.Text})
	if err != nil {
		state.LastError = err.Error()
		if response.SessionID != "" {
			state.SessionID = response.SessionID
		}
		_ = processor.states.Save(state)
		return err
	}

	state.SessionID = response.SessionID
	state.PendingUpdateID = update.UpdateID
	state.PendingReply = response.Reply
	state.LastError = ""
	if err := processor.states.Save(state); err != nil {
		return err
	}

	if _, err := processor.client.SendMessage(ctx, update.ChatID, response.Reply); err != nil {
		state.LastError = err.Error()
		_ = processor.states.Save(state)
		return err
	}

	state.LastCompletedUpdateID = update.UpdateID
	state.PendingUpdateID = 0
	state.PendingReply = ""
	state.LastError = ""
	return processor.states.Save(state)
}
