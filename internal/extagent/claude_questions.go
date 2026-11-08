package extagent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"bqagent/internal/transcript"
)

// Only option IDs and authored text cross the reply endpoint. Display labels
// and the native answer keys come from the original can_use_tool request.
type QuestionAnswer struct {
	QuestionID string   `json:"question_id"`
	OptionIDs  []string `json:"option_ids,omitempty"`
	Other      string   `json:"other,omitempty"`
}

func orderedQuestionAnswers(questions []transcript.PromptQuestion, answers []QuestionAnswer) ([]QuestionAnswer, error) {
	if len(questions) == 0 || len(answers) != len(questions) {
		return nil, fmt.Errorf("请回答全部问题")
	}
	byID := make(map[string]QuestionAnswer, len(answers))
	for _, a := range answers {
		if _, found := byID[a.QuestionID]; found {
			return nil, fmt.Errorf("同一道问题只能提交一次")
		}
		a.Other = strings.TrimSpace(a.Other)
		byID[a.QuestionID] = a
	}
	var ordered []QuestionAnswer
	for _, q := range questions {
		a, ok := byID[q.ID]
		if !ok || len(a.OptionIDs) == 0 && a.Other == "" {
			return nil, fmt.Errorf("请回答「%s」", q.Title)
		}
		if len(a.Other) > 64<<10 || len(a.OptionIDs) > len(q.Options) || !q.MultiSelect && (len(a.OptionIDs) > 1 || a.Other != "" && len(a.OptionIDs) != 0) {
			return nil, fmt.Errorf("「%s」的答案无效", q.Title)
		}
		seen := make(map[string]bool, len(a.OptionIDs))
		for _, id := range a.OptionIDs {
			if seen[id] || !slices.ContainsFunc(q.Options, func(o transcript.PromptOption) bool { return o.ID == id }) {
				return nil, fmt.Errorf("「%s」包含无效的选项", q.Title)
			}
			seen[id] = true
		}
		a.OptionIDs = slices.Clone(a.OptionIDs)
		ordered = append(ordered, a)
	}
	return ordered, nil
}

// Make a public copy for the resolution event. Authored answers stay on the
// CLI pipe, including when selected options and free text share one answer.
func answeredDialog(dialog *AgentDialog, answers []QuestionAnswer) *AgentDialog {
	result := *dialog
	result.Questions = slices.Clone(dialog.Questions)
	for i, a := range answers {
		q := &result.Questions[i]
		q.Options = slices.Clone(q.Options)
		for j := range q.Options {
			q.Options[j].Selected = slices.Contains(a.OptionIDs, q.Options[j].ID)
		}
	}
	return &result
}

type claudeQuestion struct {
	Question, Header string
	MultiSelect      bool
	Options          []struct{ Label, Description string }
}

func (q claudeQuestion) key() string {
	if strings.TrimSpace(q.Question) != "" {
		return q.Question
	}
	return q.Header
}

func claudeQuestionForm(questions []claudeQuestion) ([]transcript.PromptQuestion, map[string]string, error) {
	if len(questions) == 0 || len(questions) > 64 {
		return nil, nil, fmt.Errorf("invalid Claude question count")
	}
	keys := make(map[string]bool, len(questions))
	labels := make(map[string]string)
	var rows []transcript.PromptQuestion
	var options int
	for i, q := range questions {
		id := fmt.Sprintf("q%d", i+1)
		key := q.key()
		title := strings.TrimSpace(claudeDisplayText(key))
		if title == "" || keys[key] {
			return nil, nil, fmt.Errorf("empty or duplicate Claude question")
		}
		keys[key] = true
		row := transcript.PromptQuestion{ID: id, Title: title, Header: claudeDisplayText(q.Header), MultiSelect: q.MultiSelect}
		options += len(q.Options)
		if options > 256 {
			return nil, nil, fmt.Errorf("too many Claude question options")
		}
		for j, o := range q.Options {
			label := strings.TrimSpace(claudeDisplayText(o.Label))
			if label == "" {
				continue
			}
			optionID := fmt.Sprintf("%s:choice-%d", id, j+1)
			labels[optionID] = o.Label
			row.Options = append(row.Options, transcript.PromptOption{ID: optionID, Label: label, Description: claudeDisplayText(o.Description)})
		}
		rows = append(rows, row)
	}
	return rows, labels, nil
}

func (p claudeToolRequest) askQuestions(ctx context.Context, session string, ask piDialogHandler, receipt *permissionReceipt) map[string]any {
	var input map[string]json.RawMessage
	var questions []claudeQuestion
	if len(p.Input) > 128<<10 || json.Unmarshal(p.Input, &input) != nil || json.Unmarshal(input["questions"], &questions) != nil {
		return p.deny("Invalid Claude question group.")
	}
	rows, labels, err := claudeQuestionForm(questions)
	if err != nil {
		return p.deny(err.Error())
	}
	dialog := &AgentDialog{Method: "question", Title: "Claude 提问", Questions: rows}
	if p.Agent != "" {
		dialog.Header = "子 Agent：" + claudeDisplayText(p.Agent)
	}
	outcome := ask(ctx, acpPermissionParams{SessionID: session, Dialog: dialog, receipt: receipt, Options: []ACPPermissionOption{
		{OptionID: "submit", Name: "提交回答"}, {OptionID: "cancel", Name: "停止本轮", Kind: "reject_once"},
	}})
	if outcome.Outcome != "selected" || outcome.OptionID != "submit" {
		result := p.deny("The user dismissed these questions without answering. End your turn and wait for them.")
		if outcome.Outcome == "selected" && outcome.OptionID == "cancel" {
			result["interrupt"] = true
		}
		return result
	}
	ordered, err := orderedQuestionAnswers(rows, outcome.Answers)
	if err != nil {
		return p.deny("Invalid Claude question answers.")
	}
	answers := make(map[string]any, len(rows))
	for i, a := range ordered {
		q := questions[i]
		var values []string
		for _, id := range a.OptionIDs {
			values = append(values, labels[id])
		}
		if a.Other != "" {
			values = append(values, a.Other)
		}
		if q.MultiSelect {
			answers[q.key()] = values
		} else {
			answers[q.key()] = values[0]
		}
	}
	input["answers"], _ = json.Marshal(answers)
	return map[string]any{"behavior": "allow", "toolUseID": p.ToolID, "updatedInput": input}
}
