package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"bqagent/internal/qq"
)

type qqGateway interface {
	Configured() bool
	Connect(context.Context, qq.GatewaySessionState, func(context.Context, qq.Update) error) (qq.GatewaySessionState, error)
}

type QQChannel struct {
	service       *Service
	client        *qq.Client
	gateway       qqGateway
	states        *qq.StateStore
	gatewayStates *qq.GatewayStateStore
	runner        *ChannelTurnRunner
	mu            sync.Mutex
	started       bool
}

func NewQQChannel(service *Service, client *qq.Client, gateway qqGateway, states *qq.StateStore, gatewayStates *qq.GatewayStateStore) *QQChannel {
	return &QQChannel{
		service:       service,
		client:        client,
		gateway:       gateway,
		states:        states,
		gatewayStates: gatewayStates,
		runner:        NewChannelTurnRunner(service),
	}
}

func (channel *QQChannel) Name() string {
	return "qq"
}

func (channel *QQChannel) Enabled() bool {
	return channel != nil && channel.service != nil && channel.states != nil && channel.gatewayStates != nil
}

func (channel *QQChannel) Configured() bool {
	return channel != nil && channel.service != nil && channel.client != nil && channel.client.Configured() && channel.gateway != nil && channel.gateway.Configured() && channel.states != nil && channel.gatewayStates != nil
}

func (channel *QQChannel) RegisterRoutes(*http.ServeMux) {}

func (channel *QQChannel) Start(ctx context.Context) {
	if !channel.Enabled() {
		return
	}
	channel.mu.Lock()
	if channel.started {
		channel.mu.Unlock()
		return
	}
	channel.started = true
	channel.mu.Unlock()
	if !channel.Configured() {
		log.Printf("qq bot is not configured")
		return
	}
	go channel.runGateway(ctx)
}

func (channel *QQChannel) runGateway(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		state, err := channel.gatewayStates.Load()
		if err != nil {
			log.Printf("qq gateway state load failed: %v", err)
			state.LastError = err.Error()
		}
		next, err := channel.gateway.Connect(ctx, state, func(_ context.Context, update qq.Update) error {
			channel.dispatchUpdate(update)
			return nil
		})
		if saveErr := channel.gatewayStates.Save(next); saveErr != nil {
			log.Printf("qq gateway state save failed: %v", saveErr)
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			if errors.Is(err, qq.ErrGatewayInvalidSession) {
				if clearErr := channel.gatewayStates.ClearSession(); clearErr != nil {
					log.Printf("qq gateway session clear failed: %v", clearErr)
				}
			}
			log.Printf("qq gateway connection ended: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (channel *QQChannel) dispatchUpdate(update qq.Update) {
	go func() {
		turnCtx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		if err := channel.processUpdate(turnCtx, update); err != nil && !errors.Is(err, ErrChannelTurnInProgress) {
			log.Printf("qq update processing failed: %v", err)
		}
	}()
}

func (channel *QQChannel) processUpdate(ctx context.Context, update qq.Update) error {
	if !channel.Configured() {
		return errors.New("qq bot is not configured")
	}
	sender := newQQUpdateSender(channel.client, update)
	_, err := channel.runner.TryProcess(ctx, ChannelTurnOptions{
		PeerKey:   update.PeerKey,
		DedupeKey: update.DedupeKey,
		Message:   update.Text,
		LoadState: func() (ChannelConversationState, error) {
			state, err := channel.states.Load(update.PeerKey)
			if err != nil {
				return ChannelConversationState{}, err
			}
			return ChannelConversationState{
				SessionID:        state.SessionID,
				LastCompletedKey: state.LastCompletedKey,
				PendingKey:       state.PendingKey,
				PendingReply:     state.PendingReply,
				LastError:        state.LastError,
			}, nil
		},
		SaveState: func(next ChannelConversationState) error {
			state, err := channel.states.Load(update.PeerKey)
			if err != nil {
				return err
			}
			state.SessionID = next.SessionID
			state.LastCompletedKey = next.LastCompletedKey
			state.PendingKey = next.PendingKey
			state.PendingReply = next.PendingReply
			state.LastError = next.LastError
			return channel.states.Save(state)
		},
		SendReply:    sender.SendReply,
		SendProgress: sender.SendProgress,
	})
	return err
}

type qqUpdateSender struct {
	client  *qq.Client
	update  qq.Update
	mu      sync.Mutex
	nextSeq int
}

func newQQUpdateSender(client *qq.Client, update qq.Update) *qqUpdateSender {
	return &qqUpdateSender{client: client, update: update, nextSeq: 1}
}

func (sender *qqUpdateSender) SendReply(ctx context.Context, message string) error {
	return sender.send(ctx, message)
}

func (sender *qqUpdateSender) SendProgress(ctx context.Context, message string) error {
	return sender.send(ctx, message)
}

func (sender *qqUpdateSender) send(ctx context.Context, message string) error {
	sender.mu.Lock()
	msgSeq := sender.nextSeq
	sender.nextSeq++
	sender.mu.Unlock()
	_, err := sender.client.SendText(ctx, qq.SendTarget{
		Kind:        sender.update.Kind,
		UserOpenID:  sender.update.UserOpenID,
		GroupOpenID: sender.update.GroupOpenID,
		MsgID:       sender.update.MessageID,
		MsgSeq:      msgSeq,
	}, message)
	return err
}
