package extagent

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func TestClaudeUsageControlRequestsOnlyMetadataAfterTheRootResult(t *testing.T) {
	var seen []ContextUsage
	emit := func(id string, u ContextUsage) {
		if id != "root" {
			t.Errorf("unexpected session %q", id)
		}
		seen = append(seen, u)
	}
	in, control, err := openClaudeUsageControl(strings.NewReader("{\"type\":\"user\",\"message\":\"one prompt\"}\n"), emit, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	defer control.finish()
	_ = in.SetReadDeadline(time.Now().Add(3 * time.Second))
	commands := json.NewDecoder(in)
	var init struct {
		Type    string
		ID      string `json:"request_id"`
		Request struct{ Subtype, Detail string }
	}
	if commands.Decode(&init) != nil || init.Request.Subtype != "initialize" {
		t.Fatal(init)
	}
	var user struct{ Type, Message string }
	if commands.Decode(&user) != nil || user.Type != "user" || user.Message != "one prompt" {
		t.Fatal(user)
	}
	stdout, provider := io.Pipe()
	defer stdout.Close()
	defer provider.Close()
	done := make(chan error, 1)
	go func() {
		reply, id, err := readClaudeContextStream(stdout, func(Activity) {}, "root", emit, control)
		if err == nil && (reply != "answer" || id != "root") {
			t.Errorf("unexpected output %q %q", reply, id)
		}
		done <- err
	}()
	write := func(data string) {
		t.Helper()
		if _, err := io.WriteString(provider, data+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"type":"control_response","response":{"subtype":"success","request_id":"` + init.ID + `"}}`)
	write(`{"type":"system","subtype":"init","session_id":"root","model":"main"}`)
	write(`{"type":"assistant","session_id":"root","message":{"model":"main","usage":{"input_tokens":12}}}`)
	write(`{"type":"result","session_id":"root","result":"answer","modelUsage":{"main":{"contextWindow":800}}}`)
	var query struct {
		Type    string
		ID      string `json:"request_id"`
		Request struct{ Subtype, Detail string }
	}
	if commands.Decode(&query) != nil || query.Type != "control_request" || query.Request.Subtype != "get_context_usage" || query.Request.Detail != "summary" {
		t.Fatal(query)
	}
	if err := commands.Decode(&user); err != io.EOF {
		t.Fatal("expected EOF after one report request", err)
	}
	write(`{"type":"control_response","response":{"subtype":"success","request_id":"` + query.ID + `","response":{"model":"main","totalTokens":20,"rawMaxTokens":100,"maxTokens":80,"percentage":25,"autoCompactThreshold":73,"isAutoCompactEnabled":true,"categories":[{"name":"tools","tokens":2,"isDeferred":true},{"name":" Messages ","tokens":18},{"name":"zero","tokens":0}]}}}`)
	provider.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || !seen[0].Estimated || seen[0].Used != 12 {
		t.Fatal(seen)
	}
	last := seen[1]
	if last.Estimated || !last.Summary || last.Used != 20 || last.Window != 100 || last.Percentage == nil || *last.Percentage != 25 || last.CompactAt == nil || *last.CompactAt != 73 ||
		len(last.Categories) != 2 || last.Categories[0].Name != "Messages" || !last.Categories[1].Deferred {
		t.Fatal(last)
	}
}

func TestClaudeUsageControlReleaseDeliversEntirePendingPrompt(t *testing.T) {
	user, w := io.Pipe()
	in, c, err := openClaudeUsageControl(user, func(string, ContextUsage) { t.Error("unexpected report") }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	defer c.finish()
	_ = in.SetReadDeadline(time.Now().Add(3 * time.Second))
	// An adapter can request input before a large image has finished writing.
	c.release()
	data := strings.Repeat("x", 200000)
	go func() { _, _ = io.WriteString(w, data); w.Close() }()
	body, err := io.ReadAll(in)
	_, prompt, ok := strings.Cut(string(body), "\n")
	if err != nil || !ok || prompt != data {
		t.Fatal(err, len(prompt))
	}
}
