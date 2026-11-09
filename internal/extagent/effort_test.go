package extagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestNativeEffortChoicesDependOnModel(t *testing.T) {
	ctx := context.Background()
	menu, err := codexModelCatalog(ctx, func(context.Context, string, any) (rpcEnvelope, error) {
		return rpcEnvelope{Result: json.RawMessage(`{"data":[{"model":"a","isDefault":true,"supportedReasoningEfforts":[{"reasoningEffort":"medium"},{"reasoningEffort":"ultra"}],"defaultReasoningEffort":"medium"},{"model":"b","supportedReasoningEfforts":[{"reasoningEffort":"low"}]}]}`)}, nil
	})
	if err != nil || menu.DefaultModel != "a" || menu.Efforts["a"].Default != "medium" || !menu.Efforts["a"].supports("ultra") || menu.Efforts["b"].supports("ultra") {
		t.Fatalf("Codex: %+v %v", menu, err)
	}
	readCodexModelDefaults(ctx, "/work", func(_ context.Context, method string, params any) (rpcEnvelope, error) {
		if method != "config/read" || params.(map[string]string)["cwd"] != "/work" {
			t.Fatal(method, params)
		}
		return rpcEnvelope{Result: json.RawMessage(`{"config":{"model":"b","model_reasoning_effort":"low"}}`)}, nil
	}, &menu)
	if menu.DefaultModel != "b" || menu.Efforts["b"].Default != "low" {
		t.Fatal(menu)
	}
	menu = ModelCatalog{}
	pi := `{"id":"models","command":"get_available_models","type":"response","success":true,"data":{"models":[{"provider":"local","id":"r","reasoning":true,"thinkingLevelMap":{"off":null,"xhigh":"high"}},{"provider":"local","id":"plain","reasoning":false}]}}`
	if _, err := parsePiModelReply([]byte(pi), &menu); err != nil {
		t.Fatal(err)
	}
	if !parsePiModelState([]byte(`{"id":"state","type":"response","command":"get_state","success":true,"data":{"model":{"provider":"local","id":"r"},"thinkingLevel":"high"}}`), &menu) {
		t.Fatal("no Pi state")
	}
	p := menu.Efforts["local/r"]
	if menu.SelectedEffort != "high" || menu.DefaultModel != "local/r" || !p.supports("xhigh") || p.supports("max") || p.supports("off") || len(menu.Efforts["local/plain"].Options) != 0 {
		t.Fatal(menu)
	}
	menu = ModelCatalog{}
	claude := `{"type":"control_response","response":{"subtype":"success","request_id":"models","response":{"models":[{"value":"opus","resolvedModel":"version-1","supportsEffort":true,"supportedEffortLevels":["low","high"]},{"value":"default","resolvedModel":"version-1"},{"value":"plain","supportsEffort":false,"supportedEffortLevels":["high"]}]}}}`
	if err := readCLIModels(t.Context(), AgentClaude, claudeModelControlRequests, &catalogInput{}, io.NopCloser(strings.NewReader(claude)), &menu); err != nil || menu.DefaultModel != "version-1" || !menu.Efforts["version-1"].supports("high") || !menu.Efforts["opus"].supports("high") || menu.Efforts["plain"].supports("high") || !slices.Equal(menu.Models, []ModelOption{{"version-1", "version-1"}, {"opus", "opus"}, {"plain", "plain"}}) {
		t.Fatalf("Claude: %+v %v", menu, err)
	}
}

func TestCodexTurnAppliesNativeEffortOnExistingThread(t *testing.T) {
	c := &codexAppServerClient{responses: map[string]chan rpcEnvelope{}, done: make(chan struct{})}
	c.stdin = modelWire(func(msg rpcEnvelope) {
		var args struct{ ThreadID, Effort, Model string }
		if json.Unmarshal(msg.Params, &args) != nil || msg.Method != "turn/start" || args.ThreadID != "thread" || args.Effort != "xhigh" || args.Model != "selected" {
			t.Fatal(msg.Method, string(msg.Params))
		}
		c.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(`{"turn":{"id":"turn"}}`)}
		c.notification(rpcEnvelope{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread","turn":{"id":"turn","status":"completed","items":[{"id":"a","type":"agentMessage","text":"ok"}]}}`)})
	})
	if _, err := c.PromptWithOptions(context.Background(), "thread", "question", PromptOptions{Model: "selected", Effort: "xhigh"}); err != nil {
		t.Fatal(err)
	}
}

func TestEffortFlagsUseNativeValuesAndKeepCLIResume(t *testing.T) {
	if got := cliEffortArgs([]string{"--mode", "json", "--thinking=max"}, AgentPi, "off"); !slices.Equal(got, []string{"--mode", "json", "--thinking", "off"}) {
		t.Fatal(got)
	}
	if got := cliEffortArgs([]string{"-p", "--effort", "low"}, AgentClaude, "xhigh"); !slices.Equal(got, []string{"-p", "--effort", "xhigh"}) {
		t.Fatal(got)
	}
	spec := CommandSpec{Args: cliEffortArgs([]string{"exec", "--json"}, AgentCodex, "ultra")}
	if got := codexArgs(spec, "thread"); !slices.Equal(got, []string{"exec", "resume", "thread", "--json", "-c", `model_reasoning_effort="ultra"`, "--skip-git-repo-check"}) {
		t.Fatal(got)
	}
}

func acpEffortConfig(model, level string) json.RawMessage {
	id := "effort-" + model
	choice := "brief"
	if model == "b" {
		choice = "deep"
	}
	return json.RawMessage(fmt.Sprintf(`{"configOptions":[{"id":"llm","category":"model","type":"select","currentValue":%q,"options":[{"value":"a"},{"value":"b"}]},{"id":%q,"category":"thought_level","type":"select","currentValue":%q,"options":[{"group":"native","options":[{"value":%q,"name":"Native label"},{"value":"normal"}]}]}]}`, model, id, level, choice))
}

func TestACPChangedModelValidatesItsNewEffortBeforePrompt(t *testing.T) {
	for _, tc := range []struct {
		effort string
		want   []string
	}{{"deep", []string{"llm=b", "effort-b=deep", "session/prompt"}}, {"brief", []string{"llm=b", "llm=a"}}} {
		t.Run(tc.effort, func(t *testing.T) {
			c := &stdioACPClient{responses: map[string]chan rpcEnvelope{}, collectors: map[string]*strings.Builder{}}
			c.rememberModels("s", acpEffortConfig("a", "normal"))
			var calls []string
			c.stdin = modelWire(func(msg rpcEnvelope) {
				result := json.RawMessage(`{}`)
				if msg.Method == "session/set_config_option" {
					var p struct{ ConfigID, Value string }
					if json.Unmarshal(msg.Params, &p) != nil {
						t.Fatal(string(msg.Params))
					}
					calls = append(calls, p.ConfigID+"="+p.Value)
					if p.ConfigID == "llm" {
						result = acpEffortConfig(p.Value, "normal")
					} else {
						result = acpEffortConfig("b", p.Value)
					}
				} else {
					calls = append(calls, msg.Method)
				}
				c.responses[string(msg.ID)] <- rpcEnvelope{Result: result}
			})
			_, err := c.PromptWithOptions(context.Background(), "s", "hi", PromptOptions{Model: "b", Effort: tc.effort})
			if (err == nil) != (tc.effort == "deep") || !slices.Equal(calls, tc.want) {
				t.Fatalf("calls=%v err=%v", calls, err)
			}
		})
	}
}

type previewEffortClient struct {
	reconnectingACPClient
	model    string
	prompted bool
}

func (c *previewEffortClient) availableModels(_ context.Context, id string) (acpModels, bool) {
	m, ok := parseACPModels(acpEffortConfig(c.model, "normal"))
	return m, ok && id == c.sessionID
}
func (c *previewEffortClient) selectModel(_ context.Context, id, model string) error {
	if id != c.sessionID {
		return fmt.Errorf("unexpected session")
	}
	c.model = model
	return nil
}
func (c *previewEffortClient) Prompt(context.Context, string, string) (string, error) {
	c.prompted = true
	return "", fmt.Errorf("preview sent a prompt")
}

func TestACPEffortPreviewNeverChangesOrPromptsActiveSession(t *testing.T) {
	store := NewStateStore(t.TempDir())
	if err := store.Save(SessionState{BQSessionID: "chat", Agent: AgentCursor, Transport: TransportACP, ExternalSessionID: "active", Model: "a", Effort: "brief"}); err != nil {
		t.Fatal(err)
	}
	probe := &previewEffortClient{reconnectingACPClient: reconnectingACPClient{sessionID: "probe"}, model: "a"}
	active := &previewEffortClient{reconnectingACPClient: reconnectingACPClient{sessionID: "active"}, model: "a"}
	b := NewBroker(store, map[AgentName]DetectionResult{AgentCursor: {Preferred: &AgentTransport{Agent: AgentCursor, Kind: TransportACP}}}, func(context.Context, CommandSpec, string) (ACPClient, error) { return probe, nil })
	b.acpClients[acpClientKey{sessionID: "chat", agent: AgentCursor}] = active
	defer b.Close()
	menu, err := b.PreviewModels(context.Background(), AgentCursor, "chat", ".", "b")
	if err != nil || !menu.Efforts["b"].supports("deep") || !menu.SessionScopedEffort || active.model != "a" || probe.model != "b" || probe.loadCount != 0 || probe.closeCount != 1 || probe.prompted || active.prompted {
		t.Fatalf("catalog=%+v active=%+v probe=%+v err=%v", menu, active, probe, err)
	}
	saved, err := store.Load("chat")
	if err != nil || saved.Model != "a" || saved.Effort != "brief" {
		t.Fatal(saved, err)
	}
}
