package weixin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultIlinkBaseURL      = "https://ilinkai.weixin.qq.com"
	defaultChannelVersion    = "1.0.2"
	loginBotType             = "3"
	inboundMessageType       = 1
	outboundMessageType      = 2
	outboundMessageStateDone = 2
)

type Client struct {
	httpClient     *http.Client
	baseURL        string
	channelVersion string
}

type BaseInfo struct {
	ChannelVersion string `json:"channel_version"`
}

type responseEnvelope struct {
	Ret    int    `json:"ret"`
	Msg    string `json:"msg,omitempty"`
	ErrMsg string `json:"errmsg,omitempty"`
}

type QRCodeResponse struct {
	responseEnvelope
	QRCode          string `json:"qrcode,omitempty"`
	QRCodeImgBase64 string `json:"qrcode_img_content,omitempty"`
}

type QRCodeStatusResponse struct {
	responseEnvelope
	Status      string `json:"status,omitempty"`
	BotToken    string `json:"bot_token,omitempty"`
	BaseURL     string `json:"baseurl,omitempty"`
	AltBaseURL  string `json:"base_url,omitempty"`
	AccountID   string `json:"account_id,omitempty"`
	UserID      string `json:"user_id,omitempty"`
	LoginUserID string `json:"login_user_id,omitempty"`
}

type GetUpdatesRequest struct {
	GetUpdatesBuf string   `json:"get_updates_buf,omitempty"`
	BaseInfo      BaseInfo `json:"base_info"`
}

type GetUpdatesResponse struct {
	responseEnvelope
	Msgs                 []InboundMessage `json:"msgs,omitempty"`
	GetUpdatesBuf        string           `json:"get_updates_buf,omitempty"`
	LongPollingTimeoutMS int              `json:"longpolling_timeout_ms,omitempty"`
}

type InboundMessage struct {
	FromUserID   string        `json:"from_user_id,omitempty"`
	ToUserID     string        `json:"to_user_id,omitempty"`
	ClientID     string        `json:"client_id,omitempty"`
	MessageType  int           `json:"message_type,omitempty"`
	MessageState int           `json:"message_state,omitempty"`
	ContextToken string        `json:"context_token,omitempty"`
	ItemList     []MessageItem `json:"item_list,omitempty"`
}

type MessageItem struct {
	Type      int        `json:"type,omitempty"`
	TextItem  *TextItem  `json:"text_item,omitempty"`
	VoiceItem *VoiceItem `json:"voice_item,omitempty"`
	FileItem  *FileItem  `json:"file_item,omitempty"`
}

type TextItem struct {
	Text string `json:"text,omitempty"`
}

type VoiceItem struct {
	Text string `json:"text,omitempty"`
}

type FileItem struct {
	FileName string `json:"file_name,omitempty"`
}

type SendMessageRequest struct {
	Msg      OutboundMessage `json:"msg"`
	BaseInfo BaseInfo        `json:"base_info"`
}

type OutboundMessage struct {
	FromUserID   string        `json:"from_user_id,omitempty"`
	ToUserID     string        `json:"to_user_id"`
	ClientID     string        `json:"client_id,omitempty"`
	MessageType  int           `json:"message_type"`
	MessageState int           `json:"message_state"`
	ContextToken string        `json:"context_token,omitempty"`
	ItemList     []MessageItem `json:"item_list"`
}

func NewClient(httpClient *http.Client) *Client {
	return NewClientWithBaseURL(defaultIlinkBaseURL, defaultChannelVersion, httpClient)
}

func NewClientWithBaseURL(baseURL, channelVersion string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 45 * time.Second}
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultIlinkBaseURL
	}
	if strings.TrimSpace(channelVersion) == "" {
		channelVersion = defaultChannelVersion
	}
	return &Client{
		httpClient:     httpClient,
		baseURL:        strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		channelVersion: strings.TrimSpace(channelVersion),
	}
}

func (client *Client) GetBotQRCode(ctx context.Context) (QRCodeResponse, error) {
	var response QRCodeResponse
	query := url.Values{}
	query.Set("bot_type", loginBotType)
	if err := client.doJSON(ctx, http.MethodGet, "", "/ilink/bot/get_bot_qrcode", query, "", nil, &response); err != nil {
		return QRCodeResponse{}, err
	}
	if response.Ret != 0 {
		return QRCodeResponse{}, responseError(response.responseEnvelope, "get bot qrcode failed")
	}
	return response, nil
}

func (client *Client) PollQRCodeStatus(ctx context.Context, qrcode string) (QRCodeStatusResponse, error) {
	qrcode = strings.TrimSpace(qrcode)
	if qrcode == "" {
		return QRCodeStatusResponse{}, fmt.Errorf("qrcode is required")
	}
	var response QRCodeStatusResponse
	query := url.Values{}
	query.Set("qrcode", qrcode)
	if err := client.doJSON(ctx, http.MethodGet, "", "/ilink/bot/get_qrcode_status", query, "", nil, &response); err != nil {
		return QRCodeStatusResponse{}, err
	}
	if response.Ret != 0 {
		return QRCodeStatusResponse{}, responseError(response.responseEnvelope, "get qrcode status failed")
	}
	return response, nil
}

func (client *Client) GetUpdates(ctx context.Context, baseURL, token, cursor string) (GetUpdatesResponse, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return GetUpdatesResponse{}, fmt.Errorf("bot token is required")
	}
	var response GetUpdatesResponse
	request := GetUpdatesRequest{
		GetUpdatesBuf: strings.TrimSpace(cursor),
		BaseInfo:      BaseInfo{ChannelVersion: client.channelVersion},
	}
	if err := client.doJSON(ctx, http.MethodPost, baseURL, "/ilink/bot/getupdates", nil, token, request, &response); err != nil {
		return GetUpdatesResponse{}, err
	}
	if response.Ret != 0 {
		return GetUpdatesResponse{}, responseError(response.responseEnvelope, "get updates failed")
	}
	return response, nil
}

func (client *Client) SendTextMessage(ctx context.Context, baseURL, token, toUserID, clientID, contextToken, text string) error {
	return client.SendMessage(ctx, baseURL, token, NewTextMessage(toUserID, clientID, contextToken, text))
}

func (client *Client) SendMessage(ctx context.Context, baseURL, token string, message OutboundMessage) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("bot token is required")
	}
	if strings.TrimSpace(message.ToUserID) == "" {
		return fmt.Errorf("to_user_id is required")
	}
	if len(message.ItemList) == 0 {
		return fmt.Errorf("item_list is required")
	}
	var response responseEnvelope
	request := SendMessageRequest{
		Msg:      message,
		BaseInfo: BaseInfo{ChannelVersion: client.channelVersion},
	}
	if err := client.doJSON(ctx, http.MethodPost, baseURL, "/ilink/bot/sendmessage", nil, token, request, &response); err != nil {
		return err
	}
	if response.Ret != 0 {
		return responseError(response, "send message failed")
	}
	return nil
}

func (response QRCodeStatusResponse) ResolvedBaseURL() string {
	return firstNonEmpty(response.BaseURL, response.AltBaseURL)
}

func responseError(response responseEnvelope, fallback string) error {
	message := strings.TrimSpace(firstNonEmpty(response.ErrMsg, response.Msg))
	if message == "" {
		message = fallback
	}
	return fmt.Errorf(message)
}

func (client *Client) doJSON(ctx context.Context, method, baseURL, path string, query url.Values, token string, body any, out any) error {
	var requestBody io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		requestBody = bytes.NewReader(payload)
	}
	url := client.resolvedBaseURL(baseURL) + path
	if len(query) > 0 {
		url += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, method, url, requestBody)
	if err != nil {
		return err
	}
	for key, value := range client.buildHeaders(strings.TrimSpace(token)) {
		request.Header.Set(key, value)
	}

	response, err := client.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	payload, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("ilink request failed: %s", strings.TrimSpace(string(payload)))
	}
	if out == nil || len(payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return err
	}
	return nil
}

func (client *Client) buildHeaders(token string) map[string]string {
	headers := map[string]string{
		"Content-Type":      "application/json",
		"AuthorizationType": "ilink_bot_token",
		"X-WECHAT-UIN":      randomWeChatUIN(),
	}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	return headers
}

func (client *Client) resolvedBaseURL(baseURL string) string {
	return strings.TrimRight(firstNonEmpty(baseURL, client.baseURL), "/")
}

func randomWeChatUIN() string {
	max := new(big.Int).SetUint64(1 << 32)
	value, err := rand.Int(rand.Reader, max)
	if err != nil {
		return base64.StdEncoding.EncodeToString([]byte("0"))
	}
	return base64.StdEncoding.EncodeToString([]byte(value.String()))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
