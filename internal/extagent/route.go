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
	switch strings.ToLower(strings.TrimPrefix(fields[0], "/")) {
	case string(AgentClaude):
		agent = AgentClaude
	case string(AgentCodex):
		agent = AgentCodex
	case string(AgentCursor):
		agent = AgentCursor
	case string(AgentOpenCode):
		agent = AgentOpenCode
	case string(AgentDefault):
		if len(fields) > 1 {
			return "", "", true, fmt.Errorf("/%s does not accept a message", AgentDefault)
		}
		return AgentDefault, "", true, nil
	default:
		return "", message, false, nil
	}
	if len(fields) == 1 {
		return "", "", true, fmt.Errorf("message is required after /%s", agent)
	}
	return agent, strings.TrimSpace(strings.TrimPrefix(message, fields[0])), true, nil
}
