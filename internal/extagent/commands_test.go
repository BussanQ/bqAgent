package extagent

import (
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestNativeCLICommandReportsKeepActualInvocationAndHints(t *testing.T) {
	for _, tc := range []struct {
		name  AgentName
		frame string
		want  []NativeCommand
	}{
		{AgentPi, `{"type":"response","id":"commands","command":"get_commands","success":true,"data":{"commands":[{"name":"skill:review","description":"Pi skill","source":"skill"},{"name":"vendor:test","source":"extension"},{"name":"bad token"},{"name":"skill:review","description":"duplicate"}]}}`, []NativeCommand{{Name: "skill:review", Text: "/skill:review", Description: "Pi skill", Kind: "skill"}, {Name: "vendor:test", Text: "/vendor:test", Kind: "extension"}}},
		{AgentClaude, `{"type":"control_response","response":{"subtype":"success","request_id":"commands","response":{"commands":[{"name":"audit","description":"Check code","argumentHint":"[path]"}]}}}`, []NativeCommand{{Name: "audit", Text: "/audit", Description: "Check code", Hint: "[path]"}}},
	} {
		got, found, err := parseCLICommandReply(tc.name, []byte(tc.frame))
		if err != nil || !found || !slices.Equal(got, tc.want) {
			t.Fatal(tc.name, got, found, err)
		}
		if _, found, _ := parseCLICommandReply(tc.name, []byte(strings.Replace(tc.frame, `"commands"`, `"other"`, 1))); found {
			t.Fatal("accepted foreign response")
		}
	}
	_, found, err := parseCLICommandReply(AgentPi, []byte(`{"type":"response","id":"commands","command":"get_commands","success":false,"error":"not supported"}`))
	if !found || err == nil {
		t.Fatal(found, err)
	}
}

func TestACPCommandReportReplacesSessionCatalog(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c := &stdioACPClient{}
	report := func(id, entries string) {
		c.handleSessionUpdate(json.RawMessage(`{"sessionId":"` + id + `","update":{"sessionUpdate":"available_commands_update","availableCommands":` + entries + `}}`))
	}
	report("other", `[{"name":"unrelated"}]`)
	done := make(chan []NativeCommand, 1)
	go func() { rows, _ := c.AvailableCommands(ctx, "current"); done <- rows }()
	report("current", `[{"name":"review","description":"Read changes","input":{"hint":"[file]"}},{"name":"explain"}]`)
	got := <-done
	if len(got) != 2 || got[0].Text != "/review" || got[0].Hint != "[file]" {
		t.Fatal(got)
	}
	got[0].Text = "changed"
	next, _ := c.AvailableCommands(ctx, "current")
	if next[0].Text != "/review" {
		t.Fatal("reader changed stored report")
	}
	report("current", "[]")
	got, _ = c.AvailableCommands(ctx, "current")
	if len(got) != 0 {
		t.Fatal(got)
	}
	got, _ = c.AvailableCommands(ctx, "other")
	if len(got) != 1 || got[0].Name != "unrelated" {
		t.Fatal(got)
	}
	cancel()
	if _, err := c.AvailableCommands(ctx, "missing"); err == nil {
		t.Fatal("catalog read ignored cancellation")
	}
}

func TestCodexSkillCompletionUsesExecutingWorkspaceReport(t *testing.T) {
	const catalog = `{"data":[{"cwd":"other","skills":[{"name":"audit","path":"/other"}]},{"cwd":"repo","skills":[{"name":"audit","path":"/disabled","enabled":false},{"name":"audit","path":"/live","interface":{"shortDescription":"Inspect changes"}},{"name":"ignored"},{"name":"audit","path":"/shadow"}]}]}`
	for _, tc := range []struct {
		prompt    string
		wantCalls []string
		skill     bool
	}{
		{"$audit source", []string{"skills/list", "turn/start"}, true},
		{"$missing arg", []string{"skills/list", "turn/start"}, false},
		{"explain error", []string{"turn/start"}, false},
	} {
		c := &codexAppServerClient{cmd: &exec.Cmd{Dir: "repo"}, responses: map[string]chan rpcEnvelope{}, done: make(chan struct{})}
		var methods []string
		c.stdin = modelWire(func(msg rpcEnvelope) {
			methods = append(methods, msg.Method)
			switch msg.Method {
			case "skills/list":
				var p struct{ Cwds []string }
				if json.Unmarshal(msg.Params, &p) != nil || !slices.Equal(p.Cwds, []string{"repo"}) {
					t.Fatalf("params=%s", msg.Params)
				}
				c.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(catalog)}
			case "turn/start":
				var p struct {
					Input []struct{ Type, Name, Path, Text string }
				}
				if json.Unmarshal(msg.Params, &p) != nil || len(p.Input) == 0 || p.Input[0].Text != tc.prompt {
					t.Fatal(string(msg.Params))
				}
				if tc.skill && (len(p.Input) != 2 || p.Input[1].Type != "skill" || p.Input[1].Name != "audit" || p.Input[1].Path != "/live") {
					t.Fatal(p)
				}
				if !tc.skill && len(p.Input) != 1 {
					t.Fatal(p)
				}
				c.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(`{"turn":{"id":"t"}}`)}
				c.notification(rpcEnvelope{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"chat","turn":{"id":"t","status":"completed","items":[{"type":"agentMessage","id":"m","text":"ok"}]}}`)})
			default:
				t.Fatal(msg.Method)
			}
		})
		reply, err := c.Prompt(context.Background(), "chat", tc.prompt)
		if err != nil || reply != "ok" || !slices.Equal(methods, tc.wantCalls) {
			t.Fatal(reply, err, methods)
		}
	}
}

func TestPiExtensionCommandSettlesWithoutModelTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	w := newPiTestWire()
	done := make(chan error, 1)
	go func() {
		res, err := w.rpc().prompt(ctx, SessionState{ExternalSessionID: "native"}, "/vendor:test arg", nil, nil)
		if err == nil && res.Reply != "Pi 已处理该命令。" {
			t.Errorf("reply=%q", res.Reply)
		}
		done <- err
	}()
	w.reply(w.next(t, "get_state"), `{"sessionId":"native"}`)
	p := w.next(t, "prompt")
	if p["message"] != "/vendor:test arg" {
		t.Fatal(p)
	}
	w.reply(p, `{"agentInvoked":false}`)
	w.reply(w.next(t, "get_state"), `{"sessionId":"native","isStreaming":false}`)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type catalogInput struct {
	strings.Builder
	closed bool
}

func (w *catalogInput) Close() error { w.closed = true; return nil }

func TestCatalogProbeCancelsPiStartupDialogsAndKeepsWorkspaceSettings(t *testing.T) {
	spec := &AgentTransport{Agent: AgentPi, Command: CommandSpec{Args: []string{"wrapper", "-c", "--", "pi", "--session", "private", "--mode=json", "--fork=old", "-r", "--provider", "local", "--extension", "plugin.js"}}}
	got := catalogCLIArgs(spec)
	want := []string{"wrapper", "-c", "--", "pi", "--provider", "local", "--extension", "plugin.js", "--mode", "rpc", "--no-session"}
	if !slices.Equal(got, want) || !slices.Contains(spec.Command.Args, "--fork=old") {
		t.Fatal(got, spec)
	}
	var input catalogInput
	frames := strings.Join([]string{
		`{"type":"extension_ui_request","id":"status","method":"setStatus"}`,
		`{"type":"extension_ui_request","id":"ask","method":"confirm"}`,
		`{"type":"response","id":"commands","command":"get_commands","success":true,"data":{"commands":[{"name":"actual"}]}}`,
	}, "\n")
	var names []NativeCommand
	control := `{"type":"get_commands","id":"commands"}`
	err := readCatalogControl(context.Background(), AgentPi, control, &input, strings.NewReader(frames), func(line []byte) (bool, error) {
		rows, found, err := parseCLICommandReply(AgentPi, line)
		names = append(names, rows...)
		return found, err
	})
	var reply map[string]any
	lines := strings.Split(strings.TrimSpace(input.String()), "\n")
	if err != nil || len(names) != 1 || names[0].Name != "actual" || input.closed || len(lines) != 2 || lines[0] != control || json.Unmarshal([]byte(lines[1]), &reply) != nil ||
		reply["type"] != "extension_ui_response" || reply["id"] != "ask" || reply["cancelled"] != true {
		t.Fatal(err, names, lines)
	}
}

func TestCatalogModelReaderKeepsPiStateIfListArrivesFirst(t *testing.T) {
	var input catalogInput
	var menu ModelCatalog
	frames := strings.Join([]string{
		`{"type":"response","command":"get_available_models","id":"models","success":true,"data":{"models":[{"provider":"p","id":"a"}]}}`,
		`{"type":"response","command":"get_state","id":"state","success":true,"data":{"model":{"provider":"p","id":"a"},"thinkingLevel":"high"}}`,
	}, "\n")
	err := readCLIModels(context.Background(), AgentPi, "requests", &input, io.NopCloser(strings.NewReader(frames)), &menu)
	if err != nil || menu.DefaultModel != "p/a" || menu.SelectedEffort != "high" || len(menu.Models) != 1 {
		t.Fatal(menu, err)
	}
}

func TestPiModelCatalogEchoesNumericStartupDialogBeforeListingModels(t *testing.T) {
	var input catalogInput
	var menu ModelCatalog
	frames := strings.Join([]string{
		`{"type":"extension_ui_request","method":"confirm","id":9007199254740993}`,
		`{"type":"extension_ui_request","method":"input","id":"9007199254740993"}`,
		`{"type":"response","command":"get_state","id":"state","success":true,"data":{}}`,
		`{"type":"response","command":"get_available_models","id":"models","success":true,"data":{"models":[{"provider":"local","id":"second"}]}}`,
	}, "\n")
	err := readCLIModels(t.Context(), AgentPi, "requests", &input, io.NopCloser(strings.NewReader(frames)), &menu)
	if err != nil || len(menu.Models) != 1 || menu.Models[0].ID != "local/second" {
		t.Fatalf("model catalog: %+v, %v", menu, err)
	}
	lines := strings.Split(strings.TrimSpace(input.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("dialog responses: %q", lines)
	}
	for i, want := range []string{`9007199254740993`, `"9007199254740993"`} {
		var reply struct {
			Type      string
			ID        json.RawMessage
			Cancelled bool
		}
		if err := json.Unmarshal([]byte(lines[i+1]), &reply); err != nil || reply.Type != "extension_ui_response" || string(reply.ID) != want || !reply.Cancelled {
			t.Fatalf("dialog response: %s, %v", lines[i+1], err)
		}
	}
}

func TestPiModelListDoesNotWaitForMissingDefault(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reader, writer := io.Pipe()
		defer writer.Close()
		defer reader.Close()
		go func() {
			_, _ = io.WriteString(writer, `{"type":"response","command":"get_available_models","id":"models","success":true,"data":{"models":[{"provider":"p","id":"a"}]}}`+"\n")
		}()
		timer := time.AfterFunc(5*time.Second, func() { writer.CloseWithError(context.DeadlineExceeded) })
		defer timer.Stop()
		var input catalogInput
		var menu ModelCatalog
		err := readCLIModels(t.Context(), AgentPi, "requests", &input, reader, &menu)
		if err != nil || len(menu.Models) != 1 || menu.Models[0].ID != "p/a" || menu.DefaultModel != "" {
			t.Fatalf("model list unavailable without optional state: %+v %v", menu, err)
		}
	})
}

type commandProbeACP struct {
	reconnectingACPClient
	resumable bool
	loaded    string
	reads     int
}

func (c *commandProbeACP) LoadSessionSupported() bool { return c.resumable }
func (c *commandProbeACP) LoadSession(context.Context, string, string) (string, error) {
	return c.loaded, nil
}
func (c *commandProbeACP) AvailableCommands(_ context.Context, id string) ([]NativeCommand, error) {
	c.reads++
	return []NativeCommand{{Name: id}}, nil
}
func TestACPCatalogNeverSubstitutesAnotherSession(t *testing.T) {
	for _, tc := range []struct {
		resumable bool
		id        string
		ok        bool
	}{{false, "", false}, {true, "other", false}, {true, "native", true}} {
		client := &commandProbeACP{resumable: tc.resumable, loaded: tc.id}
		b := NewBroker(nil, nil, func(context.Context, CommandSpec, string) (ACPClient, error) { return client, nil })
		got, err := b.acpCommands(context.Background(), &AgentTransport{Agent: AgentOpenCode}, nil, "native", "workspace")
		b.Close()
		if tc.ok {
			if err != nil || len(got) != 1 || got[0].Name != "native" {
				t.Fatal(got, err)
			}
		} else if err == nil || client.reads != 0 {
			t.Fatal(tc, client.reads, err)
		}
		if client.closeCount != 1 {
			t.Fatal("probe leaked", client.closeCount)
		}
	}
}
