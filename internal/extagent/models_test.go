package extagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestModelPickerOffersRecentNativeRowsWithOnlyCurrentChatsSettings(t *testing.T) {
	root := t.TempDir()
	store := NewStateStore(root)
	pi := &AgentTransport{Agent: AgentPi, Kind: TransportCLI}
	acp := &AgentTransport{Agent: AgentPi, Kind: TransportACP}
	broker := NewBroker(store, map[AgentName]DetectionResult{AgentPi: {Agent: AgentPi, Preferred: pi, CLI: pi, ACP: acp}}, nil)
	defer broker.Close()
	start := time.Now()
	listed := ModelCatalog{Selected: "old", SelectedEffort: "high", DefaultModel: "old", Models: []ModelOption{{"p/one", "One"}, {"p/two", "Two"}}}
	broker.rememberListing(pi, root, start, &listed, nil)
	broker.rememberListing(pi, root, start.Add(-time.Second), &ModelCatalog{}, nil)
	if err := store.Save(SessionState{BQSessionID: "current", Agent: AgentPi, Transport: TransportCLI, ExternalSessionID: "native", Model: "p/two"}); err != nil {
		t.Fatal(err)
	}
	menu, err := broker.ModelCapabilities(t.Context(), AgentPi, "current", root)
	if err != nil || !slices.Equal(menu.Models, listed.Models) || menu.Selected != "p/two" || menu.SelectedEffort != "" || menu.DefaultModel != "" {
		t.Fatal(menu, err)
	}
	menu.Models[0].ID = "foreign"
	failed := ModelCatalog{}
	broker.rememberListing(pi, root, start, &failed, errors.New("temporarily unavailable"))
	if !slices.Equal(failed.Models, listed.Models) || len(broker.recentInventory(pi, root+"/other").models) != 0 {
		t.Fatal(failed)
	}
	broker.rememberListing(acp, root, start, &listed, nil)
	if len(broker.recentInventory(acp, root).models) != 0 {
		t.Fatal("shared a session-scoped ACP catalog")
	}
	broker.rememberListing(pi, root, time.Now(), &ModelCatalog{}, nil)
	if len(broker.recentInventory(pi, root).models) != 0 {
		t.Fatal("a successful empty inventory must replace the old rows")
	}
}

func TestCodexModelCatalogPagesAndSkipsHiddenModels(t *testing.T) {
	var cursors []any
	menu, err := codexModelCatalog(context.Background(), func(_ context.Context, method string, params any) (rpcEnvelope, error) {
		if method != "model/list" {
			t.Fatal(method)
		}
		cursor := params.(map[string]any)["cursor"]
		cursors = append(cursors, cursor)
		if cursor == nil {
			return rpcEnvelope{Result: json.RawMessage(`{"data":[{"id":"row","model":"first","displayName":"First"},{"id":"hidden","hidden":true}],"nextCursor":"page2"}`)}, nil
		}
		return rpcEnvelope{Result: json.RawMessage(`{"data":[{"id":"second"},{"model":"first"}],"nextCursor":null}`)}, nil
	})
	if err != nil || !reflect.DeepEqual(cursors, []any{nil, "page2"}) || !slices.Equal(menu.Models, []ModelOption{{"first", "First"}, {"second", "second"}}) {
		t.Fatalf("catalog=%+v cursors=%v error=%v", menu, cursors, err)
	}
}

func TestACPModelMenuWaitsForConfigOptionsFromItsOwnSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := &stdioACPClient{}
		got := make(chan acpModels, 1)
		go func() {
			models, _ := c.availableModels(t.Context(), "native")
			got <- models
		}()
		synctest.Wait()
		c.rememberModels("other", acpEffortConfig("a", "normal"))
		synctest.Wait()
		select {
		case <-got:
			t.Fatal("used another session's model report")
		default:
		}
		c.rememberModels("native", acpEffortConfig("b", "deep"))
		menu := <-got
		if menu.Selected != "b" || len(menu.Models) != 2 || menu.SelectedEffort != "deep" {
			t.Fatal(menu)
		}
	})
}

func TestACPModelMenuWaitsForChoicesAfterCurrentModel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := &stdioACPClient{}
		c.rememberModels("native", json.RawMessage(`{"models":{"currentModelId":"a","availableModels":[]}}`))
		got := make(chan acpModels, 1)
		go func() {
			models, _ := c.availableModels(t.Context(), "native")
			got <- models
		}()
		synctest.Wait()
		select {
		case <-got:
			t.Fatal("closed the model menu after receiving only the current model")
		default:
		}
		c.rememberModels("native", acpEffortConfig("a", "normal"))
		if menu := <-got; menu.Selected != "a" || len(menu.Models) != 2 {
			t.Fatal(menu)
		}
		// A provider may only know its current model. Return it at the deadline.
		c.rememberModels("native", json.RawMessage(`{"models":{"currentModelId":"b"}}`))
		menu, ok := c.availableModels(t.Context(), "native")
		if !ok || menu.Selected != "b" || len(menu.Models) != 0 {
			t.Fatal(menu, ok)
		}
	})
}

func TestModelPickerKeepsExistingCLISessionsDistinctFromACP(t *testing.T) {
	cli := &AgentTransport{Agent: AgentPi, Kind: TransportCLI}
	acp := &AgentTransport{Agent: AgentPi, Kind: TransportACP}
	for _, tc := range []struct {
		name                                  string
		preferred, installedCLI, installedACP *AgentTransport
		saved                                 TransportKind
		want                                  *AgentTransport
	}{
		{"saved-cli", acp, cli, acp, TransportCLI, cli},
		{"saved-acp", cli, cli, acp, TransportACP, acp},
		{"missing-acp", cli, cli, nil, TransportACP, nil},
		{"missing-cli", acp, nil, acp, TransportCLI, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := transportForSession(DetectionResult{Agent: AgentPi, Preferred: tc.preferred, CLI: tc.installedCLI, ACP: tc.installedACP},
				SessionState{Agent: AgentPi, ExternalSessionID: "original", Transport: tc.saved})
			if got != tc.want || (err != nil) != (tc.want == nil) {
				t.Fatalf("transport=%v, error=%v", got, err)
			}
		})
	}
}

// Drive both adapters through their real RPC encoders, with synchronous
// replies in memory so the test can assert ordering without a subprocess.
type modelWire func(rpcEnvelope)

func (w modelWire) Close() error { return nil }
func (w modelWire) Write(line []byte) (int, error) {
	var msg rpcEnvelope
	if err := json.Unmarshal(line, &msg); err != nil {
		return 0, err
	}
	w(msg)
	return len(line), nil
}

func TestCodexModelOverrideIsSentOnTheSameThread(t *testing.T) {
	c := &codexAppServerClient{responses: map[string]chan rpcEnvelope{}, done: make(chan struct{})}
	c.stdin = modelWire(func(msg rpcEnvelope) {
		var params struct{ ThreadID, Model string }
		if json.Unmarshal(msg.Params, &params) != nil || msg.Method != "turn/start" || params.ThreadID != "original" || params.Model != "selected" {
			t.Fatalf("unexpected RPC: %s %s", msg.Method, msg.Params)
		}
		c.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(`{"turn":{"id":"t"}}`)}
		c.notification(rpcEnvelope{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"original","turn":{"id":"t","status":"completed","items":[{"id":"a","type":"agentMessage","text":"ok"}]}}`)})
	})
	reply, err := c.PromptWithModel(context.Background(), "original", "hi", "selected")
	if err != nil || reply != "ok" {
		t.Fatalf("reply=%q err=%v", reply, err)
	}
}

func TestACPModelSelectionUsesAdvertisedProtocolBeforePrompt(t *testing.T) {
	for _, tc := range []struct{ name, catalog, method, key string }{
		{"modern", `{"configOptions":[{"type":"boolean","currentValue":true},{"category":"model","type":"select","id":"llm-choice","currentValue":"one","options":[{"group":"provider","options":[{"value":"two","name":"Two"}]}]}]}`, "session/set_config_option", "value"},
		{"legacy", `{"models":{"currentModelId":"one","availableModels":[{"modelId":"two","name":"Two"}]}}`, "session/set_model", "modelId"},
		{"empty-config-with-legacy-list", `{"configOptions":[{"type":"select","id":"model","currentValue":"one","options":[]}],"models":{"currentModelId":"one","availableModels":[{"modelId":"two","name":"Two"}]}}`, "session/set_model", "modelId"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &stdioACPClient{responses: map[string]chan rpcEnvelope{}, collectors: map[string]*strings.Builder{}}
			c.rememberModels("s", json.RawMessage(tc.catalog))
			var calls []string
			c.stdin = modelWire(func(msg rpcEnvelope) {
				calls = append(calls, msg.Method)
				if msg.Method == tc.method {
					var params map[string]string
					if json.Unmarshal(msg.Params, &params) != nil || params["sessionId"] != "s" || params[tc.key] != "two" || (tc.name == "modern" && params["configId"] != "llm-choice") {
						t.Fatalf("unexpected params: %s", msg.Params)
					}
				}
				c.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(`{}`)}
			})
			if _, err := c.PromptWithModel(context.Background(), "s", "hi", "unknown"); err == nil || len(calls) != 0 {
				t.Fatal("unknown model was accepted")
			}
			if _, err := c.PromptWithModel(context.Background(), "s", "hi", "two"); err != nil || !slices.Equal(calls, []string{tc.method, "session/prompt"}) {
				t.Fatalf("calls=%v err=%v", calls, err)
			}
		})
	}
}

func TestCLIModelListsKeepProviderIDsAndExcludeUnavailableModels(t *testing.T) {
	for _, tc := range []struct {
		name AgentName
		line string
		want []ModelOption
	}{
		{AgentPi, `{"type":"response","id":"models","command":"get_available_models","success":true,"data":{"models":[{"provider":"p","id":"org/m","name":"Friendly"},{"provider":"q","id":"org/m","name":"Friendly"}]}}`, []ModelOption{{"p/org/m", "p / Friendly"}, {"q/org/m", "q / Friendly"}}},
		{AgentClaude, `{"type":"control_response","response":{"subtype":"success","request_id":"models","response":{"models":[{"value":"opus","displayName":"Opus"},{"value":"update","disabled":true}]}}}`, []ModelOption{{"opus", "Opus"}}},
	} {
		var menu ModelCatalog
		err := readCLIModels(t.Context(), tc.name, "requests", &catalogInput{}, io.NopCloser(strings.NewReader(tc.line)), &menu)
		if err != nil || !slices.Equal(menu.Models, tc.want) {
			t.Fatalf("%s %+v %v", tc.name, menu, err)
		}
	}
}

func TestPiPickerSkipsUnknownDefaultAndUnaddressableProvider(t *testing.T) {
	input := &catalogInput{}
	frames := strings.Join([]string{
		`{"type":"response","id":"state","command":"get_state","success":true,"data":{"model":{"provider":"unknown","id":"placeholder"},"thinkingLevel":"high"}}`,
		`{"type":"response","id":"models","command":"get_available_models","success":true,"data":{"models":[{"provider":"bad/provider","id":"a"},{"provider":"local","id":"org/a"}]}}`,
	}, "\n")
	var menu ModelCatalog
	if err := readCLIModels(t.Context(), AgentPi, "requests", input, io.NopCloser(strings.NewReader(frames)), &menu); err != nil ||
		menu.DefaultModel != "" || menu.SelectedEffort != "" || len(menu.Models) != 1 || menu.Models[0].ID != "local/org/a" {
		t.Fatal(menu, err)
	}
}

func TestCLIModelOverrideReplacesConfiguredFlagsOnResume(t *testing.T) {
	args := codexCatalogArgs([]string{"wrapper", "--", "codex", "exec", "--json", "-c", "model_provider=local", "--profile=test", "--skip-git-repo-check"})
	if !slices.Equal(args, []string{"wrapper", "--", "codex", "-c", "model_provider=local", "--profile=test", "debug", "models"}) {
		t.Fatal(args)
	}
	pi := cliModelArgs([]string{"--mode", "json", "--provider=old", "--model", "first"}, AgentPi, "p/org/m")
	if !slices.Equal(pi, []string{"--mode", "json", "--provider", "p", "--model", "org/m"}) {
		t.Fatal(pi)
	}
	claude := cliModelArgs([]string{"-p", "--output-format", "json", "--model=first"}, AgentClaude, "second")
	if !slices.Equal(claude, []string{"-p", "--output-format", "json", "--model", "second"}) {
		t.Fatal(claude)
	}
	codex := codexArgs(CommandSpec{Args: cliModelArgs([]string{"exec", "-m", "old", "--json"}, AgentCodex, "new")}, "same-thread")
	if !slices.Equal(codex, []string{"exec", "resume", "same-thread", "--json", "--model", "new", "--skip-git-repo-check"}) {
		t.Fatal(codex)
	}
	wrapped := []string{"wrapper", "--", "codex", "--profile", "local", "exec", "--json", "--model=old"}
	got := codexArgs(CommandSpec{Args: cliModelArgs(wrapped, AgentCodex, "new")}, "same-thread")
	want := []string{"wrapper", "--", "codex", "--profile", "local", "exec", "resume", "same-thread", "--json", "--model", "new", "--skip-git-repo-check"}
	if !slices.Equal(got, want) {
		t.Fatal(got)
	}
}

type catalogACPClient struct{ reconnectingACPClient }

func (c *catalogACPClient) availableModels(_ context.Context, nativeID string) (acpModels, bool) {
	return acpModels{ModelCatalog: ModelCatalog{Models: []ModelOption{{"first", "First"}, {"second", "Second"}}, Selected: "first"}}, nativeID == "pi-thread"
}

func TestAgentCatalogRestoresAnInactiveAgentsModelWithoutSwitchingTheChat(t *testing.T) {
	store := NewStateStore(t.TempDir())
	if err := store.Save(SessionState{BQSessionID: "chat", Agent: AgentPi, Transport: TransportACP, ExternalSessionID: "pi-thread", Model: "second"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(SessionState{BQSessionID: "chat", Agent: AgentCodex, Transport: TransportAppServer, ExternalSessionID: "codex-thread"}); err != nil {
		t.Fatal(err)
	}
	client := &catalogACPClient{}
	b := NewBroker(store, map[AgentName]DetectionResult{AgentPi: {Agent: AgentPi, Preferred: &AgentTransport{Agent: AgentPi, Kind: TransportACP}}}, func(context.Context, CommandSpec, string) (ACPClient, error) { return client, nil })
	defer b.Close()
	menu, err := b.Models(context.Background(), AgentPi, "chat", ".")
	if err != nil || menu.Selected != "second" || len(menu.Models) != 2 || client.loadCount != 1 || client.closeCount != 1 || menu.AllowCustom {
		t.Fatalf("menu=%+v client=%+v err=%v", menu, client, err)
	}
	active, err := b.ActiveAgent("chat")
	if err != nil || active != AgentCodex {
		t.Fatalf("catalog changed route: %v %v", active, err)
	}
}

func TestScopedCLIArgsUseTheClearedContextAndKeepWorkspaceOptions(t *testing.T) {
	for _, tc := range []struct {
		name       AgentName
		args, want []string
	}{
		{AgentPi, []string{"wrap", "-c", "--", "pi", "--session=old", "--continue", "--provider", "local", "--no-session"}, []string{"wrap", "-c", "--", "pi", "--provider", "local", "--no-session"}},
		{AgentClaude, []string{"-r", "old", "--fork-session", "--model", "sonnet", "--no-session-persistence"}, []string{"--model", "sonnet", "--no-session-persistence"}},
		{AgentCodex, []string{"wrap", "--", "codex", "--profile", "resume", "exec", "--json", "resume", "--last", "--all", "--model", "fast", "old-thread"}, []string{"wrap", "--", "codex", "--profile", "resume", "exec", "--json", "--model", "fast"}},
	} {
		before := slices.Clone(tc.args)
		if got := scopedCLIArgs(tc.name, tc.args); !slices.Equal(got, tc.want) || !slices.Equal(before, tc.args) {
			t.Fatal(tc.name, got, tc.args)
		}
	}
}
