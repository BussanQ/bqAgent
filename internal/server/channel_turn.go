package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var ErrChannelTurnInProgress = errors.New("channel turn already in progress")

const (
	channelTurnInProgressReply = "上一条消息仍在处理中，请稍后再试。"
	channelTurnTimedOutReply   = "处理超时，请稍后重试。"
	channelTurnFailedReply     = "处理出错，请稍后重试。"
)

// defaultChannelTurnTimeout bounds one full channel turn (all agent loop
// iterations and tool calls); each LLM HTTP request is separately bounded by
// the client's own timeout.
const defaultChannelTurnTimeout = 10 * time.Minute

var channelTurnTimeoutNanos atomic.Int64

func ChannelTurnTimeout() time.Duration {
	if nanos := channelTurnTimeoutNanos.Load(); nanos > 0 {
		return time.Duration(nanos)
	}
	return defaultChannelTurnTimeout
}

func SetChannelTurnTimeout(timeout time.Duration) {
	if timeout <= 0 {
		channelTurnTimeoutNanos.Store(0)
		return
	}
	channelTurnTimeoutNanos.Store(int64(timeout))
}

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
				// Best-effort busy notice; the caller still gets ErrChannelTurnInProgress.
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
			// Best-effort persist; the send error below is the one that matters.
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
		// Best-effort persist; the turn error below is the one that matters.
		_ = options.SaveState(state)
		notifyChannelTurnFailure(options, err)
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
		// Best-effort persist; the send error below is the one that matters.
		_ = options.SaveState(state)
		return state, err
	}

	state.LastCompletedKey = dedupeKey
	state.PendingKey = ""
	state.PendingReply = ""
	state.LastError = ""
	return state, options.SaveState(state)
}

// notifyChannelTurnFailure tells the user a turn failed. It only uses the
// explicit SendProgress sender: synchronous callers (HTTP chat) already
// surface the error in their response, and only async channels configure
// progress delivery. The turn context is usually already expired or canceled
// at this point, so delivery uses a fresh short-lived context. Cancellation
// (e.g. shutdown) sends nothing.
func notifyChannelTurnFailure(options ChannelTurnOptions, turnErr error) {
	if errors.Is(turnErr, context.Canceled) {
		return
	}
	sender := options.SendProgress
	if sender == nil {
		return
	}
	reply := channelTurnFailedReply
	if errors.Is(turnErr, context.DeadlineExceeded) {
		reply = channelTurnTimedOutReply
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = sender(ctx, reply)
}

// channelStateFuncs adapts a channel-specific chat-state store to the
// LoadState/SaveState callbacks of ChannelTurnOptions, so each channel only
// supplies the field mapping. SaveState reloads before writing to avoid
// clobbering fields the conversation state does not carry.
func channelStateFuncs[S any](
	load func() (S, error),
	get func(S) ChannelConversationState,
	set func(*S, ChannelConversationState),
	save func(S) error,
) (func() (ChannelConversationState, error), func(ChannelConversationState) error) {
	loadState := func() (ChannelConversationState, error) {
		state, err := load()
		if err != nil {
			return ChannelConversationState{}, err
		}
		return get(state), nil
	}
	saveState := func(next ChannelConversationState) error {
		state, err := load()
		if err != nil {
			return err
		}
		set(&state, next)
		return save(state)
	}
	return loadState, saveState
}
