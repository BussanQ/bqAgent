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
	runner  *ChannelTurnRunner
}

func NewBotWebhookProcessor(service *Service, client *serverchanclient.BotClient, states *serverchanclient.BotStateStore, secret string) *BotWebhookProcessor {
	return &BotWebhookProcessor{
		service: service,
		client:  client,
		states:  states,
		secret:  secret,
		runner:  NewChannelTurnRunner(service),
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

	_, err := processor.runner.Process(ctx, ChannelTurnOptions{
		PeerKey:   strconv.FormatInt(update.ChatID, 10),
		DedupeKey: strconv.FormatInt(update.UpdateID, 10),
		Message:   update.Text,
		LoadState: func() (ChannelConversationState, error) {
			state, err := processor.states.Load(update.ChatID)
			if err != nil {
				return ChannelConversationState{}, err
			}
			return ChannelConversationState{
				SessionID:        state.SessionID,
				LastCompletedKey: formatInt64Key(state.LastCompletedUpdateID),
				PendingKey:       formatInt64Key(state.PendingUpdateID),
				PendingReply:     state.PendingReply,
				LastError:        state.LastError,
			}, nil
		},
		SaveState: func(next ChannelConversationState) error {
			state, err := processor.states.Load(update.ChatID)
			if err != nil {
				return err
			}
			state.SessionID = next.SessionID
			state.LastCompletedUpdateID = parseInt64Key(next.LastCompletedKey)
			state.PendingUpdateID = parseInt64Key(next.PendingKey)
			state.PendingReply = next.PendingReply
			state.LastError = next.LastError
			return processor.states.Save(state)
		},
		SendReply: func(ctx context.Context, reply string) error {
			_, err := processor.client.SendMessage(ctx, update.ChatID, reply)
			return err
		},
	})
	return err
}

func formatInt64Key(value int64) string {
	if value <= 0 {
		return ""
	}
	return strconv.FormatInt(value, 10)
}

func parseInt64Key(value string) int64 {
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}
