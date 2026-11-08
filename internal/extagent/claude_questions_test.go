package extagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const claudeGroupInput = `{"questions":[{"header":"Mode","question":" Pick a mode ","options":[{"label":"ignored\u0001"},{"label":"\u001b[32mBrief\u001b[0m","description":"Short reply"}]},{"header":"Extras","multiSelect":true,"options":[{"label":"Tests"},{"label":"Docs"}]},{"question":"Notes"}],"metadata":{"opaque":9007199254740993},"answers":{"stale":"old"}}`

func TestClaudeQuestionGroupReturnsOneNativeReplyAndRecordsOnlyPublicChoices(t *testing.T) {
	b := NewBroker(NewStateStore(t.TempDir()), nil, nil)
	defer b.Close()
	sink := permissionReplaySink{make(chan ACPPermissionRequest, 2), make(chan ACPPermissionRequest, 2)}
	host := newClaudePromptHost(t.Context(), func(ctx context.Context, p acpPermissionParams) acpPermissionOutcome {
		return b.awaitPermission(ctx, "chat", AgentClaude, p, sink, 0)
	})
	in, c, err := openClaudeControl(strings.NewReader(""), nil, nil, host)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	defer c.finish()
	_ = in.SetReadDeadline(time.Now().Add(3 * time.Second))
	decoder := json.NewDecoder(in)
	var init json.RawMessage
	if err := decoder.Decode(&init); err != nil {
		t.Fatal(err)
	}
	out, provider := io.Pipe()
	defer out.Close()
	defer provider.Close()
	var activity []Activity
	done := make(chan error, 1)
	go func() {
		_, _, err := readClaudeContextStream(out, func(a Activity) { activity = append(activity, a) }, "root", nil, c)
		done <- err
	}()
	write := func(frame string) {
		t.Helper()
		if _, err := io.WriteString(provider, frame+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"type":"control_request","request_id":"native","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","tool_use_id":"tool","agent_id":"worker","requires_user_interaction":true,"input":` + claudeGroupInput + `}}`)
	card := takeQuestionEvent(t, sink.start)
	if card.Dialog == nil || len(card.Dialog.Questions) != 3 || card.Dialog.Header != "子 Agent：worker" || card.Dialog.Questions[0].Options[1].Label != "Brief" ||
		!card.Dialog.Questions[1].MultiSelect || card.ExternalSessionID != "root" || !b.HasPendingPrompt("chat") {
		t.Fatal(card)
	}
	answers := []QuestionAnswer{{QuestionID: "q3", Other: " private note "}, {QuestionID: "q1", OptionIDs: []string{"q1:choice-2"}}, {QuestionID: "q2", OptionIDs: []string{"q2:choice-2", "q2:choice-1"}, Other: " extra "}}
	for _, bad := range [][]QuestionAnswer{nil, answers[:2], {answers[0], answers[0], answers[1]},
		{answers[0], {QuestionID: "q1", OptionIDs: []string{"q2:choice-1"}}, answers[2]},
		{answers[0], {QuestionID: "q1", OptionIDs: []string{"q1:choice-1", "q1:choice-2"}}, answers[2]},
		{answers[0], {QuestionID: "q1", OptionIDs: []string{"q1:choice-1"}, Other: "both"}, answers[2]},
		{answers[0], answers[1], {QuestionID: "q2", OptionIDs: []string{"q2:choice-1", "q2:choice-1"}}},
		{{QuestionID: "q3", Other: " "}, answers[1], answers[2]},
	} {
		if err := b.RespondPromptWithAnswers(card.RequestID, "submit", nil, bad); err == nil {
			t.Fatal("accepted invalid group", bad)
		}
	}
	c.writeMu.Lock()
	unlock := sync.OnceFunc(c.writeMu.Unlock)
	defer unlock()
	if err := b.RespondPromptWithAnswers(card.RequestID, "submit", nil, answers); err != nil {
		t.Fatal(err)
	}
	if err := b.RespondPromptWithAnswers(card.RequestID, "submit", nil, answers); !errors.Is(err, ErrPermissionNotFound) {
		t.Fatal(err)
	}
	answers[1].OptionIDs[0] = "q1:choice-1"
	if !b.HasPendingPrompt("chat") {
		t.Fatal("unblocked before native writer received the answers")
	}
	unlock()
	var reply struct {
		Type     string
		Response struct {
			ID       string `json:"request_id"`
			Subtype  string
			Response struct {
				Behavior, ToolUseID string
				UpdatedInput        struct {
					Questions json.RawMessage
					Metadata  json.RawMessage
					Answers   map[string]any
				}
			}
		}
	}
	if err := decoder.Decode(&reply); err != nil {
		t.Fatal(err)
	}
	r := reply.Response.Response
	want := map[string]any{" Pick a mode ": "\x1b[32mBrief\x1b[0m", "Extras": []any{"Docs", "Tests", "extra"}, "Notes": "private note"}
	if reply.Type != "control_response" || reply.Response.ID != "native" || reply.Response.Subtype != "success" || r.Behavior != "allow" || r.ToolUseID != "tool" ||
		!reflect.DeepEqual(r.UpdatedInput.Answers, want) || len(r.UpdatedInput.Questions) == 0 || string(r.UpdatedInput.Metadata) != `{"opaque":9007199254740993}` {
		t.Fatal(reply)
	}
	end := takeQuestionEvent(t, sink.end)
	if end.RequestID != card.RequestID || end.Selected != "submit" || end.Expired || b.HasPendingPrompt("chat") || card.Dialog.Questions[0].Options[1].Selected ||
		!end.Dialog.Questions[0].Options[1].Selected || !end.Dialog.Questions[1].Options[0].Selected || !end.Dialog.Questions[1].Options[1].Selected {
		t.Fatal(card, end)
	}
	encoded, _ := json.Marshal(end)
	if strings.Contains(string(encoded), "private note") || strings.Contains(string(encoded), `"extra"`) || strings.Contains(string(encoded), "9007199254740993") {
		t.Fatal(string(encoded))
	}
	write(`{"type":"assistant","session_id":"root","message":{"content":[{"type":"tool_use","id":"tool","name":"AskUserQuestion","input":{"answers":{"Notes":"private note"}}}]}}`)
	write(`{"type":"user","session_id":"root","message":{"content":[{"type":"tool_result","tool_use_id":"tool","content":"private note"},{"type":"tool_result","tool_use_id":"normal","content":"public result"}]}}`)
	write(`{"type":"result","session_id":"root","result":"done"}`)
	provider.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(activity) != 3 || activity[0].Input != nil || activity[1].Output != "" || activity[1].Status != "succeeded" || activity[2].Output != "public result" {
		t.Fatal(activity)
	}
	if err := decoder.Decode(&init); err != io.EOF {
		t.Fatal("unexpected second reply", err, string(init))
	}
}

func TestClaudeQuestionStopAndMalformedGroupCannotGrantToolUse(t *testing.T) {
	request := claudeToolRequest{Name: "AskUserQuestion", ToolID: "tool", Input: json.RawMessage(claudeGroupInput)}
	result := request.decide(t.Context(), "root", func(context.Context, acpPermissionParams) acpPermissionOutcome {
		return acpPermissionOutcome{Outcome: "selected", OptionID: "cancel"}
	}, new(permissionReceipt))
	if result["behavior"] != "deny" || result["interrupt"] != true || result["updatedInput"] != nil {
		t.Fatal(result)
	}
	for _, input := range []string{`{}`, `{"questions":[]}`, `{"questions":[null]}`, `{"questions":[{"question":"same"},{"header":"same"}]}`} {
		request.Input = json.RawMessage(input)
		result = request.decide(t.Context(), "root", func(context.Context, acpPermissionParams) acpPermissionOutcome {
			t.Fatal("published malformed group")
			return acpPermissionOutcome{}
		}, new(permissionReceipt))
		if result["behavior"] != "deny" {
			t.Fatal(result)
		}
	}
}

func TestClaudeQuestionWithdrawalOrBrokenPipeExpiresSubmittedAnswers(t *testing.T) {
	for _, mode := range []string{"withdraw and reuse", "broken pipe", "broker closed"} {
		t.Run(mode, func(t *testing.T) {
			b := NewBroker(NewStateStore(t.TempDir()), nil, nil)
			defer b.Close()
			sink := permissionReplaySink{make(chan ACPPermissionRequest, 3), make(chan ACPPermissionRequest, 3)}
			claimed := make(chan struct{}, 2)
			host := newClaudePromptHost(t.Context(), func(ctx context.Context, p acpPermissionParams) acpPermissionOutcome {
				answer := b.awaitPermission(ctx, "chat", AgentClaude, p, sink, 0)
				claimed <- struct{}{}
				return answer
			})
			in, c, err := openClaudeControl(strings.NewReader(""), nil, nil, host)
			if err != nil {
				t.Fatal(err)
			}
			defer in.Close()
			defer c.finish()
			_ = in.SetReadDeadline(time.Now().Add(3 * time.Second))
			decoder := json.NewDecoder(in)
			var raw json.RawMessage
			if err := decoder.Decode(&raw); err != nil {
				t.Fatal(err)
			}
			req := claudeToolRequest{Subtype: "can_use_tool", Name: "AskUserQuestion", ToolID: "old", Input: json.RawMessage(`{"questions":[{"question":"One","options":[{"label":"First"}]}]}`)}
			if err := c.dispatch("id", "root", req); err != nil {
				t.Fatal(err)
			}
			old := takeQuestionEvent(t, sink.start)
			c.writeMu.Lock()
			unlock := sync.OnceFunc(c.writeMu.Unlock)
			defer unlock()
			if err := b.RespondPromptWithAnswers(old.RequestID, "submit", nil, []QuestionAnswer{{QuestionID: "q1", OptionIDs: []string{"q1:choice-1"}}}); err != nil {
				t.Fatal(err)
			}
			<-claimed
			var replacement ACPPermissionRequest
			switch mode {
			case "broken pipe":
				in.Close()
			case "broker closed":
				if err := b.Close(); err != nil {
					t.Fatal(err)
				}
			case "withdraw and reuse":
				host.withdraw("id")
				req.ToolID = "new"
				if err := c.dispatch("id", "root", req); err != nil {
					t.Fatal(err)
				}
				replacement = takeQuestionEvent(t, sink.start)
				if replacement.RequestID == old.RequestID {
					t.Fatal("reused local request id")
				}
			}
			unlock()
			end := takeQuestionEvent(t, sink.end)
			if end.RequestID != old.RequestID || !end.Expired || end.Selected != "" || end.Dialog.Questions[0].Options[0].Selected {
				t.Fatal(end)
			}
			if mode != "withdraw and reuse" {
				if b.HasPendingPrompt("chat") {
					t.Fatal("expired group still blocks queue")
				}
				if mode == "broker closed" {
					if err := decoder.Decode(&raw); err != nil || !strings.Contains(string(raw), `"behavior":"deny"`) || strings.Contains(string(raw), `"updatedInput"`) {
						t.Fatal(string(raw), err)
					}
				}
				return
			}
			if err := b.RespondPermission(replacement.RequestID, "cancel"); err != nil {
				t.Fatal(err)
			}
			if err := decoder.Decode(&raw); err != nil || !strings.Contains(string(raw), `"toolUseID":"new"`) || !strings.Contains(string(raw), `"interrupt":true`) || strings.Contains(string(raw), `"answers"`) {
				t.Fatal(string(raw), err)
			}
			end = takeQuestionEvent(t, sink.end)
			if end.RequestID != replacement.RequestID || !end.Expired || b.HasPendingPrompt("chat") {
				t.Fatal(end)
			}
		})
	}
}
