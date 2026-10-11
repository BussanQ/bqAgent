package extagent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func appServerSpec(t *testing.T, mode string) (CommandSpec, string) {
	t.Helper()
	log := filepath.Join(t.TempDir(), "requests.jsonl")
	return CommandSpec{Command: os.Args[0], Args: []string{"-test.run=^TestAppServerHelperProcess$", "--", mode, log}}, log
}

func TestAppServerHelperProcess(t *testing.T) {
	if len(os.Args) < 5 || os.Args[2] != "--" {
		return
	}
	mode, logPath := os.Args[3], os.Args[4]
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(2)
	}
	defer log.Close()
	encoder := json.NewEncoder(os.Stdout)
	send := func(value any) {
		if encoder.Encode(value) != nil {
			os.Exit(3)
		}
	}
	respond := func(id json.RawMessage, value any) { send(map[string]any{"id": id, "result": value}) }
	notify := func(method string, params any) { send(map[string]any{"method": method, "params": params}) }
	threadID, turnID := "", ""
	turnCount, threadCount := 0, 0
	initialized := false
	finish := func(status, reply string) {
		notify("item/completed", map[string]any{"threadId": threadID, "turnId": turnID, "item": map[string]any{"id": "answer", "type": "agentMessage", "phase": "final_answer", "text": reply}})
		notify("turn/completed", map[string]any{"threadId": threadID, "turn": map[string]any{"id": turnID, "status": status, "error": map[string]any{"message": "test failure"}}})
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 65536), 16<<20)
	for scanner.Scan() {
		_, _ = log.Write(append(append([]byte{}, scanner.Bytes()...), '\n'))
		var env rpcEnvelope
		if json.Unmarshal(scanner.Bytes(), &env) != nil {
			os.Exit(4)
		}
		if env.Method == "" {
			finish("completed", string(env.Result))
			continue
		}
		if env.Method != "initialize" && env.Method != "initialized" && !initialized {
			os.Exit(5)
		}
		switch env.Method {
		case "initialize":
			if mode == "init-fail" {
				send(map[string]any{"id": env.ID, "error": map[string]any{"code": -1, "message": "init failed"}})
				continue
			}
			respond(env.ID, map[string]any{"userAgent": "fake-codex"})
		case "initialized":
			initialized = true
		case "thread/start":
			threadCount++
			threadID = fmt.Sprintf("thread-%d-%d", os.Getpid(), threadCount)
			respond(env.ID, map[string]any{"thread": map[string]string{"id": threadID}})
		case "thread/resume":
			var params struct {
				ThreadID string `json:"threadId"`
			}
			_ = json.Unmarshal(env.Params, &params)
			threadID = params.ThreadID
			respond(env.ID, map[string]any{"thread": map[string]string{"id": threadID}})
		case "turn/start":
			turnCount++
			turnID = fmt.Sprintf("turn-%d", turnCount)
			if mode == "disconnect" {
				os.Exit(0)
			}
			if mode == "early" {
				finish("completed", "early final")
				respond(env.ID, map[string]any{"turn": map[string]string{"id": turnID, "status": "inProgress"}})
				continue
			}
			respond(env.ID, map[string]any{"turn": map[string]string{"id": turnID, "status": "inProgress"}})
			if mode == "wait" {
				continue
			}
			if strings.HasPrefix(mode, "approval") {
				method := "item/commandExecution/requestApproval"
				if mode == "approval-file" {
					method = "item/fileChange/requestApproval"
				}
				if mode == "approval-permissions" {
					method = "item/permissions/requestApproval"
				}
				send(map[string]any{"id": "server-approval", "method": method, "params": map[string]any{"threadId": threadID, "turnId": turnID, "itemId": "tool-1", "command": "echo check", "availableDecisions": []string{"accept", "decline"}, "permissions": map[string]any{"network": map[string]any{"enabled": true}}}})
				continue
			}
			notify("turn/completed", map[string]any{"threadId": "other-thread", "turn": map[string]string{"id": turnID, "status": "failed"}})
			notify("turn/completed", map[string]any{"threadId": threadID, "turn": map[string]string{"id": "stale-turn", "status": "failed"}})
			notify("item/completed", map[string]any{"threadId": threadID, "turnId": turnID, "item": map[string]string{"id": "note", "type": "agentMessage", "phase": "commentary", "text": "working"}})
			notify("item/agentMessage/delta", map[string]string{"threadId": threadID, "turnId": turnID, "itemId": "answer", "delta": "partial"})
			if mode == "failed" {
				finish("failed", "not a success")
				continue
			}
			finish("completed", "authoritative final")
		case "turn/interrupt":
			respond(env.ID, map[string]any{})
			finish("interrupted", "cancelled")
		default:
			os.Exit(6)
		}
	}
	os.Exit(0)
}

func TestAppServerLifecycleAndEarlyEvents(t *testing.T) {
	for _, mode := range []string{"normal", "early"} {
		t.Run(mode, func(t *testing.T) {
			spec, log := appServerSpec(t, mode)
			client, err := NewCodexAppServerClient(spec, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := client.Initialize(ctx); err != nil {
				t.Fatal(err)
			}
			id, err := client.NewSession(ctx, "/workspace")
			if err != nil || id == "" {
				t.Fatalf("thread = %q, %v", id, err)
			}
			resumed, err := client.LoadSession(ctx, id, "/workspace")
			if err != nil || resumed != id {
				t.Fatalf("resume = %q, %v", resumed, err)
			}
			for i := 0; i < 2; i++ {
				reply, err := client.Prompt(ctx, id, "test prompt")
				want := "authoritative final"
				if mode == "early" {
					want = "early final"
				}
				if err != nil || reply != want {
					t.Fatalf("reply = %q, %v", reply, err)
				}
			}
			data, _ := os.ReadFile(log)
			if strings.Contains(string(data), "approvalPolicy") || strings.Contains(string(data), "sandbox") {
				t.Fatal("adapter overwrote user's security configuration")
			}
		})
	}
}

func TestAppServerFailedTurnsAndCancellation(t *testing.T) {
	for _, mode := range []string{"failed", "disconnect", "wait"} {
		t.Run(mode, func(t *testing.T) {
			spec, log := appServerSpec(t, mode)
			client, err := NewCodexAppServerClient(spec, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := client.Initialize(ctx); err != nil {
				t.Fatal(err)
			}
			id, err := client.NewSession(ctx, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			turnCtx := ctx
			if mode == "wait" {
				var turnCancel context.CancelFunc
				turnCtx, turnCancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer turnCancel()
			}
			if _, err := client.Prompt(turnCtx, id, "task"); err == nil {
				t.Fatal("expected turn error")
			} else if mode == "wait" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			if mode == "wait" {
				data, _ := os.ReadFile(log)
				if !strings.Contains(string(data), "turn/interrupt") {
					t.Fatal("cancel did not interrupt the turn")
				}
			}
		})
	}
}

type appServerPermissionSink struct {
	broker   *Broker
	option   string
	requests chan ACPPermissionRequest
}

func (s *appServerPermissionSink) EmitACPPermissionRequest(request ACPPermissionRequest) {
	s.requests <- request
	_ = s.broker.RespondPermission(request.RequestID, s.option)
}

func TestAppServerApprovalsReuseBrokerPermissionBridge(t *testing.T) {
	for _, mode := range []string{"approval-command", "approval-file", "approval-permissions"} {
		for _, approve := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/%t", mode, approve), func(t *testing.T) {
				spec, _ := appServerSpec(t, mode)
				root := t.TempDir()
				broker := NewBroker(NewStateStore(root), appServerDetections(spec), nil)
				defer broker.Close()
				request := TurnRequest{BQSessionID: "group", Agent: AgentCodex, CWD: root, Prompt: "task"}
				sink := &appServerPermissionSink{broker: broker, option: "accept", requests: make(chan ACPPermissionRequest, 1)}
				if approve {
					request.PermissionSink = sink
				}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				response, err := broker.SendGroupTurn(ctx, request)
				if err != nil {
					t.Fatal(err)
				}
				want := `"decision":"cancel"`
				if approve {
					want = `"decision":"accept"`
					if len(sink.requests) != 1 || (<-sink.requests).Agent != AgentCodex {
						t.Fatal("missing Codex approval")
					}
				}
				if mode == "approval-permissions" {
					want = `"permissions":{}`
					if approve {
						want = `"enabled":true`
					}
				}
				if !strings.Contains(response.Reply, want) {
					t.Fatalf("approval result = %s, want %s", response.Reply, want)
				}
			})
		}
	}
}

func appServerDetections(spec CommandSpec) map[AgentName]DetectionResult {
	transport := &AgentTransport{Agent: AgentCodex, Kind: TransportAppServer, Command: spec}
	return map[AgentName]DetectionResult{AgentCodex: {Agent: AgentCodex, Preferred: transport, AppServer: transport}}
}

func TestAppServerDetectionOptInAndFallback(t *testing.T) {
	config := ConfigFromEnv(func(string) string { return "" }, t.TempDir())
	if !config.Agents[AgentCodex].AutoAppServer || config.Agents[AgentCodex].CLI.Command != "codex" {
		t.Fatal("expected automatic App Server with legacy CLI fallback")
	}
	configured := ConfigFromEnv(func(key string) string {
		if key == "AGENT_CODEX_APP_SERVER_CMD" {
			return "custom-codex"
		}
		return ""
	}, t.TempDir()).Agents[AgentCodex]
	if configured.AppServer.Command != "custom-codex" || strings.Join(configured.AppServer.Args, " ") != "app-server" {
		t.Fatalf("config = %+v", configured)
	}
	for _, mode := range []string{"normal", "init-fail"} {
		spec, log := appServerSpec(t, mode)
		config.Agents = map[AgentName]AgentConfig{AgentCodex: {AppServer: spec, CLI: helperSpec(t, "cli-codex")}}
		result := Detect(context.Background(), config, nil)[AgentCodex]
		want := TransportAppServer
		if mode == "init-fail" {
			want = TransportCLI
		}
		if result.Preferred == nil || result.Preferred.Kind != want || result.CLIFallback != (mode == "init-fail") {
			t.Fatalf("detection = %+v", result)
		}
		data, _ := os.ReadFile(log)
		if strings.Contains(string(data), "thread/start") || strings.Contains(string(data), "turn/start") {
			t.Fatal("detection ran a model task")
		}
	}
}

func TestAppServerBrokerResumesCLIThreadAndKeepsFailedThread(t *testing.T) {
	root := t.TempDir()
	store := NewStateStore(root)
	if err := store.SaveGroup(SessionState{BQSessionID: "group", Agent: AgentCodex, Transport: TransportCLI, ExternalSessionID: "existing-cli-thread"}); err != nil {
		t.Fatal(err)
	}
	spec, log := appServerSpec(t, "normal")
	broker := NewBroker(store, appServerDetections(spec), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, err := broker.SendGroupTurn(ctx, TurnRequest{BQSessionID: "group", Agent: AgentCodex, CWD: root, Prompt: "continue"})
	if err != nil || response.State.ExternalSessionID != "existing-cli-thread" || response.State.Transport != TransportAppServer {
		t.Fatalf("response = %+v, %v", response, err)
	}
	_ = broker.Close()
	data, _ := os.ReadFile(log)
	if strings.Contains(string(data), "thread/start") || !strings.Contains(string(data), "thread/resume") {
		t.Fatal("CLI history was not resumed")
	}
	spec, log = appServerSpec(t, "disconnect")
	broker = NewBroker(store, appServerDetections(spec), nil)
	defer broker.Close()
	_, err = broker.SendGroupTurn(ctx, TurnRequest{BQSessionID: "failed-group", Agent: AgentCodex, CWD: root, Prompt: "change files"})
	if err == nil {
		t.Fatal("lost connection reported success")
	}
	state, err := store.LoadGroup("failed-group", AgentCodex)
	if err != nil || state.ExternalSessionID == "" {
		t.Fatalf("failed thread was not saved: %+v, %v", state, err)
	}
	data, _ = os.ReadFile(log)
	if strings.Count(string(data), `"method":"turn/start"`) != 1 {
		t.Fatalf("turn was replayed: %s", data)
	}
}

func TestAppServerOptInPreservesExistingACPSessions(t *testing.T) {
	root := t.TempDir()
	store := NewStateStore(root)
	if err := store.Save(SessionState{BQSessionID: "old-acp", Agent: AgentCodex, Transport: TransportACP, ExternalSessionID: "old-acp-session"}); err != nil {
		t.Fatal(err)
	}
	spec, log := appServerSpec(t, "normal")
	detections := appServerDetections(spec)
	detection := detections[AgentCodex]
	detection.ACP = &AgentTransport{Agent: AgentCodex, Kind: TransportACP, Command: helperSpec(t, "acp-good")}
	detections[AgentCodex] = detection
	broker := NewBroker(store, detections, nil)
	defer broker.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	response, err := broker.SendTurn(ctx, TurnRequest{BQSessionID: "old-acp", Agent: AgentCodex, CWD: root, Prompt: "continue"})
	if err != nil || response.State.Transport != TransportACP {
		t.Fatalf("legacy ACP changed: %+v, %v", response, err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("App Server started for an existing ACP session")
	}
}

type waitingAppServerPermissionSink struct{ requests chan ACPPermissionRequest }

func (s *waitingAppServerPermissionSink) EmitACPPermissionRequest(request ACPPermissionRequest) {
	s.requests <- request
}

func TestAppServerCancellationReleasesPendingApproval(t *testing.T) {
	root := t.TempDir()
	spec, _ := appServerSpec(t, "approval-command")
	broker := NewBroker(NewStateStore(root), appServerDetections(spec), nil)
	defer broker.Close()
	sink := &waitingAppServerPermissionSink{requests: make(chan ACPPermissionRequest, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := broker.SendGroupTurn(ctx, TurnRequest{BQSessionID: "group", Agent: AgentCodex, CWD: root, Prompt: "task", PermissionSink: sink})
		done <- err
	}()
	var permission ACPPermissionRequest
	select {
	case permission = <-sink.requests:
	case <-time.After(3 * time.Second):
		t.Fatal("approval did not arrive")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel blocked on approval")
	}
	if err := broker.RespondPermission(permission.RequestID, "accept"); !errors.Is(err, ErrPermissionNotFound) {
		t.Fatalf("stale permission survived: %v", err)
	}
}
