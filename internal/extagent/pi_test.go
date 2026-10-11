package extagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPiConfigDetectionAndRoute(t *testing.T) {
	config := ConfigFromEnv(func(string) string { return "" }, t.TempDir())
	pi := config.Agents[AgentPi]
	if pi.CLI.Command != "pi" || !reflect.DeepEqual(pi.CLI.Args, []string{"--mode", "json"}) || pi.ACP.Command != "" {
		t.Fatalf("Pi defaults = %+v", pi)
	}
	custom := ConfigFromEnv(func(key string) string {
		return map[string]string{"AGENT_PI_CLI_CMD": os.Args[0], "AGENT_PI_CLI_ARGS": "--mode json --model test-model"}[key]
	}, t.TempDir()).Agents[AgentPi]
	if custom.CLI.Command != os.Args[0] || !reflect.DeepEqual(custom.CLI.Args, []string{"--mode", "json", "--model", "test-model"}) {
		t.Fatalf("Pi overrides = %+v", custom)
	}
	config.Agents = map[AgentName]AgentConfig{AgentPi: custom}
	result := Detect(context.Background(), config, nil)[AgentPi]
	if result.Preferred == nil || result.Preferred.Kind != TransportCLI {
		t.Fatalf("Pi detection = %+v", result)
	}
	for _, name := range SupportedAgents() {
		agent, prompt, explicit, err := ParseRoute("/" + strings.ToUpper(string(name)) + " inspect\nrepo")
		if err != nil || !explicit || agent != name || prompt != "inspect\nrepo" || !IsRouteCommand("/"+string(name)) {
			t.Fatalf("route %s = %s, %q, %v, %v", name, agent, prompt, explicit, err)
		}
	}
	if _, _, explicit, err := ParseRoute("/pi"); !explicit || err == nil {
		t.Fatal("empty Pi route was accepted")
	}
}

func TestParsePiJSONUsesFinalAssistantAndReportsFailures(t *testing.T) {
	header := `{"type":"session","id":"pi-session-1"}` + "\n"
	answer := `{"type":"message_end","message":{"role":"assistant","stopReason":"stop","content":[{"type":"thinking","thinking":"private"},{"type":"text","text":"answer"}]}}` + "\n"
	errorMessage := `{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"quota exceeded","content":[]}}` + "\n"
	for _, tt := range []struct{ name, events, want, wantErr string }{
		{"final only", header + `{"type":"message_end","message":{"role":"user","content":"question"}}
{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"duplicate"}}
{"type":"message_end","message":{"role":"assistant","stopReason":"toolUse","content":[{"type":"text","text":"working"}]}}
{"type":"message_end","message":{"role":"toolResult","content":[{"type":"text","text":"tool output"}]}}
` + answer + `{"type":"agent_end","messages":[]}`, "answer", ""},
		{"retry succeeds", header + errorMessage + answer, "answer", ""},
		{"unrelated event", header + `{"type":"custom_event","message":"status"}` + "\n" + answer, "answer", ""},
		{"error after text", header + answer + errorMessage, "", "quota exceeded"},
		{"aborted", header + answer + `{"type":"agent_settled","aborted":true}`, "", "aborted"},
		{"missing header", answer, "", "session ID"},
		{"empty", header + `{"type":"message_end","message":{"role":"assistant","content":[]}}`, "", "empty final"},
		{"tool preamble", header + `{"type":"message_end","message":{"role":"assistant","stopReason":"toolUse","content":[{"type":"text","text":"working"}]}}`, "", "empty final"},
		{"broken JSON", header + "{", "", "invalid pi JSON"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reply, id, err := parsePiJSON([]byte(tt.events))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || reply != tt.want || id != "pi-session-1" {
				t.Fatalf("reply = %q, id = %q, err = %v", reply, id, err)
			}
		})
	}
	longReply := strings.Repeat("x", 128<<10) + "\u2028end"
	encoded, _ := json.Marshal(longReply)
	data := header + `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":` + string(encoded) + `}]}}`
	if reply, _, err := parsePiJSON([]byte(data)); err != nil || reply != longReply {
		t.Fatalf("long record truncated: len=%d, err=%v", len(reply), err)
	}
}

func piHelperSpec() CommandSpec {
	return CommandSpec{Command: os.Args[0], Args: []string{"-test.run=^TestPiCLIHelperProcess$", "--", "--mode", "json"}}
}

func TestPiCLIHelperProcess(t *testing.T) {
	if len(os.Args) < 3 || os.Args[2] != "--" {
		return
	}
	prompt, _ := io.ReadAll(os.Stdin)
	if string(prompt) == "fail" {
		fmt.Fprintln(os.Stderr, "pi provider unavailable")
		os.Exit(1)
	}
	if string(prompt) == "wait" {
		time.Sleep(time.Minute)
		os.Exit(1)
	}
	resumeID := ""
	for i := 3; i+1 < len(os.Args); i++ {
		if os.Args[i] == "--session" {
			resumeID = os.Args[i+1]
		}
	}
	id := resumeID
	if id == "" {
		id = fmt.Sprintf("pi-session-%d", os.Getpid())
	}
	cwd, _ := os.Getwd()
	encoder := json.NewEncoder(os.Stdout)
	_ = encoder.Encode(map[string]any{"type": "session", "id": id})
	_ = encoder.Encode(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "stopReason": "stop", "content": []map[string]any{{"type": "text", "text": string(prompt) + "|cwd=" + cwd + "|resume=" + resumeID}}}})
	os.Exit(0)
}

func TestPiCLIResumesAndPreservesPromptAndCWD(t *testing.T) {
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prompt := "@pi --not-a-flag\nsecond line"
	response, err := (CLIAdapter{}).SendTurn(context.Background(), piHelperSpec(), SessionState{Agent: AgentPi, ExternalSessionID: "saved-session"}, cwd, prompt)
	if err != nil || response.State.ExternalSessionID != "saved-session" || response.Reply != prompt+"|cwd="+cwd+"|resume=saved-session" {
		t.Fatalf("response = %+v, err = %v", response, err)
	}
	_, err = (CLIAdapter{}).SendTurn(context.Background(), piHelperSpec(), SessionState{Agent: AgentPi}, cwd, "fail")
	if err == nil || !strings.Contains(err.Error(), "pi provider unavailable") {
		t.Fatalf("CLI failure = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = (CLIAdapter{}).SendTurn(ctx, piHelperSpec(), SessionState{Agent: AgentPi}, cwd, "wait")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CLI cancellation = %v", err)
	}
}

func TestPiBrokerGroupSessionsPersistAndStayIsolated(t *testing.T) {
	root := t.TempDir()
	store := NewStateStore(root)
	detections := map[AgentName]DetectionResult{AgentPi: {Agent: AgentPi, Preferred: &AgentTransport{Agent: AgentPi, Kind: TransportCLI, Command: piHelperSpec()}}}
	broker := NewBroker(store, detections, nil)
	first, err := broker.SendGroupTurn(context.Background(), TurnRequest{BQSessionID: "group-one", Agent: AgentPi, Prompt: "hello", CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	_ = broker.Close()
	broker = NewBroker(NewStateStore(root), detections, nil)
	defer broker.Close()
	second, err := broker.SendGroupTurn(context.Background(), TurnRequest{BQSessionID: "group-one", Agent: AgentPi, Prompt: "continue", CWD: root})
	if err != nil || second.State.ExternalSessionID != first.State.ExternalSessionID || !strings.HasSuffix(second.Reply, "|resume="+first.State.ExternalSessionID) {
		t.Fatalf("resume = %+v, err = %v", second, err)
	}
	other, err := broker.SendGroupTurn(context.Background(), TurnRequest{BQSessionID: "group-two", Agent: AgentPi, Prompt: "new", CWD: root})
	if err != nil || other.State.ExternalSessionID == first.State.ExternalSessionID {
		t.Fatalf("group isolation = %+v, err = %v", other, err)
	}
	ordinary, err := broker.SendTurn(context.Background(), TurnRequest{BQSessionID: "group-one", Agent: AgentPi, Prompt: "ordinary", CWD: root})
	if err != nil || ordinary.State.ExternalSessionID == first.State.ExternalSessionID {
		t.Fatalf("ordinary/group isolation = %+v, err = %v", ordinary, err)
	}
	if err := broker.ClearGroup("group-one"); err != nil {
		t.Fatal(err)
	}
	state, err := store.LoadGroup("group-one", AgentPi)
	if err != nil || state.ExternalSessionID != "" {
		t.Fatalf("cleared group = %+v, err = %v", state, err)
	}
	state, err = store.LoadGroup("group-two", AgentPi)
	if err != nil || state.ExternalSessionID != other.State.ExternalSessionID {
		t.Fatalf("other group affected by clear: %+v, %v", state, err)
	}
}
