package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"bqagent/internal/weixin"
)

const ilinkIdleInterval = time.Second

type IlinkChannel struct {
	service *Service
	client  *weixin.Client
	tokens  *weixin.TokenStore
	poller  *weixin.PollerStateStore
	chats   *weixin.ChatStateStore
	runner  *ChannelTurnRunner

	mu            sync.Mutex
	started       bool
	pollerRunning bool
	lastError     string
	login         ilinkLoginState
}

type ilinkLoginState struct {
	InProgress       bool
	Status           string
	QRCode           string
	QRCodeImgContent string
	LastError        string
	UpdatedAt        time.Time
}

type IlinkStatus struct {
	Name             string    `json:"name"`
	Enabled          bool      `json:"enabled"`
	LoggedIn         bool      `json:"logged_in"`
	LoginInProgress  bool      `json:"login_in_progress,omitempty"`
	LoginStatus      string    `json:"login_status,omitempty"`
	QRCode           string    `json:"qrcode,omitempty"`
	QRCodeImgContent string    `json:"qrcode_img_content,omitempty"`
	PollerRunning    bool      `json:"poller_running,omitempty"`
	AccountID        string    `json:"account_id,omitempty"`
	UserID           string    `json:"user_id,omitempty"`
	BaseURL          string    `json:"base_url,omitempty"`
	LastError        string    `json:"last_error,omitempty"`
	UpdatedAt        time.Time `json:"updated_at,omitempty"`
}

func NewIlinkChannel(service *Service, client *weixin.Client, tokens *weixin.TokenStore, poller *weixin.PollerStateStore, chats *weixin.ChatStateStore) *IlinkChannel {
	return &IlinkChannel{
		service: service,
		client:  client,
		tokens:  tokens,
		poller:  poller,
		chats:   chats,
		runner:  NewChannelTurnRunner(service),
	}
}

func (channel *IlinkChannel) Name() string {
	return "ilink"
}

func (channel *IlinkChannel) Enabled() bool {
	return channel != nil && channel.service != nil && channel.client != nil && channel.tokens != nil && channel.poller != nil && channel.chats != nil
}

func (channel *IlinkChannel) RegisterRoutes(mux *http.ServeMux) {
	if !channel.Enabled() || mux == nil {
		return
	}
	mux.HandleFunc("/api/v1/weixin/ilink/status", channel.handleStatus)
	mux.HandleFunc("/api/v1/weixin/ilink/login", channel.handleLogin)
}

func (channel *IlinkChannel) Start(ctx context.Context) {
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
	go channel.run(ctx)
}

func (channel *IlinkChannel) handleStatus(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
		return
	}
	status := channel.Status()
	writeJSON(writer, http.StatusOK, status)
}

func (channel *IlinkChannel) handleLogin(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
		return
	}
	status, err := channel.StartLogin(request.Context())
	if err != nil {
		writeError(writer, http.StatusBadGateway, chatResponse{Error: err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, status)
}

func (channel *IlinkChannel) StartLogin(ctx context.Context) (IlinkStatus, error) {
	if !channel.Enabled() {
		return IlinkStatus{}, fmt.Errorf("ilink channel is not configured")
	}
	channel.mu.Lock()
	if channel.login.InProgress {
		channel.mu.Unlock()
		return channel.Status(), nil
	}
	channel.mu.Unlock()

	tokenState, err := channel.tokens.Load()
	if err != nil {
		channel.setLastError(err.Error())
		return IlinkStatus{}, err
	}
	if strings.TrimSpace(tokenState.BotToken) != "" {
		return channel.Status(), nil
	}

	response, err := channel.client.GetBotQRCode(ctx)
	if err != nil {
		channel.setLastError(err.Error())
		return IlinkStatus{}, err
	}
	channel.mu.Lock()
	channel.login = ilinkLoginState{
		InProgress:       true,
		Status:           "pending",
		QRCode:           strings.TrimSpace(response.QRCode),
		QRCodeImgContent: strings.TrimSpace(response.QRCodeImgBase64),
		LastError:        "",
		UpdatedAt:        time.Now().UTC(),
	}
	channel.mu.Unlock()

	go channel.pollLoginStatus(strings.TrimSpace(response.QRCode))
	return channel.Status(), nil
}

func (channel *IlinkChannel) Status() IlinkStatus {
	tokenState, err := channel.tokens.Load()
	loadError := ""
	if err != nil {
		loadError = err.Error()
	}

	channel.mu.Lock()
	defer channel.mu.Unlock()
	status := IlinkStatus{
		Name:             channel.Name(),
		Enabled:          channel.Enabled(),
		LoggedIn:         strings.TrimSpace(tokenState.BotToken) != "",
		LoginInProgress:  channel.login.InProgress,
		LoginStatus:      channel.login.Status,
		QRCode:           channel.login.QRCode,
		QRCodeImgContent: channel.login.QRCodeImgContent,
		PollerRunning:    channel.pollerRunning,
		AccountID:        tokenState.AccountID,
		UserID:           firstNonEmptyString(tokenState.UserID),
		BaseURL:          tokenState.BaseURL,
		LastError:        firstNonEmptyString(loadError, channel.login.LastError, channel.lastError),
		UpdatedAt:        maxTime(tokenState.UpdatedAt, channel.login.UpdatedAt),
	}
	return status
}

func (channel *IlinkChannel) run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			channel.setPollerRunning(false)
			return
		}

		tokenState, err := channel.tokens.Load()
		if err != nil {
			channel.setLastError(err.Error())
			channel.setPollerRunning(false)
			if !sleepContext(ctx, ilinkIdleInterval) {
				return
			}
			continue
		}
		if strings.TrimSpace(tokenState.BotToken) == "" {
			channel.setPollerRunning(false)
			if !sleepContext(ctx, ilinkIdleInterval) {
				return
			}
			continue
		}

		channel.setPollerRunning(true)
		pollerState, err := channel.poller.Load()
		if err != nil {
			channel.setLastError(err.Error())
			if !sleepContext(ctx, ilinkIdleInterval) {
				return
			}
			continue
		}

		pollCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		response, err := channel.client.GetUpdates(pollCtx, tokenState.BaseURL, tokenState.BotToken, pollerState.GetUpdatesBuf)
		cancel()
		if err != nil {
			pollerState.LastError = err.Error()
			_ = channel.poller.Save(pollerState)
			channel.setLastError(err.Error())
			if !sleepContext(ctx, ilinkIdleInterval) {
				return
			}
			continue
		}

		nextCursor := strings.TrimSpace(response.GetUpdatesBuf)
		if nextCursor == "" {
			nextCursor = pollerState.GetUpdatesBuf
		}
		processed := true
		for _, message := range response.Msgs {
			update, err := weixin.ParseUpdate(message)
			if err != nil {
				if errors.Is(err, weixin.ErrIgnoreUpdate) {
					continue
				}
				channel.setLastError(err.Error())
				processed = false
				break
			}
			turnCtx, turnCancel := context.WithTimeout(ctx, requestTimeout)
			err = channel.processUpdate(turnCtx, tokenState, update)
			turnCancel()
			if err != nil {
				channel.setLastError(err.Error())
				processed = false
				break
			}
		}
		if !processed {
			if !sleepContext(ctx, ilinkIdleInterval) {
				return
			}
			continue
		}

		pollerState.GetUpdatesBuf = nextCursor
		pollerState.LastError = ""
		if err := channel.poller.Save(pollerState); err != nil {
			channel.setLastError(err.Error())
			if !sleepContext(ctx, ilinkIdleInterval) {
				return
			}
			continue
		}
		channel.setLastError("")
	}
}

func (channel *IlinkChannel) processUpdate(ctx context.Context, tokenState weixin.TokenState, update weixin.Update) error {
	_, err := channel.runner.Process(ctx, ChannelTurnOptions{
		PeerKey:   update.UserID,
		DedupeKey: update.ContextToken,
		Message:   update.Text,
		LoadState: func() (ChannelConversationState, error) {
			state, err := channel.chats.Load(update.UserID)
			if err != nil {
				return ChannelConversationState{}, err
			}
			return ChannelConversationState{
				SessionID:        state.SessionID,
				LastCompletedKey: state.LastCompletedContextToken,
				PendingKey:       state.PendingContextToken,
				PendingReply:     state.PendingReply,
				LastError:        state.LastError,
			}, nil
		},
		SaveState: func(next ChannelConversationState) error {
			state, err := channel.chats.Load(update.UserID)
			if err != nil {
				return err
			}
			state.SessionID = next.SessionID
			state.LastCompletedContextToken = next.LastCompletedKey
			state.PendingContextToken = next.PendingKey
			state.PendingReply = next.PendingReply
			state.LastError = next.LastError
			return channel.chats.Save(state)
		},
		SendReply: func(ctx context.Context, reply string) error {
			return channel.client.SendTextMessage(ctx, tokenState.BaseURL, tokenState.BotToken, update.UserID, update.ClientID, update.ContextToken, reply)
		},
	})
	return err
}

func (channel *IlinkChannel) pollLoginStatus(qrcode string) {
	for {
		channel.mu.Lock()
		active := channel.login.InProgress && channel.login.QRCode == qrcode
		channel.mu.Unlock()
		if !active {
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		response, err := channel.client.PollQRCodeStatus(ctx, qrcode)
		cancel()
		if err != nil {
			channel.mu.Lock()
			channel.login.LastError = err.Error()
			channel.login.UpdatedAt = time.Now().UTC()
			channel.lastError = err.Error()
			channel.mu.Unlock()
			time.Sleep(ilinkIdleInterval)
			continue
		}

		status := strings.TrimSpace(response.Status)
		confirmed := strings.EqualFold(status, "confirmed") || strings.TrimSpace(response.BotToken) != ""
		if confirmed {
			state := weixin.TokenState{
				BotToken:  strings.TrimSpace(response.BotToken),
				BaseURL:   strings.TrimSpace(response.ResolvedBaseURL()),
				AccountID: strings.TrimSpace(response.AccountID),
				UserID:    firstNonEmptyString(response.UserID, response.LoginUserID),
			}
			if state.BotToken == "" {
				channel.mu.Lock()
				channel.login.LastError = "bot token is empty"
				channel.login.UpdatedAt = time.Now().UTC()
				channel.lastError = "bot token is empty"
				channel.mu.Unlock()
				return
			}
			if err := channel.tokens.Save(state); err != nil {
				channel.mu.Lock()
				channel.login.LastError = err.Error()
				channel.login.UpdatedAt = time.Now().UTC()
				channel.lastError = err.Error()
				channel.mu.Unlock()
				return
			}
			channel.mu.Lock()
			channel.login = ilinkLoginState{Status: "confirmed", UpdatedAt: time.Now().UTC()}
			channel.lastError = ""
			channel.mu.Unlock()
			return
		}

		channel.mu.Lock()
		channel.login.Status = firstNonEmptyString(status, "pending")
		channel.login.LastError = ""
		channel.login.UpdatedAt = time.Now().UTC()
		channel.mu.Unlock()
		time.Sleep(ilinkIdleInterval)
	}
}

func (channel *IlinkChannel) setLastError(message string) {
	channel.mu.Lock()
	channel.lastError = strings.TrimSpace(message)
	channel.mu.Unlock()
}

func (channel *IlinkChannel) setPollerRunning(running bool) {
	channel.mu.Lock()
	channel.pollerRunning = running
	channel.mu.Unlock()
}

func sleepContext(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func maxTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}
