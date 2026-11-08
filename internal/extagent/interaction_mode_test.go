package extagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
	"testing"
	"time"
)

func TestCodexInteractionModeUsesTheResumedThreadsModelAndEffort(t *testing.T) {
	const id = "original"
	for _, tc := range []struct {
		name, reported, saved, pick, model, effort string
		wantMode, wantModel, wantEffort            string
	}{
		{"plan with current model", "default", "", "plan", "", "", "plan", "m", "high"},
		{"build with new model", "plan", "", "default", "new", "", "default", "new", ""},
		{"plan with simultaneous overrides", "default", "", "plan", "new", "low", "plan", "new", "low"},
		{"older resume without mode", "", "plan", "", "", "", "plan", "m", "high"},
		{"newer native selection wins", "default", "plan", "", "", "", "", "", ""},
		{"model switch inside native plan", "plan", "", "", "new", "low", "plan", "new", "low"},
		{"legacy chat without explicit selection", "", "", "", "", "", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &codexAppServerClient{responses: map[string]chan rpcEnvelope{}, done: make(chan struct{})}
			starts := 0
			c.stdin = modelWire(func(msg rpcEnvelope) {
				switch msg.Method {
				case "collaborationMode/list":
					if tc.wantMode == "" || tc.reported == tc.wantMode {
						t.Fatal("unexpected mode listing")
					}
					c.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(`{"data":[{"mode":"plan"},{"mode":"default"}]}`)}
				case "thread/resume":
					response := map[string]any{"thread": map[string]string{"id": id}, "model": "m", "reasoningEffort": "high"}
					if tc.reported != "" {
						response["collaborationMode"] = map[string]any{"mode": tc.reported, "settings": map[string]string{"model": "m", "developer_instructions": "native rules"}}
					}
					body, _ := json.Marshal(response)
					c.responses[string(msg.ID)] <- rpcEnvelope{Result: body}
				case "turn/start":
					starts++
					var params struct {
						ThreadID, Model, Effort string
						CollaborationMode       *struct {
							Mode     string
							Settings map[string]json.RawMessage
						}
					}
					if json.Unmarshal(msg.Params, &params) != nil || params.ThreadID != id || params.Model != tc.model || params.Effort != tc.effort {
						t.Fatal(string(msg.Params))
					}
					m := params.CollaborationMode
					if tc.wantMode != "" {
						effort := "null"
						if tc.wantEffort != "" {
							effort = fmt.Sprintf("%q", tc.wantEffort)
						}
						instructions := "null"
						if tc.reported == tc.wantMode {
							instructions = `"native rules"`
						}
						if m == nil || m.Mode != tc.wantMode || string(m.Settings["model"]) != fmt.Sprintf("%q", tc.wantModel) || string(m.Settings["reasoning_effort"]) != effort || string(m.Settings["developer_instructions"]) != instructions {
							t.Fatal(string(msg.Params))
						}
					} else if m != nil {
						t.Fatal("overwrote native mode", string(msg.Params))
					}
					c.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(`{"turn":{"id":"turn"}}`)}
					c.notification(rpcEnvelope{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"original","turn":{"id":"turn","status":"completed","items":[{"type":"agentMessage","id":"a","text":"ok"}]}}`)})
				default:
					t.Fatal(msg.Method)
				}
			})
			if _, err := c.LoadSession(t.Context(), id, "."); err != nil {
				t.Fatal(err)
			}
			if _, err := c.PromptWithOptions(t.Context(), id, "question", PromptOptions{InteractionMode: tc.pick, savedInteractionMode: tc.saved, Model: tc.model, Effort: tc.effort}); err != nil {
				t.Fatal(err)
			}
			if state := c.threadSettings(id); starts != 1 || state.InteractionMode != firstNonEmpty(tc.wantMode, tc.reported) {
				t.Fatal(starts, state)
			}
		})
	}
	for _, mode := range []string{"plan", "invalid"} {
		c := &codexAppServerClient{}
		if _, err := c.PromptWithOptions(t.Context(), id, "must not send", PromptOptions{InteractionMode: mode}); err == nil {
			t.Fatal("sent without a supported mode and model")
		}
	}
}

func TestCodexInteractionModePersistsOnlyWhatNativeAccepted(t *testing.T) {
	for _, tc := range []struct {
		name, pick, expected string
		reject               bool
	}{
		{"generation failed after start", "plan", "plan", false}, {"start was rejected", "plan", "default", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			store := NewStateStore(root)
			state := SessionState{BQSessionID: "chat", Agent: AgentCodex, ExternalSessionID: "original", Transport: TransportAppServer, Model: "m", InteractionMode: "default"}
			if err := store.Save(state); err != nil {
				t.Fatal(err)
			}
			b := NewBroker(store, map[AgentName]DetectionResult{AgentCodex: {Preferred: &AgentTransport{Agent: AgentCodex, Kind: TransportAppServer}}}, nil)
			defer b.Close()
			c := &codexAppServerClient{cmd: &exec.Cmd{Cancel: func() error { return nil }}, responses: map[string]chan rpcEnvelope{}, done: make(chan struct{}), waitDone: make(chan struct{})}
			close(c.waitDone)
			b.acpClients[acpClientKey{sessionID: "chat", agent: AgentCodex}] = c
			starts := 0
			c.stdin = modelWire(func(msg rpcEnvelope) {
				switch msg.Method {
				case "collaborationMode/list":
					c.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(`{"data":[{"mode":"plan"}]}`)}
				case "thread/resume":
					c.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(`{"thread":{"id":"original"},"model":"m","collaborationMode":{"mode":"default","settings":{"model":"m"}}}`)}
				case "turn/start":
					starts++
					if tc.reject {
						c.responses[string(msg.ID)] <- rpcEnvelope{Error: &rpcError{Code: -32602, Message: "mode unavailable"}}
						return
					}
					c.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(`{"turn":{"id":"new"}}`)}
					c.notification(rpcEnvelope{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"original","turn":{"id":"new","status":"failed","error":{"message":"generation failed"}}}`)})
				default:
					t.Fatal(msg.Method)
				}
			})
			if _, err := b.SendTurn(context.Background(), TurnRequest{BQSessionID: "chat", Agent: AgentCodex, CWD: root, Prompt: "hi", InteractionMode: tc.pick}); err == nil || starts != 1 {
				t.Fatal(err, starts)
			}
			got, err := b.ModelCapabilities(t.Context(), AgentCodex, "chat", root)
			if err != nil || !got.SupportsInteractionMode || got.InteractionMode != tc.expected {
				t.Fatal(got, err)
			}
		})
	}
}

func TestCodexUnconfirmedPlanModeNeverSendsUserContent(t *testing.T) {
	for _, reply := range []rpcEnvelope{
		{Error: &rpcError{Code: -32601, Message: "method not found"}},
		{Result: json.RawMessage(`{"data":[{"mode":"default"},{"name":"Plan","mode":null}]}`)},
	} {
		c := &codexAppServerClient{responses: map[string]chan rpcEnvelope{}, done: make(chan struct{}), threadOptions: SessionState{ExternalSessionID: "thread", Model: "m"}}
		probes := 0
		c.stdin = modelWire(func(msg rpcEnvelope) {
			if msg.Method != "collaborationMode/list" || string(msg.Params) != "{}" {
				t.Fatal("sent prompt before confirming plan mode", msg.Method, string(msg.Params))
			}
			probes++
			c.responses[string(msg.ID)] <- reply
		})
		if _, err := c.PromptWithOptions(t.Context(), "thread", "inspect only", PromptOptions{InteractionMode: "plan"}); err == nil || probes != 1 || c.threadSettings("thread").InteractionMode != "" {
			t.Fatal(err, probes)
		}
	}
}

func TestCodexModeCatalogPreservesNativeAvailabilityWithoutSharingChatModes(t *testing.T) {
	modes, err := codexModeCatalog(t.Context(), func(_ context.Context, method string, _ any) (rpcEnvelope, error) {
		if method != "collaborationMode/list" {
			t.Fatal(method)
		}
		return rpcEnvelope{Result: json.RawMessage(`{"data":[{"mode":"default"},{"name":"Plan","mode":null},{"mode":"future"},{"mode":"default"}]}`)}, nil
	})
	if err != nil || !slices.Equal(modes, []string{"default"}) {
		t.Fatal(modes, err)
	}
	root := t.TempDir()
	store := NewStateStore(root)
	spec := &AgentTransport{Agent: AgentCodex, Kind: TransportAppServer}
	b := NewBroker(store, map[AgentName]DetectionResult{AgentCodex: {Preferred: spec}}, nil)
	defer b.Close()
	if err := store.Save(SessionState{BQSessionID: "one", Agent: AgentCodex, Transport: TransportAppServer, ExternalSessionID: "native", InteractionMode: "plan"}); err != nil {
		t.Fatal(err)
	}
	b.rememberListing(spec, root, time.Now(), &ModelCatalog{InteractionModes: modes}, nil)
	modes[0] = "changed"
	menu, err := b.ModelCapabilities(t.Context(), AgentCodex, "one", root)
	if err != nil || menu.InteractionMode != "plan" || !menu.SupportsInteractionMode || !slices.Equal(menu.InteractionModes, []string{"default"}) {
		t.Fatal(menu, err)
	}
	b.rememberListing(spec, root, time.Now(), &ModelCatalog{InteractionModes: []string{}, modeListMissing: true}, nil)
	bound, _ := b.ModelCapabilities(t.Context(), AgentCodex, "one", root)
	other, _ := b.ModelCapabilities(t.Context(), AgentCodex, "", root)
	if !bound.SupportsInteractionMode || bound.InteractionModes != nil || other.SupportsInteractionMode || other.InteractionModes == nil || other.InteractionMode != "" {
		t.Fatal(bound, other)
	}
}
