package serverchan

import (
	"fmt"
	"strings"
)

type ChatRequest struct {
	SendKey   string
	SessionID string
	Message   string
	Title     string
}

func ParseChatRequest(values map[string]string) (ChatRequest, error) {
	sendKey := firstNonEmpty(values["sendkey"], values["key"])
	sessionID := firstNonEmpty(values["session_id"], values["session"])
	title := firstNonEmpty(values["title"], values["text"])
	message := strings.TrimSpace(values["message"])
	if message == "" {
		message = strings.TrimSpace(composeMessage(title, values["desp"]))
	}

	request := ChatRequest{
		SendKey:   strings.TrimSpace(sendKey),
		SessionID: strings.TrimSpace(sessionID),
		Message:   message,
		Title:     strings.TrimSpace(title),
	}
	if request.SendKey == "" {
		return ChatRequest{}, fmt.Errorf("sendkey is required")
	}
	if request.Message == "" {
		return ChatRequest{}, fmt.Errorf("message is required")
	}
	return request, nil
}

func BuildReply(title, reply string) (string, string) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "bqagent 回复"
	} else {
		title = "Re: " + compactLine(title)
	}
	return title, strings.TrimSpace(reply)
}

func composeMessage(text, desp string) string {
	text = strings.TrimSpace(text)
	desp = strings.TrimSpace(desp)
	switch {
	case text != "" && desp != "":
		return text + "\n\n" + desp
	case desp != "":
		return desp
	default:
		return text
	}
}

func compactLine(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	const maxRunes = 64
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "…"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
