package mcp

import (
	"context"
	"errors"
	"net"
	"time"
)

// ServerStatus contains only safe diagnostic fields, never URLs, headers or raw errors.
type ServerStatus struct {
	Name      string
	State     string
	Reason    string
	Tools     int
	CheckedAt time.Time
}

func FailureReason(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	var network net.Error
	if errors.As(err, &network) {
		return "connection_failed"
	}
	return "protocol_or_auth_failed"
}

func (c Config) Status(getenv func(string) string) []ServerStatus {
	_, invalid := c.enabledServers(getenv)
	result := make([]ServerStatus, 0, len(c.Servers))
	for _, name := range sortedMapKeys(c.Servers) {
		state, reason := "unverified", "not_probed"
		if c.Servers[name].Disabled {
			state, reason = "disabled", "disabled"
		} else if invalid[name] != nil {
			state, reason = "error", "invalid_configuration"
		}
		result = append(result, ServerStatus{Name: name, State: state, Reason: reason, CheckedAt: time.Now().UTC()})
	}
	return result
}
