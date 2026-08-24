package agent

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	DefaultStreamIdleTimeout = 2 * time.Minute
	providerRetryBaseDelay   = 250 * time.Millisecond
	providerRetryJitter      = 250 * time.Millisecond
	providerRetryAfterCap    = 10 * time.Second
)

type ProviderErrorCategory string

const (
	ProviderErrorRateLimit ProviderErrorCategory = "model_rate_limit"
	ProviderErrorServer    ProviderErrorCategory = "model_server"
	ProviderErrorNetwork   ProviderErrorCategory = "model_network"
	ProviderErrorTimeout   ProviderErrorCategory = "model_timeout"
	ProviderErrorProtocol  ProviderErrorCategory = "model_protocol"
	ProviderErrorRequest   ProviderErrorCategory = "model_request"
)

type ProviderRecovery struct {
	Kind       string
	Attempt    int
	Category   ProviderErrorCategory
	StatusCode int
	Delay      time.Duration
}

type ProviderRequestMetadata struct {
	RetryCount               int
	ReasoningDowngraded      bool
	ReasoningDowngradeSource string
	Recoveries               []ProviderRecovery
}

type ProviderError struct {
	Category      ProviderErrorCategory
	StatusCode    int
	Message       string
	RetryAfter    time.Duration
	HasRetryAfter bool
	Transient     bool
	Cause         error
	Request       ProviderRequestMetadata
}

func (err *ProviderError) Error() string {
	if err == nil {
		return "provider request failed"
	}
	message := strings.TrimSpace(err.Message)
	if err.Cause != nil {
		if message == "" {
			message = err.Cause.Error()
		} else if !strings.Contains(message, err.Cause.Error()) {
			message += ": " + err.Cause.Error()
		}
	}
	if err.StatusCode > 0 {
		if message == "" {
			return fmt.Sprintf("provider request failed: HTTP %d", err.StatusCode)
		}
		return fmt.Sprintf("provider request failed: HTTP %d: %s", err.StatusCode, message)
	}
	if message != "" {
		return "provider request failed: " + message
	}
	return "provider request failed"
}

func (err *ProviderError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Cause
}

func (err *ProviderError) ErrorCategory() string {
	if err == nil {
		return ""
	}
	return string(err.Category)
}

func ProviderRequestMetadataFromError(err error) ProviderRequestMetadata {
	type metadataError interface {
		ProviderRequestMetadata() ProviderRequestMetadata
	}
	var metadataCarrier metadataError
	if errors.As(err, &metadataCarrier) {
		return metadataCarrier.ProviderRequestMetadata()
	}
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		return providerErr.Request
	}
	return ProviderRequestMetadata{}
}

type providerRequestError struct {
	cause   error
	request ProviderRequestMetadata
}

func (err *providerRequestError) Error() string { return err.cause.Error() }
func (err *providerRequestError) Unwrap() error { return err.cause }
func (err *providerRequestError) ProviderRequestMetadata() ProviderRequestMetadata {
	return cloneProviderRequestMetadata(err.request)
}

type ClientOptions struct {
	StreamIdleTimeout time.Duration
}

type streamAttemptState struct {
	semanticOutput bool
}

func (state *streamAttemptState) markSemantic() {
	if state != nil {
		state.semanticOutput = true
	}
}

type providerAttempt func(ChatCompletionOptions, *streamAttemptState) (AssistantMessage, error)

var reasoningUnsupportedModels sync.Map

func (c *Client) executeWithResilience(ctx context.Context, model string, options ChatCompletionOptions, stream bool, attempt providerAttempt) (AssistantMessage, error) {
	metadata := ProviderRequestMetadata{}
	effectiveOptions := options
	cacheKey := c.reasoningCacheKey(model)
	if options.ReasoningEffort != ReasoningEffortAuto {
		if _, disabled := reasoningUnsupportedModels.Load(cacheKey); disabled {
			effectiveOptions.ReasoningEffort = ReasoningEffortAuto
			metadata.ReasoningDowngraded = true
			metadata.ReasoningDowngradeSource = "capability_cache"
		}
	}

	transientRetryUsed := false
	downgradeAttempt := false
	attemptNumber := 0
	for {
		attemptNumber++
		state := &streamAttemptState{}
		message, err := attempt(effectiveOptions, state)
		if err == nil {
			if downgradeAttempt {
				reasoningUnsupportedModels.Store(cacheKey, struct{}{})
			}
			message.Request = cloneProviderRequestMetadata(metadata)
			return message, nil
		}

		if ctxErr := ctx.Err(); ctxErr != nil {
			return AssistantMessage{}, &providerRequestError{cause: ctxErr, request: metadata}
		}
		providerErr := normalizeProviderError(ctx, err)
		if stream && state.semanticOutput {
			providerErr.Request = cloneProviderRequestMetadata(metadata)
			return AssistantMessage{}, providerErr
		}

		if !metadata.ReasoningDowngraded && effectiveOptions.ReasoningEffort != ReasoningEffortAuto && reasoningUnsupportedError(c.apiType, providerErr) {
			effectiveOptions.ReasoningEffort = ReasoningEffortAuto
			metadata.ReasoningDowngraded = true
			metadata.ReasoningDowngradeSource = "provider_rejection"
			metadata.Recoveries = append(metadata.Recoveries, ProviderRecovery{
				Kind: "reasoning_downgrade", Attempt: attemptNumber + 1,
				Category: providerErr.Category, StatusCode: providerErr.StatusCode,
			})
			downgradeAttempt = true
			continue
		}

		if !transientRetryUsed && providerErr.Transient {
			delay := c.providerRetryDelay(providerErr)
			if waitErr := c.waitForProviderRetry(ctx, delay); waitErr != nil {
				return AssistantMessage{}, &providerRequestError{cause: waitErr, request: metadata}
			}
			metadata.RetryCount++
			metadata.Recoveries = append(metadata.Recoveries, ProviderRecovery{
				Kind: "provider_retry", Attempt: attemptNumber + 1,
				Category: providerErr.Category, StatusCode: providerErr.StatusCode, Delay: delay,
			})
			transientRetryUsed = true
			continue
		}

		providerErr.Request = cloneProviderRequestMetadata(metadata)
		return AssistantMessage{}, providerErr
	}
}

func cloneProviderRequestMetadata(metadata ProviderRequestMetadata) ProviderRequestMetadata {
	metadata.Recoveries = append([]ProviderRecovery(nil), metadata.Recoveries...)
	return metadata
}

func (c *Client) reasoningCacheKey(model string) string {
	return strings.Join([]string{string(c.apiType), strings.ToLower(strings.TrimRight(strings.TrimSpace(c.baseURL), "/")), strings.TrimSpace(model)}, "\x00")
}

func reasoningUnsupportedError(apiType APIType, err *ProviderError) bool {
	if err == nil || (err.StatusCode != http.StatusBadRequest && err.StatusCode != http.StatusUnprocessableEntity) {
		return false
	}
	text := strings.ToLower(err.Message)
	unsupported := strings.Contains(text, "unsupported") || strings.Contains(text, "not support") ||
		strings.Contains(text, "unknown") || strings.Contains(text, "unrecognized") ||
		strings.Contains(text, "not allowed") || strings.Contains(text, "not permitted")
	if !unsupported {
		return false
	}
	switch apiType {
	case APITypeOpenAIResponse:
		return strings.Contains(text, "reasoning.effort") || (strings.Contains(text, "reasoning") && strings.Contains(text, "effort"))
	case APITypeAnthropic:
		return strings.Contains(text, "thinking") || strings.Contains(text, "output_config") || strings.Contains(text, "output config")
	default:
		return strings.Contains(text, "reasoning_effort") || strings.Contains(text, "reasoning effort")
	}
}

func (c *Client) providerRetryDelay(err *ProviderError) time.Duration {
	if err != nil && err.HasRetryAfter && (err.StatusCode == http.StatusTooManyRequests || err.StatusCode == http.StatusServiceUnavailable) {
		if err.RetryAfter > providerRetryAfterCap {
			return providerRetryAfterCap
		}
		if err.RetryAfter >= 0 {
			return err.RetryAfter
		}
	}
	jitter := time.Duration(0)
	if c.retryJitter != nil {
		jitter = c.retryJitter(providerRetryJitter)
	}
	return providerRetryBaseDelay + jitter
}

func defaultProviderRetryJitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(max)+1))
	if err != nil {
		return 0
	}
	return time.Duration(value.Int64())
}

func (c *Client) waitForProviderRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	if c.retrySleep != nil {
		return c.retrySleep(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func parseRetryAfter(header string, now time.Time) (time.Duration, bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseInt(header, 10, 64); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second, true
	}
	when, err := http.ParseTime(header)
	if err != nil || !when.After(now) {
		return 0, false
	}
	return when.Sub(now), true
}

func providerHTTPError(response *http.Response, message string) *ProviderError {
	statusCode := 0
	status := ""
	if response != nil {
		statusCode = response.StatusCode
		status = response.Status
	}
	message = providerErrorMessage(message)
	if message == "" {
		message = status
	}
	err := &ProviderError{StatusCode: statusCode, Message: message, Category: ProviderErrorRequest}
	switch {
	case statusCode == http.StatusTooManyRequests:
		err.Category = ProviderErrorRateLimit
		err.Transient = true
	case statusCode >= 500 && statusCode <= 599:
		err.Category = ProviderErrorServer
		err.Transient = true
	}
	if response != nil && (statusCode == http.StatusTooManyRequests || statusCode == http.StatusServiceUnavailable) {
		now := time.Now()
		if retryAfter, ok := parseRetryAfter(response.Header.Get("Retry-After"), now); ok {
			err.RetryAfter = retryAfter
			err.HasRetryAfter = true
		}
	}
	return err
}

func providerErrorMessage(payload string) string {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return ""
	}
	var envelope struct {
		Message string          `json:"message"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal([]byte(payload), &envelope) != nil {
		return payload
	}
	if strings.TrimSpace(envelope.Message) != "" {
		return strings.TrimSpace(envelope.Message)
	}
	if len(envelope.Error) > 0 {
		var nested struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(envelope.Error, &nested) == nil && strings.TrimSpace(nested.Message) != "" {
			return strings.TrimSpace(nested.Message)
		}
		var text string
		if json.Unmarshal(envelope.Error, &text) == nil && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return payload
}

func providerProtocolError(message string, cause error) *ProviderError {
	return &ProviderError{Category: ProviderErrorProtocol, Message: message, Cause: cause}
}

func providerStreamEventError(message, code string) *ProviderError {
	text := strings.ToLower(strings.TrimSpace(code + " " + message))
	err := &ProviderError{Category: ProviderErrorProtocol, Message: strings.TrimSpace(message)}
	switch {
	case strings.Contains(text, "rate_limit") || strings.Contains(text, "rate limit") || strings.Contains(text, "too many requests"):
		err.Category = ProviderErrorRateLimit
		err.Transient = true
	case strings.Contains(text, "overloaded") || strings.Contains(text, "server_error") || strings.Contains(text, "server error") || strings.Contains(text, "internal_error"):
		err.Category = ProviderErrorServer
		err.Transient = true
	}
	return err
}

func normalizeProviderError(ctx context.Context, err error) *ProviderError {
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		return providerErr
	}
	if ctx != nil && ctx.Err() != nil {
		return &ProviderError{Category: ProviderErrorTimeout, Message: ctx.Err().Error(), Cause: ctx.Err()}
	}
	var incomplete *IncompleteStreamError
	if errors.As(err, &incomplete) {
		return providerProtocolError(incomplete.Error(), incomplete)
	}
	return providerTransportError(ctx, err)
}

func providerTransportError(ctx context.Context, err error) *ProviderError {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return &ProviderError{Category: ProviderErrorTimeout, Message: ctx.Err().Error(), Cause: ctx.Err()}
	}
	var idle *streamIdleTimeoutError
	if errors.As(err, &idle) {
		return &ProviderError{Category: ProviderErrorTimeout, Message: idle.Error(), Cause: idle, Transient: true}
	}
	category := ProviderErrorNetwork
	transient := isTransientNetworkError(err)
	if errors.Is(err, context.DeadlineExceeded) {
		category = ProviderErrorTimeout
		transient = true
	}
	return &ProviderError{Category: category, Message: err.Error(), Cause: err, Transient: transient}
}

func isTransientNetworkError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ETIMEDOUT) {
		return true
	}
	var certificateInvalid x509.CertificateInvalidError
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameError x509.HostnameError
	var recordHeaderError tls.RecordHeaderError
	if errors.As(err, &certificateInvalid) || errors.As(err, &unknownAuthority) || errors.As(err, &hostnameError) || errors.As(err, &recordHeaderError) {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsErr.IsTimeout || dnsErr.IsTemporary
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout() || netErr.Temporary()
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return isTransientNetworkError(urlErr.Err)
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "connection reset") || strings.Contains(text, "connection refused") ||
		strings.Contains(text, "broken pipe") || strings.Contains(text, "unexpected eof") ||
		strings.Contains(text, "temporarily unavailable")
}

type streamIdleTimeoutError struct {
	Timeout time.Duration
}

func (err *streamIdleTimeoutError) Error() string {
	return fmt.Sprintf("SSE stream idle for %s", err.Timeout)
}

type streamWatchdog struct {
	timeout  time.Duration
	cancel   context.CancelCauseFunc
	activity chan struct{}
	stopCh   chan struct{}
	stopOnce sync.Once
	timedOut atomic.Bool
}

func newStreamWatchdog(parent context.Context, timeout time.Duration) (context.Context, *streamWatchdog) {
	ctx, cancel := context.WithCancelCause(parent)
	watchdog := &streamWatchdog{
		timeout: timeout, cancel: cancel,
		activity: make(chan struct{}, 1), stopCh: make(chan struct{}),
	}
	go watchdog.run()
	return ctx, watchdog
}

func (watchdog *streamWatchdog) run() {
	timer := time.NewTimer(watchdog.timeout)
	defer timer.Stop()
	for {
		select {
		case <-watchdog.stopCh:
			return
		case <-watchdog.activity:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(watchdog.timeout)
		case <-timer.C:
			watchdog.timedOut.Store(true)
			watchdog.cancel(&streamIdleTimeoutError{Timeout: watchdog.timeout})
			return
		}
	}
}

func (watchdog *streamWatchdog) touch() {
	if watchdog == nil {
		return
	}
	select {
	case watchdog.activity <- struct{}{}:
	default:
	}
}

func (watchdog *streamWatchdog) stop() {
	if watchdog == nil {
		return
	}
	watchdog.stopOnce.Do(func() {
		close(watchdog.stopCh)
		watchdog.cancel(context.Canceled)
	})
}

type watchdogResponseBody struct {
	inner    io.ReadCloser
	watchdog *streamWatchdog
}

func (body *watchdogResponseBody) Read(buffer []byte) (int, error) {
	n, err := body.inner.Read(buffer)
	if n > 0 {
		body.watchdog.touch()
	}
	if err != nil && body.watchdog.timedOut.Load() {
		return n, &streamIdleTimeoutError{Timeout: body.watchdog.timeout}
	}
	return n, err
}

func (body *watchdogResponseBody) Close() error {
	err := body.inner.Close()
	body.watchdog.stop()
	return err
}
