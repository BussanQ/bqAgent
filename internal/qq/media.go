package qq

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// maxInboundImageBytes caps a single downloaded image to keep memory and the
// model context bounded.
const maxInboundImageBytes = 12 << 20 // 12 MiB

// FetchImage downloads a QQ image attachment and returns the raw bytes plus a
// best-effort MIME type. QQ rich-media URLs are plain (unencrypted) and need no
// auth, so this is a direct GET.
func (client *Client) FetchImage(ctx context.Context, image InboundImage) ([]byte, string, error) {
	target := strings.TrimSpace(image.URL)
	if target == "" {
		return nil, "", fmt.Errorf("image has no url")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
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
	data, err := io.ReadAll(io.LimitReader(response.Body, maxInboundImageBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxInboundImageBytes {
		return nil, "", fmt.Errorf("image exceeds %d bytes", maxInboundImageBytes)
	}
	return data, resolveImageMIME(data, image, response.Header.Get("Content-Type")), nil
}

func resolveImageMIME(data []byte, image InboundImage, responseContentType string) string {
	if detected := http.DetectContentType(data); strings.HasPrefix(detected, "image/") {
		return strings.SplitN(detected, ";", 2)[0]
	}
	if ct := strings.TrimSpace(responseContentType); strings.HasPrefix(ct, "image/") {
		return strings.SplitN(ct, ";", 2)[0]
	}
	if ct := strings.TrimSpace(image.ContentType); strings.HasPrefix(ct, "image/") {
		return ct
	}
	name := strings.ToLower(strings.TrimSpace(image.FileName))
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
