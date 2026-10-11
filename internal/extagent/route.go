package extagent

import (
	"fmt"
	"strings"
)

func ParseRoute(message string) (agent AgentName, prompt string, explicit bool, err error) {
	message = strings.TrimSpace(message)
	if message == "" || !strings.HasPrefix(message, "/") {
		return "", message, false, nil
	}
	fields := strings.Fields(message)
	if len(fields) == 0 {
		return "", "", false, nil
	}
	name := AgentName(strings.ToLower(strings.TrimPrefix(fields[0], "/")))
	if name == AgentDefault {
		if len(fields) > 1 {
			return "", "", true, fmt.Errorf("/%s does not accept a message", AgentDefault)
		}
		return AgentDefault, "", true, nil
	}
	for _, supported := range SupportedAgents() {
		if name == supported {
			agent = name
			break
		}
	}
	if agent == "" {
		return "", message, false, nil
	}
	if len(fields) == 1 {
		return "", "", true, fmt.Errorf("message is required after /%s", agent)
	}
	return agent, strings.TrimSpace(strings.TrimPrefix(message, fields[0])), true, nil
}

// IsRouteCommand also recognizes commands with missing or invalid arguments so
// channel-specific guards use the same registry as routing and detection.
func IsRouteCommand(message string) bool {
	_, _, explicit, _ := ParseRoute(message)
	return explicit
}
