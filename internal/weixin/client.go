package weixin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	// defaultCDNBaseURL is the WeChat c2c CDN that hosts inbound media. Media is
	// AES-128-ECB encrypted; see FetchImage.
	defaultCDNBaseURL = "https://novac2c.cdn.weixin.qq.com/c2c"
)

type Client struct {
	httpClient     *http.Client
	baseURL        string
	channelVersion string
	cdnBaseURL     string
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
	ImageItem *ImageItem `json:"image_item,omitempty"`
	// Raw keeps the original item object so the image parser can recover image
	// references even when the iLink wire format uses field names we have not
	// modeled yet. It is populated on unmarshal only and omitted on marshal.
	Raw map[string]json.RawMessage `json:"-"`
}

// UnmarshalJSON decodes the known item shape and additionally captures the raw
// object into Raw for tolerant image extraction / debugging.
func (item *MessageItem) UnmarshalJSON(data []byte) error {
	type messageItemAlias MessageItem
	var alias messageItemAlias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	*item = MessageItem(alias)
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err == nil {
		item.Raw = raw
	}
	return nil
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

// CDNMedia is a reference to AES-128-ECB encrypted media on the WeChat c2c CDN.
// aes_key is base64-encoded in JSON (either base64 of the raw 16 bytes, or base64
// of a 32-char hex string of the key).
type CDNMedia struct {
	EncryptQueryParam string `json:"encrypt_query_param,omitempty"`
	AESKey            string `json:"aes_key,omitempty"`
	EncryptType       int    `json:"encrypt_type,omitempty"`
}

// ImageItem is an inbound image (item type 2). media points at the full image on
// the CDN; aeskey (a hex string of the 16-byte key) is preferred over
// media.aes_key for decryption when present.
type ImageItem struct {
	Media      *CDNMedia `json:"media,omitempty"`
	ThumbMedia *CDNMedia `json:"thumb_media,omitempty"`
	AESKey     string    `json:"aeskey,omitempty"`
	URL        string    `json:"url,omitempty"`
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
		cdnBaseURL:     defaultCDNBaseURL,
	}
}

// SetCDNBaseURL overrides the CDN base used for inbound media downloads. An empty
// value keeps the current/default base.
func (client *Client) SetCDNBaseURL(cdnBaseURL string) {
	if trimmed := strings.TrimRight(strings.TrimSpace(cdnBaseURL), "/"); trimmed != "" {
		client.cdnBaseURL = trimmed
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

// maxInboundImageBytes caps a single downloaded/decrypted image to keep memory
// and the model context bounded.
const maxInboundImageBytes = 12 << 20 // 12 MiB

// FetchImage downloads an inbound image from the CDN and AES-128-ECB decrypts it
// when a key is present (the c2c CDN stores media encrypted). It returns the
// plaintext image bytes plus a best-effort MIME type sniffed from the content.
func (client *Client) FetchImage(ctx context.Context, image InboundImage) ([]byte, string, error) {
	queryParam := strings.TrimSpace(image.EncryptQueryParam)
	if queryParam == "" {
		return nil, "", fmt.Errorf("image has no encrypt_query_param")
	}
	cdnBaseURL := strings.TrimRight(strings.TrimSpace(client.cdnBaseURL), "/")
	if cdnBaseURL == "" {
		cdnBaseURL = defaultCDNBaseURL
	}
	downloadURL := cdnBaseURL + "/download?encrypted_query_param=" + url.QueryEscape(queryParam)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, "", err
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, "", fmt.Errorf("download image failed: %s", response.Status)
	}
	encrypted, err := io.ReadAll(io.LimitReader(response.Body, maxInboundImageBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(encrypted) > maxInboundImageBytes {
		return nil, "", fmt.Errorf("image exceeds %d bytes", maxInboundImageBytes)
	}

	data := encrypted
	if key, ok, keyErr := image.aesKey(); keyErr != nil {
		return nil, "", keyErr
	} else if ok {
		decrypted, decErr := decryptAESECB(encrypted, key)
		if decErr != nil {
			return nil, "", fmt.Errorf("decrypt image: %w", decErr)
		}
		data = decrypted
	}
	return data, sniffImageMIME(data, image.FileName), nil
}

func sniffImageMIME(data []byte, fileName string) string {
	if detected := http.DetectContentType(data); strings.HasPrefix(detected, "image/") {
		return strings.SplitN(detected, ";", 2)[0]
	}
	name := strings.ToLower(strings.TrimSpace(fileName))
	switch {
	case strings.HasSuffix(name, ".png"):
		return "image/png"
	case strings.HasSuffix(name, ".gif"):
		return "image/gif"
	case strings.HasSuffix(name, ".webp"):
		return "image/webp"
	case strings.HasSuffix(name, ".bmp"):
		return "image/bmp"
	}
	return "image/jpeg"
}

func (response QRCodeStatusResponse) ResolvedBaseURL() string {
	return firstNonEmpty(response.BaseURL, response.AltBaseURL)
}

func responseError(response responseEnvelope, fallback string) error {
	message := strings.TrimSpace(firstNonEmpty(response.ErrMsg, response.Msg))
	if message == "" {
		message = fallback
	}
	return errors.New(message)
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
