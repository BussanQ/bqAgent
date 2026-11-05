package agent

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	// maxProviderErrorBodyBytes bounds how much of a non-2xx response body is
	// kept for the error message; providers and proxies sometimes return whole
	// HTML pages or stack traces here.
	maxProviderErrorBodyBytes = 64 << 10
	// maxProviderSmallResponseBytes bounds auxiliary JSON responses such as
	// input token counts.
	maxProviderSmallResponseBytes = 1 << 20
)

// Variables rather than constants so tests can exercise the limits without
// generating tens of megabytes of payload.
var (
	// maxProviderResponseBytes bounds a non-streaming JSON completion response.
	maxProviderResponseBytes int64 = 32 << 20
	// maxProviderStreamBytes bounds the total raw bytes of one streaming
	// response. Content, reasoning and tool-call argument builders are all
	// derived from this stream, so the raw limit also bounds their growth.
	maxProviderStreamBytes int64 = 64 << 20
)

// errProviderResponseTooLarge marks a provider response that exceeded its byte
// budget. It is a protocol failure, not a transient network error, so retrying
// the same request would only download the oversized payload again.
var errProviderResponseTooLarge = errors.New("provider response exceeds size limit")

// limitedResponseBody returns errProviderResponseTooLarge once more than limit
// bytes have been read, instead of the silent EOF that io.LimitReader produces
// (which would surface as a confusing truncated-JSON or missing-[DONE] error).
type limitedResponseBody struct {
	inner     io.ReadCloser
	remaining int64
	limit     int64
	exceeded  bool
}

func newLimitedResponseBody(inner io.ReadCloser, limit int64) *limitedResponseBody {
	return &limitedResponseBody{inner: inner, remaining: limit, limit: limit}
}

func (body *limitedResponseBody) Read(buffer []byte) (int, error) {
	if body.remaining <= 0 {
		// Probe one byte to distinguish "exactly at the limit" from "over it".
		var probe [1]byte
		n, err := body.inner.Read(probe[:])
		if n > 0 {
			body.exceeded = true
			return 0, fmt.Errorf("%w (%d bytes)", errProviderResponseTooLarge, body.limit)
		}
		return 0, err
	}
	if int64(len(buffer)) > body.remaining {
		buffer = buffer[:body.remaining]
	}
	n, err := body.inner.Read(buffer)
	body.remaining -= int64(n)
	return n, err
}

func (body *limitedResponseBody) Close() error {
	return body.inner.Close()
}

// providerBodyExceeded reports whether body hit its size limit. Stream loops
// check it before parsing each line because bufio.Scanner still yields the
// partially read final line after a read error, and parsing that fragment
// would mask the size-limit error with a misleading JSON error.
func providerBodyExceeded(body io.Reader) bool {
	limited, ok := body.(*limitedResponseBody)
	return ok && limited.exceeded
}

// limitProviderReader wraps a response body for a bounded JSON decode.
func limitProviderReader(body io.ReadCloser, limit int64) io.Reader {
	return newLimitedResponseBody(body, limit)
}

// readProviderErrorBody reads at most maxProviderErrorBodyBytes of an error
// response. Read failures fall back to the HTTP status text.
func readProviderErrorBody(body io.Reader, status string) string {
	payload, err := io.ReadAll(io.LimitReader(body, maxProviderErrorBodyBytes+1))
	if err != nil && len(payload) == 0 {
		return status
	}
	truncated := len(payload) > maxProviderErrorBodyBytes
	if truncated {
		payload = payload[:maxProviderErrorBodyBytes]
	}
	text := strings.TrimSpace(strings.ToValidUTF8(string(payload), ""))
	if text == "" {
		return status
	}
	if truncated {
		text += " …[truncated]"
	}
	return text
}
