package agent

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	// DefaultInvestigationRoundLimit is the number of read-only investigation
	// rounds after which the model is reminded to converge.
	DefaultInvestigationRoundLimit = 12
	investigationRecoveryPrompt    = "Investigation checkpoint: summarize the facts already established and name the specific unresolved question that each further lookup will answer. If enough evidence is available for the authorized implementation, take the smallest concrete implementation step now. For review, analysis, or other read-only work, synthesize the findings or perform only the focused lookup needed to resolve a named uncertainty; do not edit files merely to show progress. If genuinely blocked, explain the concrete missing information or authorization. Do not treat this reminder as permission, a requirement to run tests, or a reason to stop unfinished work. Avoid rereading unchanged regions or merely changing search parameters."
)

// This is an advisory budget, not a proof that investigation is unnecessary.
// Different successful searches still consume it; prose promises and bookkeeping
// cannot reset it. It is independent of the optional hard duplicate-call guard.
type investigationGuard struct {
	limit                    int // <= 0 disables the reminder
	rounds                   int
	investigating, milestone bool
}

// investigationRoundLimit resolves the Options value: zero selects the
// default, a negative value disables the reminder.
func investigationRoundLimit(option int) int {
	switch {
	case option == 0:
		return DefaultInvestigationRoundLimit
	case option < 0:
		return 0
	default:
		return option
	}
}

func (g *investigationGuard) reset() {
	*g = investigationGuard{limit: g.limit}
}

func (g *investigationGuard) beginRound() {
	g.investigating, g.milestone = false, false
}

func (g *investigationGuard) observe(call ToolCall, result string, fresh bool) {
	if strings.HasPrefix(strings.TrimSpace(result), "Error:") {
		return
	}
	args, err := parseArguments(call.Function.Arguments)
	if err != nil {
		return
	}
	object, ok := args.(map[string]any)
	if !ok {
		return
	}
	name := call.Function.Name
	if name == "bash" || name == "bash_wait" {
		if status, ok := bashWaitStatus(result); ok {
			if status.Status != "completed" || status.ExitCode == nil || *status.ExitCode != 0 || status.Truncated {
				return
			}
			object = map[string]any{"command": status.Command}
			name = "bash"
		} else if name == "bash_wait" {
			return
		}
	}
	if fresh && (name == "edit_file" || name == "write_file") {
		g.milestone = true
		return
	}
	if name == "bash" {
		command, _ := object["command"].(string)
		kind := classifyBash(command)
		if fresh && kind == bashVerify && reliableExitStatus(command) {
			g.milestone = true
			return
		}
		// The evidence classifier deliberately treats sed as potentially
		// mutating. Recognize only its simple numeric print form here.
		if kind != bashReadOnly && classifyBash(sedReadCommand.ReplaceAllString(command, "head")) != bashReadOnly {
			return
		}
	} else if name != "read_file" && name != "grep" && name != "glob" && name != "web_search" && name != "web_fetch" {
		return
	}
	g.investigating = true
}

func (g *investigationGuard) finishRound() string {
	if g.milestone {
		g.reset()
		return ""
	}
	if !g.investigating || g.limit <= 0 {
		return ""
	}
	g.rounds++
	if g.rounds < g.limit {
		return ""
	}
	prompt := fmt.Sprintf("%s Observed %d investigation rounds since the last checkpoint or concrete step.", investigationRecoveryPrompt, g.rounds)
	g.rounds = 0
	return prompt
}

var sedReadCommand = regexp.MustCompile(`\bsed\s+-n\s+(?:'[0-9]+(?:,[0-9]+)?p'|"[0-9]+(?:,[0-9]+)?p"|[0-9]+(?:,[0-9]+)?p)`)
