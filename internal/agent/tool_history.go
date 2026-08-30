package agent

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

const (
	CompletedToolActivityLead = "Completed tool activity (retain this evidence for later reasoning):"
	completedToolResultLead   = "Completed tool result:"
	assistantNoteLead         = "Assistant note: "
	callsLead                 = "Calls: "
)

var completedToolResultHeader = regexp.MustCompile(`(?m)^Result ([^\n:]*):\n`)

type HistoryTool struct {
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Result    string         `json:"result,omitempty"`
	Status    string         `json:"status,omitempty"`
	Truncated bool           `json:"truncated,omitempty"`
}

type completedToolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

// ParseCompletedToolActivity unwraps compact-transcript tool summaries back
// into structured tools plus any leftover assistant note.
func ParseCompletedToolActivity(content string) (note string, tools []HistoryTool, ok bool) {
	content = strings.TrimSpace(content)
	if content == "" {
		return "", nil, false
	}
	if strings.HasPrefix(content, completedToolResultLead) {
		result := strings.TrimSpace(strings.TrimPrefix(content, completedToolResultLead))
		preview, truncated := boundedToolEventPreview(result)
		return "", []HistoryTool{{
			Name:      "tool",
			Result:    preview,
			Status:    historyToolStatus(result),
			Truncated: truncated,
		}}, true
	}
	if !strings.HasPrefix(content, CompletedToolActivityLead) {
		return content, nil, false
	}
	body := strings.TrimSpace(strings.TrimPrefix(content, CompletedToolActivityLead))
	if strings.HasPrefix(body, assistantNoteLead) {
		rest := strings.TrimPrefix(body, assistantNoteLead)
		if index := strings.Index(rest, "\n"+callsLead); index >= 0 {
			note = strings.TrimSpace(rest[:index])
			body = strings.TrimSpace(rest[index+1:])
		} else if index := strings.Index(rest, "\nResult "); index >= 0 {
			note = strings.TrimSpace(rest[:index])
			body = strings.TrimSpace(rest[index+1:])
		} else {
			note = strings.TrimSpace(rest)
			body = ""
		}
	}
	calls := []completedToolCall{}
	if strings.HasPrefix(body, callsLead) {
		payload := strings.TrimPrefix(body, callsLead)
		decoder := json.NewDecoder(strings.NewReader(payload))
		if err := decoder.Decode(&calls); err != nil {
			calls = nil
		} else {
			body = strings.TrimSpace(payload[decoder.InputOffset():])
		}
	}
	results := extractCompletedToolResults(body, calls)
	tools = make([]HistoryTool, 0, len(results))
	seen := map[string]bool{}
	for _, call := range calls {
		result := resultByID(results, call.ID)
		preview, truncated := boundedToolEventPreview(result)
		tools = append(tools, HistoryTool{
			ID:        call.ID,
			Name:      strings.TrimSpace(call.Function.Name),
			Arguments: parseHistoryToolArguments(call.Function.Arguments),
			Result:    preview,
			Status:    historyToolStatus(result),
			Truncated: truncated,
		})
		if call.ID != "" {
			seen[call.ID] = true
		}
	}
	for _, item := range results {
		if seen[item.id] {
			continue
		}
		preview, truncated := boundedToolEventPreview(item.content)
		tools = append(tools, HistoryTool{
			ID:        item.id,
			Name:      "tool",
			Result:    preview,
			Status:    historyToolStatus(item.content),
			Truncated: truncated,
		})
	}
	return note, tools, true
}

func extractCompletedToolResults(body string, calls []completedToolCall) []namedToolResult {
	results := make([]namedToolResult, 0, len(calls))
	for index, call := range calls {
		if strings.TrimSpace(call.ID) == "" {
			continue
		}
		header := "Result " + call.ID + ":\n"
		start := strings.Index(body, header)
		if start < 0 {
			continue
		}
		start += len(header)
		end := len(body)
		for _, next := range calls[index+1:] {
			if strings.TrimSpace(next.ID) == "" {
				continue
			}
			if at := strings.Index(body[start:], "Result "+next.ID+":\n"); at >= 0 {
				end = start + at
				break
			}
		}
		results = append(results, namedToolResult{id: call.ID, content: strings.TrimRight(body[start:end], "\n")})
	}
	if len(results) > 0 {
		return results
	}
	return parseCompletedToolResults(body)
}

type namedToolResult struct {
	id      string
	content string
}

func parseCompletedToolResults(body string) []namedToolResult {
	matches := completedToolResultHeader.FindAllStringSubmatchIndex(body, -1)
	results := make([]namedToolResult, 0, len(matches))
	for index, loc := range matches {
		start := loc[1]
		end := len(body)
		if index+1 < len(matches) {
			end = matches[index+1][0]
		}
		results = append(results, namedToolResult{
			id:      body[loc[2]:loc[3]],
			content: strings.TrimRight(body[start:end], "\n"),
		})
	}
	return results
}

func resultByID(results []namedToolResult, id string) string {
	for _, item := range results {
		if item.id == id {
			return item.content
		}
	}
	return ""
}

func parseHistoryToolArguments(raw json.RawMessage) map[string]any {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return map[string]any{"raw": string(raw)}
		}
		return parseHistoryToolArguments([]byte(text))
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return map[string]any{"raw": string(raw)}
	}
	return object
}

func historyToolStatus(result string) string {
	if strings.HasPrefix(strings.TrimSpace(result), "Error:") {
		return "failed"
	}
	return "succeeded"
}
