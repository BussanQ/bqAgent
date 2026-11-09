package transcript

import "strings"

type Plan struct {
	Mode  string     `json:"mode"`
	Steps []PlanStep `json:"steps,omitempty"`
}

type PlanStep struct {
	Text     string `json:"text"`
	Status   string `json:"status"`
	Priority string `json:"priority,omitempty"`
}

// ReplyText is the textual result for clients when a native turn produces
// a plan without an assistant answer. Keep the native ordering and wording.
func (p Plan) ReplyText(body string) string {
	var lines []string
	if body = strings.TrimSpace(body); body != "" {
		lines = append(lines, body)
	}
	for _, step := range p.Steps {
		mark := "[ ]"
		switch step.Status {
		case "completed":
			mark = "[x]"
		case "in_progress":
			mark = "[-]"
		}
		lines = append(lines, "- "+mark+" "+step.Text)
	}
	return strings.Join(lines, "\n")
}

func PartialReply(parts []Part) string {
	var text []string
	var planText string
	for _, p := range parts {
		if p.Kind == "text" && p.Text != "" {
			text = append(text, p.Text)
		}
		if p.Kind == "plan" && p.Plan != nil {
			if value := p.Plan.ReplyText(p.Text); value != "" {
				planText = value
			}
		}
	}
	if len(text) > 0 {
		return strings.Join(text, "\n\n")
	}
	return planText
}
