package agent

import (
	"encoding/json"
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

// Investigation escalates independently of exact duplicate-call protection.
// Reading new output and promising progress cannot reset the escalation.
type investigationGuard struct {
	limit                    int // <= 0 disables escalation
	rounds, level            int
	investigating, milestone bool
	verified, edited         bool
	decisionPending          bool
	decisionUsed, exhausted  bool
}

const investigationDecisionPrompt = `Investigation decision checkpoint: tools are disabled for this response. Return only one JSON object with these string fields: action (implement, investigate, blocked, or complete), facts (established facts and evidence locations), deliverable (the smallest next deliverable), next_step (a concrete action and its verification), blocker (the specific unanswered question or missing prerequisite), and report (a user-facing explanation or completed analysis in the user's language). Preserve all user requirements. Choose implement when enough is known; prefer edit_file/write_file for concrete changes because opaque shell commands do not establish progress. Choose investigate only for one named uncertainty that changes the implementation or conclusion, with at most three further tool rounds. Choose blocked only for a concrete obstacle, not routine choices or failed bookkeeping. Choose complete only when the requested work or read-only analysis is actually delivered in report. Do not edit files merely to show progress, invent a requirement to test, or claim unfinished work is complete.`

const investigationStopPrompt = `Investigation recovery exhausted: the decision did not lead to a concrete step within the remaining tool budget. Tools remain disabled. Return the same JSON decision object, using action=blocked and report to explain the lack of progress, established facts, completed changes, verification results, remaining work, and the specific next step or obstacle. Do not ask for permission for routine implementation choices. This is an incomplete checkpoint, not successful task completion.`

type investigationDecision struct {
	Action      string `json:"action"`
	Facts       string `json:"facts"`
	Deliverable string `json:"deliverable"`
	NextStep    string `json:"next_step"`
	Blocker     string `json:"blocker"`
	Report      string `json:"report"`
}

func parseInvestigationDecision(text string) (investigationDecision, error) {
	var decision investigationDecision
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &decision); err != nil {
		return decision, err
	}
	if strings.TrimSpace(decision.Facts) == "" {
		return decision, fmt.Errorf("missing established facts")
	}
	switch decision.Action {
	case "implement", "investigate":
		if strings.TrimSpace(decision.Deliverable) == "" || strings.TrimSpace(decision.NextStep) == "" || decision.Action == "investigate" && strings.TrimSpace(decision.Blocker) == "" {
			return decision, fmt.Errorf("missing deliverable, next step, or investigation question")
		}
	case "blocked":
		if strings.TrimSpace(decision.Blocker) == "" || strings.TrimSpace(decision.Report) == "" {
			return decision, fmt.Errorf("missing blocker or report")
		}
	case "complete":
		if strings.TrimSpace(decision.Report) == "" {
			return decision, fmt.Errorf("missing completed report")
		}
	default:
		return decision, fmt.Errorf("invalid investigation action %q", decision.Action)
	}
	return decision, nil
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
	g.investigating, g.milestone, g.verified = false, false, false
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
		if path, _ := object["path"].(string); strings.HasPrefix(path, "/tmp/") || strings.HasPrefix(path, "/private/tmp/") {
			return
		}
		g.milestone = true
		g.edited = true
		return
	}
	if name == "bash" {
		command, _ := object["command"].(string)
		kind := classifyBash(command)
		if fresh && kind == bashVerify && reliableExitStatus(command) && g.edited {
			g.verified = true
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
	if g.limit <= 0 {
		return ""
	}
	if g.verified && !g.milestone {
		g.reset()
		return ""
	}
	if g.milestone {
		g.level = max(0, g.level-1)
		g.rounds = 0
		g.decisionPending, g.decisionUsed, g.exhausted = false, false, false
		return ""
	}
	// After a decision, every tool round spends the small recovery budget,
	// including bookkeeping and opaque shell commands that evade classification.
	if !g.investigating && !g.decisionUsed {
		return ""
	}
	g.rounds++
	threshold := max(1, g.limit/(1<<min(g.level, 2)))
	if g.decisionUsed {
		threshold = min(3, threshold)
	}
	if g.rounds < threshold {
		return ""
	}
	g.rounds = 0
	if g.decisionUsed {
		g.decisionPending, g.exhausted = true, true
		return investigationStopPrompt
	}
	g.level++
	switch g.level {
	case 1:
		return fmt.Sprintf("%s Observed %d investigation rounds. Record established facts, the remaining uncertainty, and the next verifiable action.", investigationRecoveryPrompt, threshold)
	case 2:
		return investigationRecoveryPrompt + " Second checkpoint: choose one smallest deliverable now. Preserve the other requirements as remaining work; stop expanding scope. Each further lookup must resolve a named uncertainty that directly blocks that deliverable."
	default:
		g.decisionPending = true
		return investigationDecisionPrompt
	}
}

var sedReadCommand = regexp.MustCompile(`\bsed\s+-n\s+(?:'[0-9]+(?:,[0-9]+)?p'|"[0-9]+(?:,[0-9]+)?p"|[0-9]+(?:,[0-9]+)?p)`)
