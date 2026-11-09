package extagent

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func TestClaudePlanApprovalRestoresOnlyTheOwningRootsModeBeforeAllow(t *testing.T) {
	for _, tc := range []struct {
		name, opened, status, launch, choice, parent, worker, want string
	}{
		{"existing execution policy", "acceptEdits", "plan", "default", "allow", "", "", "acceptEdits"},
		{"confirmed bypass", "bypassPermissions", "plan", "", "allow", "", "", "bypassPermissions"},
		{"starts in plan", "plan", "", "plan", "allow", "", "", "default"},
		{"unconfirmed elevated flag", "plan", "", "bypassPermissions", "allow", "", "", "default"},
		{"explicit no-prompt policy", "plan", "", "dontAsk", "allow", "", "", "dontAsk"},
		{"provider downgraded launch", "default", "plan", "bypassPermissions", "allow", "", "", "default"},
		{"keep planning", "acceptEdits", "plan", "", "deny", "", "", ""},
		{"already left plan", "plan", "default", "", "allow", "", "", ""},
		{"subagent actor", "acceptEdits", "plan", "", "allow", "", "child", ""},
		{"nested tool", "acceptEdits", "plan", "", "allow", "parent-tool", "", ""},
		{"unknown native policy", "new-mode", "plan", "", "allow", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := newClaudePromptHost(t.Context(), func(_ context.Context, p acpPermissionParams) acpPermissionOutcome {
				if len(p.Options) != 2 || !strings.Contains(string(p.ToolCall), "计划文件：plans/review.md") {
					t.Errorf("unexpected plan choices: %+v", p.Options)
				}
				return acpPermissionOutcome{Outcome: "selected", OptionID: tc.choice}
			})
			host.mode.launch = claudeLaunchMode([]string{"--permission-mode=ignore-wrapper", "--", "claude", "--permission-mode", tc.launch})
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
			done := make(chan error, 1)
			go func() {
				_, _, err := readClaudeContextStream(out, func(Activity) {}, "root", nil, c)
				done <- err
			}()
			write := func(frame any) {
				t.Helper()
				if err := json.NewEncoder(provider).Encode(frame); err != nil {
					t.Fatal(err)
				}
			}
			write(map[string]string{"type": "system", "subtype": "init", "session_id": "root", "permissionMode": tc.opened})
			if tc.status != "" {
				write(map[string]string{"type": "system", "subtype": "status", "session_id": "root", "permissionMode": tc.status})
			}
			write(map[string]string{"type": "system", "subtype": "status", "session_id": "foreign", "permissionMode": "auto"})
			write(map[string]string{"type": "system", "subtype": "status", "session_id": "root", "parent_tool_use_id": "parent", "permissionMode": "default"})
			write(map[string]any{"type": "system", "subtype": "status", "session_id": "root", "isReplay": true, "permissionMode": "dontAsk"})
			write(map[string]any{"type": "control_request", "request_id": "approval", "parent_tool_use_id": tc.parent, "request": map[string]any{"subtype": "can_use_tool", "tool_name": "ExitPlanMode", "tool_use_id": "plan", "agent_id": tc.worker, "input": map[string]string{"plan": "implement this", "planFilePath": "plans/review.md"}}})
			type wire struct {
				Type     string
				ID       string `json:"request_id"`
				Request  struct{ Subtype, Mode string }
				Response struct {
					ID       string `json:"request_id"`
					Response struct{ Behavior, ToolUseID string }
				}
			}
			var frame wire
			if err := decoder.Decode(&frame); err != nil {
				t.Fatal(err)
			}
			var modeID string
			if tc.want != "" {
				if frame.Type != "control_request" || frame.Request.Subtype != "set_permission_mode" || frame.Request.Mode != tc.want || frame.ID == "" || frame.ID == "approval" {
					t.Fatal(frame)
				}
				modeID = frame.ID
				frame = wire{}
				if err := decoder.Decode(&frame); err != nil {
					t.Fatal("approval waited for mode acknowledgement", err)
				}
			}
			if frame.Type != "control_response" || frame.Response.ID != "approval" || frame.Response.Response.Behavior != tc.choice || frame.Response.Response.ToolUseID != "plan" {
				t.Fatal(frame)
			}
			if modeID != "" {
				write(map[string]any{"type": "control_response", "response": map[string]string{"request_id": modeID, "subtype": "error", "error": "unsupported"}})
			}
			write(map[string]string{"type": "result", "session_id": "root", "result": "done"})
			provider.Close()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if err := decoder.Decode(&frame); err != io.EOF {
				t.Fatal("unexpected additional command", frame, err)
			}
		})
	}
}

func TestClaudePlanApprovalReadsLatestModeAfterWaitingForNativeWriter(t *testing.T) {
	chosen := make(chan struct{})
	host := newClaudePromptHost(t.Context(), func(_ context.Context, _ acpPermissionParams) acpPermissionOutcome {
		close(chosen)
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
	c.writeMu.Lock()
	err = c.dispatch("ask", "root", claudeToolRequest{Subtype: "can_use_tool", Name: "ExitPlanMode", ToolID: "plan", Input: json.RawMessage(`{"plan":"Implement it"}`)})
	if err != nil {
		c.writeMu.Unlock()
		t.Fatal(err)
	}
	<-chosen
	host.mode.observe("status", "dontAsk")
	c.writeMu.Unlock()
	if err := decoder.Decode(&raw); err != nil || !strings.Contains(string(raw), `"type":"control_response"`) || !strings.Contains(string(raw), `"behavior":"allow"`) {
		t.Fatal(string(raw), err)
	}
}
