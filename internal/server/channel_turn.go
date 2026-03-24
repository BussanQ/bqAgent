package server

import (
	"context"
	"fmt"
	"strings"
)

type ChannelConversationState struct {
	SessionID        string
	LastCompletedKey string
	PendingKey       string
	PendingReply     string
	LastError        string
}

type ChannelTurnOptions struct {
	PeerKey   string
	DedupeKey string
	Message   string
	LoadState func() (ChannelConversationState, error)
	SaveState func(ChannelConversationState) error
	SendReply func(context.Context, string) error
}

type ChannelTurnRunner struct {
	service *Service
	locker  *KeyedLocker
}

func NewChannelTurnRunner(service *Service) *ChannelTurnRunner {
	return &ChannelTurnRunner{service: service, locker: NewKeyedLocker()}
}

func (runner *ChannelTurnRunner) Process(ctx context.Context, options ChannelTurnOptions) (ChannelConversationState, error) {
	if runner == nil || runner.service == nil {
		return ChannelConversationState{}, fmt.Errorf("service is required")
	}
	if options.LoadState == nil {
		return ChannelConversationState{}, fmt.Errorf("load state is required")
	}
	if options.SaveState == nil {
		return ChannelConversationState{}, fmt.Errorf("save state is required")
	}
	if options.SendReply == nil {
		return ChannelConversationState{}, fmt.Errorf("send reply is required")
	}

	unlock := runner.locker.Lock(strings.TrimSpace(options.PeerKey))
	defer unlock()

	state, err := options.LoadState()
	if err != nil {
		return ChannelConversationState{}, err
	}
	dedupeKey := strings.TrimSpace(options.DedupeKey)
	if dedupeKey != "" && dedupeKey == state.LastCompletedKey {
		return state, nil
	}
	if dedupeKey != "" && dedupeKey == state.PendingKey && state.PendingReply != "" {
		if err := options.SendReply(ctx, state.PendingReply); err != nil {
			state.LastError = err.Error()
			_ = options.SaveState(state)
			return state, err
		}
		state.LastCompletedKey = dedupeKey
		state.PendingKey = ""
		state.PendingReply = ""
		state.LastError = ""
		return state, options.SaveState(state)
	}

	response, err := runner.service.HandleTurn(ctx, TurnRequest{SessionID: state.SessionID, Message: options.Message})
	if err != nil {
		state.LastError = err.Error()
		if response.SessionID != "" {
			state.SessionID = response.SessionID
		}
		_ = options.SaveState(state)
		return state, err
	}

	state.SessionID = response.SessionID
	state.PendingKey = dedupeKey
	state.PendingReply = response.Reply
	state.LastError = ""
	if err := options.SaveState(state); err != nil {
		return state, err
	}
	if err := options.SendReply(ctx, response.Reply); err != nil {
		state.LastError = err.Error()
		_ = options.SaveState(state)
		return state, err
	}

	state.LastCompletedKey = dedupeKey
	state.PendingKey = ""
	state.PendingReply = ""
	state.LastError = ""
	return state, options.SaveState(state)
}
