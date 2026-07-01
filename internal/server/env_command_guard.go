package server

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

const defaultEnvironmentCommandTimeout = 90 * time.Second

var whitespacePattern = regexp.MustCompile(`\s+`)

type environmentCommandGuard struct {
	mu       sync.Mutex
	failures map[string]environmentCommandFailure
	timeout  time.Duration
}

type environmentCommandFailure struct {
	Command   string
	Family    string
	Reason    string
	Output    string
	FailedAt  time.Time
	FailCount int
}

func newEnvironmentCommandGuard(timeout time.Duration) *environmentCommandGuard {
	if timeout <= 0 {
		timeout = defaultEnvironmentCommandTimeout
	}
	return &environmentCommandGuard{failures: make(map[string]environmentCommandFailure), timeout: timeout}
}

func (guard *environmentCommandGuard) CommandTimeout() time.Duration {
	if guard == nil || guard.timeout <= 0 {
		return defaultEnvironmentCommandTimeout
	}
	return guard.timeout
}

func (guard *environmentCommandGuard) BlockedMessage(keys []string, command string) (string, bool) {
	if guard == nil {
		return "", false
	}
	classification, ok := classifyEnvironmentCommand(command)
	if !ok {
		return "", false
	}
	failure, ok := guard.findFailure(keys, classification)
	if !ok {
		return "", false
	}
	return formatEnvironmentCommandBlockedMessage(command, failure), true
}

func (guard *environmentCommandGuard) Record(keys []string, command string, output string, err error) {
	if guard == nil || err == nil {
		return
	}
	classification, ok := classifyEnvironmentCommand(command)
	if !ok {
		return
	}
	reason := err.Error()
	if errors.Is(err, context.DeadlineExceeded) {
		reason = "context deadline exceeded"
	} else if errors.Is(err, context.Canceled) {
		reason = "context canceled"
	}
	output = strings.TrimSpace(output)
	if len(output) > 2048 {
		output = output[len(output)-2048:]
		output = "... [truncated]\n" + output
	}
	guard.mu.Lock()
	defer guard.mu.Unlock()
	for _, key := range compactStopKeys(keys) {
		for _, signature := range classification.signatures() {
			failureKey := environmentFailureKey(key, signature)
			failure := guard.failures[failureKey]
			failure.Command = normalizeCommand(command)
			failure.Family = classification.Family
			failure.Reason = reason
			failure.Output = output
			failure.FailedAt = time.Now().UTC()
			failure.FailCount++
			guard.failures[failureKey] = failure
		}
	}
}

func (guard *environmentCommandGuard) findFailure(keys []string, classification environmentCommandClassification) (environmentCommandFailure, bool) {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	for _, key := range compactStopKeys(keys) {
		for _, signature := range classification.signatures() {
			if failure, ok := guard.failures[environmentFailureKey(key, signature)]; ok {
				return failure, true
			}
		}
	}
	return environmentCommandFailure{}, false
}

type environmentCommandClassification struct {
	Command string
	Family  string
}

func (classification environmentCommandClassification) signatures() []string {
	return []string{"cmd:" + classification.Command, "family:" + classification.Family}
}

func environmentFailureKey(scopeKey string, signature string) string {
	return scopeKey + "|" + signature
}

func classifyEnvironmentCommand(command string) (environmentCommandClassification, bool) {
	normalized := normalizeCommand(command)
	if normalized == "" {
		return environmentCommandClassification{}, false
	}
	fields := strings.Fields(normalized)
	if len(fields) == 0 {
		return environmentCommandClassification{}, false
	}
	family := ""
	switch fields[0] {
	case "rustup":
		if len(fields) >= 2 && isOneOf(fields[1], "install", "update") {
			family = "rustup"
		} else if len(fields) >= 3 && fields[1] == "toolchain" && isOneOf(fields[2], "install", "repair") {
			family = "rustup"
		}
	case "cargo":
		if len(fields) >= 2 && fields[1] == "install" {
			family = "cargo-install"
		}
	case "npm":
		if len(fields) >= 2 && isOneOf(fields[1], "install", "ci", "rebuild") {
			family = "npm-install"
		}
	case "pnpm":
		if len(fields) >= 2 && fields[1] == "install" {
			family = "pnpm-install"
		}
	case "yarn":
		if len(fields) >= 2 && fields[1] == "install" {
			family = "yarn-install"
		}
	case "pip":
		if len(fields) >= 2 && fields[1] == "install" {
			family = "pip-install"
		}
	case "python", "python3":
		if len(fields) >= 5 && fields[1] == "-m" && fields[2] == "pip" && fields[3] == "install" {
			family = "pip-install"
		}
	case "go":
		if len(fields) >= 2 && fields[1] == "install" {
			family = "go-install"
		}
	case "apt", "apt-get", "brew", "choco", "winget":
		if len(fields) >= 2 && fields[1] == "install" {
			family = fields[0] + "-install"
		}
	}
	if family == "" {
		return environmentCommandClassification{}, false
	}
	return environmentCommandClassification{Command: normalized, Family: family}, true
}

func normalizeCommand(command string) string {
	return strings.ToLower(strings.TrimSpace(whitespacePattern.ReplaceAllString(command, " ")))
}

func isOneOf(value string, options ...string) bool {
	for _, option := range options {
		if value == option {
			return true
		}
	}
	return false
}

func formatEnvironmentCommandBlockedMessage(command string, failure environmentCommandFailure) string {
	parts := []string{
		"This environment install/repair command already failed or timed out in this session, so it was not run again.",
		"",
		"Command:",
		strings.TrimSpace(command),
		"",
		"Previous failure:",
		failure.Reason,
	}
	if strings.TrimSpace(failure.Output) != "" {
		parts = append(parts, "", "Previous output tail:", failure.Output)
	}
	parts = append(parts,
		"",
		"Do not retry environment install/repair commands automatically. Fall back to generating the requested code/output from available context, run only light read-only checks if useful, or ask the user to repair the environment/toolchain manually.",
	)
	return strings.Join(parts, "\n")
}

func environmentCommandTimeoutMessage(command string, timeout time.Duration) string {
	return fmt.Sprintf("environment setup command timed out after %s. Do not retry this install/repair command automatically; fall back to generating the requested code/output, run only light read-only checks if useful, or ask the user to repair the environment/toolchain manually. Command: %s", timeout, strings.TrimSpace(command))
}
