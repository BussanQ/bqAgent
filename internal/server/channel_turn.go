package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

var ErrChannelTurnInProgress = errors.New("channel turn already in progress")

const channelTurnInProgressReply = "上一条消息仍在处理中，请稍后再试。"

type ChannelConversationState struct {
	SessionID        string
	LastCompletedKey string
	PendingKey       string
	PendingReply     string
	LastError        string
}

type ChannelTurnOptions struct {
	PeerKey      string
	DedupeKey    string
	Message      string
	LoadState    func() (ChannelConversationState, error)
	SaveState    func(ChannelConversationState) error
	SendReply    func(context.Context, string) error
	SendProgress func(context.Context, string) error
}

func (options ChannelTurnOptions) progressSender() func(context.Context, string) error {
	if options.SendProgress != nil {
		return options.SendProgress
	}
	return options.SendReply
}

type ChannelTurnRunner struct {
	service *Service
	locker  *KeyedLocker
}

func NewChannelTurnRunner(service *Service) *ChannelTurnRunner {
	return &ChannelTurnRunner{service: service, locker: NewKeyedLocker()}
}

type channelProgressWriter struct {
	ctx          context.Context
	sendProgress func(context.Context, string) error
	mu           sync.Mutex
}

func newChannelProgressWriter(ctx context.Context, sendProgress func(context.Context, string) error) *channelProgressWriter {
	if sendProgress == nil {
		return nil
	}
	return &channelProgressWriter{ctx: ctx, sendProgress: sendProgress}
}

func (writer *channelProgressWriter) Write(data []byte) (int, error) {
	if writer == nil || writer.sendProgress == nil {
		return len(data), nil
	}
	message := strings.TrimSpace(string(data))
	if message == "" {
		return len(data), nil
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if err := writer.sendProgress(writer.ctx, message); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (runner *ChannelTurnRunner) Process(ctx context.Context, options ChannelTurnOptions) (ChannelConversationState, error) {
	return runner.process(ctx, options, true)
}

func (runner *ChannelTurnRunner) TryProcess(ctx context.Context, options ChannelTurnOptions) (ChannelConversationState, error) {
	return runner.process(ctx, options, false)
}

func (runner *ChannelTurnRunner) process(ctx context.Context, options ChannelTurnOptions, waitForLock bool) (ChannelConversationState, error) {
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

	var state ChannelConversationState
	peerKey := strings.TrimSpace(options.PeerKey)
	var unlock func()
	if waitForLock {
		unlock = runner.locker.Lock(peerKey)
	} else {
		var locked bool
		unlock, locked = runner.locker.TryLock(peerKey)
		if !locked {
			if sendProgress := options.progressSender(); sendProgress != nil {
				_ = sendProgress(ctx, channelTurnInProgressReply)
			}
			return state, ErrChannelTurnInProgress
		}
	}
	defer unlock()

	var err error
	state, err = options.LoadState()
	if err != nil {
		return ChannelConversationState{}, err
	}
	dedupeKey := strings.TrimSpace(options.DedupeKey)
	if dedupeKey != "" && dedupeKey == state.LastCompletedKey {
		return state, nil
	}
	if dedupeKey != "" && dedupeKey == state.PendingKey && state.PendingReply != "" {
		state.PendingReply = sanitizeChannelReply(state.PendingReply)
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

	progressWriter := newChannelProgressWriter(ctx, options.progressSender())
	response, err := runner.service.HandleTurnWithOptions(ctx, TurnRequest{SessionID: state.SessionID, Message: options.Message}, TurnOptions{ProgressWriter: progressWriter})
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
	state.PendingReply = sanitizeChannelReply(response.Reply)
	state.LastError = ""
	if err := options.SaveState(state); err != nil {
		return state, err
	}
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
