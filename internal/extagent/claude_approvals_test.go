package extagent

import (
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestClaudeApprovalRoutingPreservesExplicitHandlersAndWrapper(t *testing.T) {
	wrapped := []string{"--permission-prompts", "none", "--", "claude", "--model", "sonnet", "--permission-mode", "plan"}
	got, ok := claudeApprovalArgs(wrapped)
	if !ok || !slices.Equal(got, append(slices.Clone(wrapped), "--permission-prompt-tool", "stdio")) {
		t.Fatal(got, ok)
	}
	configured := []string{"--permission-prompt-tool=stdio", "--permission-prompts", "host"}
	got, ok = claudeApprovalArgs(configured)
	if !ok || !slices.Equal(got, configured) {
		t.Fatal(got)
	}
	for _, args := range [][]string{{"--permission-prompts=none"}, {"--permission-prompt-tool", "mcp__auth"}, {"--permission-prompts"}} {
		if got, ok := claudeApprovalArgs(args); ok || !slices.Equal(got, args) {
			t.Fatal(got, ok)
		}
	}
}

func TestClaudeToolConsentReturnsScopedDecisionOnOriginalInput(t *testing.T) {
	delivered := make(chan bool, 1)
	ask := func(_ context.Context, p acpPermissionParams) acpPermissionOutcome {
		if p.SessionID != "root" || !strings.Contains(string(p.ToolCall), "worker") || !strings.Contains(string(p.ToolCall), "blocked.file") ||
			strings.Contains(string(p.ToolCall), "\\u001b") || len(p.Options) != 3 || p.Options[0].OptionID != "deny" {
			t.Errorf("unexpected card: %s %+v", p.ToolCall, p.Options)
		}
		if p.Options[len(p.Options)-1].Description == "" || strings.Contains(p.Options[len(p.Options)-1].Description, "userSettings") {
			t.Errorf("missing session scope: %+v", p.Options)
		}
		p.receipt.publish = func(ok bool) { delivered <- ok }
		return acpPermissionOutcome{Outcome: "selected", OptionID: "allowForSession"}
	}
	host := newClaudePromptHost(t.Context(), ask)
	in, c, err := openClaudeControl(strings.NewReader("{\"type\":\"user\",\"message\":\"hello\"}\n"), nil, nil, host)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	defer c.finish()
	_ = in.SetReadDeadline(time.Now().Add(3 * time.Second))
	decoder := json.NewDecoder(in)
	var init, user struct{ Type string }
	if decoder.Decode(&init) != nil || init.Type != "control_request" || decoder.Decode(&user) != nil || user.Type != "user" {
		t.Fatal(init, user)
	}
	out, provider := io.Pipe()
	defer out.Close()
	defer provider.Close()
	completed := make(chan error, 1)
	go func() {
		reply, session, err := readClaudeContextStream(out, func(Activity) {}, "root", nil, c)
		if err == nil && (reply != "ok" || session != "root") {
			t.Errorf("reply = %q session = %q", reply, session)
		}
		completed <- err
	}()
	write := func(line string) {
		t.Helper()
		if _, err := io.WriteString(provider, line+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"type":"result","session_id":"another","result":"wrong"}`)
	write(`{"type":"control_request","request_id":"native","parent_tool_use_id":"parent","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"tool","agent_id":"worker","blocked_path":"blocked.file","title":"\u001b[31mRun\u001b[0m","default_to_no":true,"input":{"n":9007199254740993},"permission_suggestions":[{"type":"addDirectories","directories":["work"],"destination":"userSettings"}]}}`)
	var frame struct {
		Type     string
		Response struct {
			ID       string `json:"request_id"`
			Subtype  string
			Response struct {
				Behavior, ToolUseID string
				UpdatedInput        json.RawMessage
				UpdatedPermissions  []struct {
					Type, Destination string
					Directories       []string
				}
			}
		}
	}
	if err := decoder.Decode(&frame); err != nil {
		t.Fatal(err)
	}
	result := frame.Response.Response
	if frame.Type != "control_response" || frame.Response.Subtype != "success" || frame.Response.ID != "native" || result.ToolUseID != "tool" || result.Behavior != "allow" ||
		string(result.UpdatedInput) != `{"n":9007199254740993}` || len(result.UpdatedPermissions) != 1 || result.UpdatedPermissions[0].Destination != "session" || !slices.Equal(result.UpdatedPermissions[0].Directories, []string{"work"}) || !<-delivered {
		t.Fatal(frame)
	}
	write(`{"type":"result","session_id":"root","result":"ok"}`)
	if err := decoder.Decode(&frame); err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
	provider.Close()
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
}

func TestClaudeRetiredToolRequestCannotApproveReusedID(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	settled := make(chan bool, 1)
	host := newClaudePromptHost(t.Context(), func(ctx context.Context, p acpPermissionParams) acpPermissionOutcome {
		close(started)
		<-ctx.Done()
		close(cancelled)
		p.receipt.publish = func(ok bool) { settled <- ok }
		return acpPermissionOutcome{Outcome: "selected", OptionID: "allow"}
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
	host.mode.observe("init", "acceptEdits")
	host.mode.observe("status", "plan")
	request := claudeToolRequest{Subtype: "can_use_tool", Name: "ExitPlanMode", ToolID: "old", Input: json.RawMessage(`{"plan":"old plan"}`)}
	if err := c.dispatch("id", "root", request); err != nil {
		t.Fatal(err)
	}
	<-started
	c.host.withdraw("id")
	<-cancelled
	request.Name, request.ToolID = "AskUserQuestion", "new"
	if err := c.dispatch("id", "root", request); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&raw); err != nil || !strings.Contains(string(raw), `"toolUseID":"new"`) || !strings.Contains(string(raw), `"behavior":"deny"`) || <-settled {
		t.Fatal(string(raw), err)
	}
}

func TestClaudeExitPlanModeReviewAndUnadvertisedGrant(t *testing.T) {
	for _, tc := range []struct {
		name, choice, behavior, message string
		suppress, interactive           bool
	}{
		{"ExitPlanMode", "deny", "deny", "keep planning", false, true},
		{"ExitPlanMode", "allow", "allow", "", false, true},
		{"Write", "allowForSession", "deny", "denied", true, false},
		{"PrivateDialog", "allow", "deny", "dedicated interaction", false, true},
	} {
		request := claudeToolRequest{Name: tc.name, ToolID: "tool", Input: json.RawMessage(`{"plan":"Review step one"}`), RequiresInteraction: tc.interactive, SuppressRule: tc.suppress,
			Suggestions: []map[string]json.RawMessage{{"type": json.RawMessage(`"setMode"`)}}}
		result := request.decide(t.Context(), "root", func(_ context.Context, p acpPermissionParams) acpPermissionOutcome {
			if tc.name == "PrivateDialog" {
				t.Fatal("unsafe one-click approval")
			}
			if len(p.Options) != 2 || !strings.Contains(string(p.ToolCall), "Review step one") {
				t.Fatal(p.Options, string(p.ToolCall))
			}
			return acpPermissionOutcome{Outcome: "selected", OptionID: tc.choice}
		}, new(permissionReceipt))
		if result["behavior"] != tc.behavior || tc.message != "" && !strings.Contains(result["message"].(string), tc.message) || result["updatedPermissions"] != nil {
			t.Fatal(result)
		}
	}
}

func TestClaudeWithdrawsPendingCardOnNativeCancelOrTurnEnd(t *testing.T) {
	for _, terminal := range []string{"control_cancel_request", "result", "disconnect"} {
		t.Run(terminal, func(t *testing.T) {
			started, retired := make(chan struct{}), make(chan struct{})
			host := newClaudePromptHost(t.Context(), func(ctx context.Context, _ acpPermissionParams) acpPermissionOutcome {
				close(started)
				<-ctx.Done()
				close(retired)
				return acpPermissionOutcome{Outcome: "cancelled"}
			})
			in, c, err := openClaudeControl(strings.NewReader(""), nil, nil, host)
			if err != nil {
				t.Fatal(err)
			}
			defer in.Close()
			defer c.finish()
			_ = in.SetReadDeadline(time.Now().Add(3 * time.Second))
			decoder := json.NewDecoder(in)
			var frame json.RawMessage
			if err := decoder.Decode(&frame); err != nil {
				t.Fatal(err)
			}
			out, provider := io.Pipe()
			defer out.Close()
			defer provider.Close()
			done := make(chan error, 1)
			go func() {
				_, _, err := readClaudeContextStream(out, func(Activity) {}, "root", nil, c)
				done <- err
			}()
			write := func(line string) {
				t.Helper()
				if _, err := io.WriteString(provider, line+"\n"); err != nil {
					t.Fatal(err)
				}
			}
			write(`{"type":"control_request","request_id":"ask","request":{"subtype":"can_use_tool","tool_name":"Read","tool_use_id":"tool","input":{}}}`)
			<-started
			switch terminal {
			case "control_cancel_request":
				write(`{"type":"control_cancel_request","request_id":"ask"}`)
				<-retired
				write(`{"type":"result","session_id":"root","result":"ok"}`)
			case "result":
				write(`{"type":"result","session_id":"root","result":"ok"}`)
				<-retired
			}
			provider.Close()
			if err := <-done; (err != nil) != (terminal == "disconnect") {
				t.Fatal(err)
			}
			<-retired
			if err := decoder.Decode(&frame); err != io.EOF {
				t.Fatalf("unexpected native response %s %v", frame, err)
			}
		})
	}
}
