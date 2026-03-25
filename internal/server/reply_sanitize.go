package server

import (
	"regexp"
	"strings"
)

var thinkBlockPattern = regexp.MustCompile(`(?is)<think\b[^>]*>.*?</think>`)

func sanitizeChannelReply(reply string) string {
	cleaned := thinkBlockPattern.ReplaceAllString(reply, "")
	cleaned = strings.ReplaceAll(cleaned, "<think>", "")
	cleaned = strings.ReplaceAll(cleaned, "</think>", "")
	return strings.TrimSpace(cleaned)
}
