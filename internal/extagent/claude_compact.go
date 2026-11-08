package extagent

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// The caller appends exactly one resume ID. Retain the wrapper and provider
// settings, but do not let launch-time session flags fork or select another chat.
func claudeCompactArgs(args []string) []string {
	start := 0
	if sep := commandWrapperSeparator(args); sep >= 0 {
		start = sep + 2
	}
	result := append([]string(nil), args[:start]...)
	for i := start; i < len(args); i++ {
		flag, _, inline := strings.Cut(args[i], "=")
		switch flag {
		case "-c", "--continue", "--fork-session", "--no-session-persistence", "--reply-on-resume":
		case "--session-id", "-r", "--resume", "--resume-session-at", "--resume-drops-turn":
			if !inline && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
		default:
			result = append(result, args[i])
		}
	}
	return result
}

// Claude can end the print request successfully even when a compaction did
// not happen. Accept only a manual boundary and a terminal result for the
// resumed root session. Compaction summaries stay inside the native session.
func readClaudeCompact(r io.Reader, id string) (string, error) {
	return readClaudeCompactUsage(r, id, nil, nil)
}

func readClaudeCompactUsage(r io.Reader, id string, report usageSink, control *claudeUsageControl) (string, error) {
	decoder := json.NewDecoder(r)
	var marked, finished bool
	var failure string
	for {
		var frame struct {
			Response              claudeControlReply
			Type, Subtype, Result string
			SessionID             string `json:"session_id"`
			Parent                string `json:"parent_tool_use_id"`
			IsError               bool   `json:"is_error"`
			Errors                []string
			Metadata              struct{ Trigger string } `json:"compact_metadata"`
		}
		err := decoder.Decode(&frame)
		if err == io.EOF {
			break
		}
		if err != nil {
			if control != nil && finished && control.session != "" {
				break
			}
			return "", err
		}
		if control != nil && frame.Parent == "" {
			switch frame.Type {
			case "control_response":
				control.reply(frame.Response)
			case "control_request":
				control.release()
			}
		}
		if frame.SessionID != id || frame.Parent != "" {
			continue
		}
		switch frame.Type {
		case "system":
			if frame.Subtype == "init" && control != nil {
				control.activity()
			}
			if frame.Subtype == "compact_boundary" && frame.Metadata.Trigger == "manual" {
				marked = true
				if control != nil {
					control.activity()
				}
				if report != nil {
					report(id, ContextUsage{})
				}
			}
		case "result":
			finished = true
			if marked && control != nil {
				control.result(id)
			} else if control != nil {
				control.release()
			}
			if frame.IsError || strings.HasPrefix(frame.Subtype, "error") {
				failure = firstNonEmpty(strings.Join(frame.Errors, "\n"), frame.Result, frame.Subtype, "未知错误")
			}
		}
	}
	if !finished {
		return "", fmt.Errorf("Claude 未返回当前压缩会话的结束结果")
	}
	if marked {
		reply := "Claude 会话上下文已压缩。"
		if failure != "" {
			reply += "\n" + failure
		}
		return reply, nil
	}
	if failure != "" {
		return "", fmt.Errorf("Claude 压缩失败：%s", failure)
	}
	return "", fmt.Errorf("Claude 回合已结束，但未确认当前会话发生手动压缩")
}
