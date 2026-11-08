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

const tierCatalog = `{"data":[
 {"model":"quick","serviceTiers":[{"id":"priority","name":"Fast","description":"Priority processing"},{"id":"batch","name":"Batch"}]},
 {"model":"also","serviceTiers":[],"additionalSpeedTiers":["priority"]},
 {"model":"legacy","additionalSpeedTiers":["fast","fast"]},
 {"model":"normal","serviceTiers":[]},{"model":"unknown"},
 {"model":"hidden","hidden":true,"additionalSpeedTiers":["secret"]}
]}`

func TestCodexServiceTierCatalogHonorsModernLegacyAndAbsentCapabilities(t *testing.T) {
	menu, err := codexModelCatalog(t.Context(), func(_ context.Context, method string, _ any) (rpcEnvelope, error) {
		if method != "model/list" {
			t.Fatal(method)
		}
		return rpcEnvelope{Result: json.RawMessage(tierCatalog)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(menu.ServiceTiers["quick"], []ServiceTierOption{{"priority", "Fast", "Priority processing"}, {"batch", "Batch", ""}}) ||
		!slices.Equal(menu.ServiceTiers["legacy"], []ServiceTierOption{{"fast", "Fast", ""}}) ||
		!hasServiceTier(menu.ServiceTiers["also"], "priority") || menu.ServiceTiers["normal"] == nil || len(menu.ServiceTiers) != 4 {
		t.Fatal(menu.ServiceTiers)
	}
	root := t.TempDir()
	transport := &AgentTransport{Agent: AgentCodex, Kind: TransportAppServer}
	b := NewBroker(NewStateStore(root), map[AgentName]DetectionResult{AgentCodex: {Preferred: transport}}, nil)
	defer b.Close()
	menu.SelectedServiceTier = "priority"
	b.rememberListing(transport, root, time.Now(), &menu, nil)
	menu.ServiceTiers["quick"][0].Name = "edited"
	cached, err := b.ModelCapabilities(t.Context(), AgentCodex, "", root)
	if err != nil || cached.SelectedServiceTier != "" || cached.ServiceTiers["quick"][0].Name != "Fast" {
		t.Fatal(cached, err)
	}
	cached.ServiceTiers["quick"][0].Name = "another edit"
	if b.recentInventory(transport, root).tiers["quick"][0].Name != "Fast" || b.recentInventory(transport, "other").tiers != nil {
		t.Fatal("service tier cache leaked beyond its workspace or request")
	}
}

func TestCodexServiceTierChangesOnOriginalThreadAndReturnsAfterModelSwitch(t *testing.T) {
	c := &codexAppServerClient{responses: map[string]chan rpcEnvelope{}, done: make(chan struct{})}
	var sent []string
	c.stdin = modelWire(func(msg rpcEnvelope) {
		switch msg.Method {
		case "thread/resume":
			c.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(`{"model":"quick","serviceTier":null,"thread":{"id":"original"}}`)}
		case "model/list":
			c.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(tierCatalog)}
		case "turn/start":
			var params struct {
				ThreadID, Model string
				ServiceTier     *string
			}
			if err := json.Unmarshal(msg.Params, &params); err != nil || params.ThreadID != "original" {
				t.Fatalf("%s, %v", msg.Params, err)
			}
			tier := "(inherit)"
			if params.ServiceTier != nil {
				tier = *params.ServiceTier
			}
			sent = append(sent, tier)
			id := fmt.Sprintf("t-%d", len(sent))
			c.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(fmt.Sprintf(`{"turn":{"id":%q}}`, id))}
			c.notification(rpcEnvelope{Method: "turn/completed", Params: json.RawMessage(fmt.Sprintf(`{"threadId":"original","turn":{"id":%q,"status":"completed","items":[{"type":"agentMessage","id":"reply","text":"ok"}]}}`, id))})
		default:
			t.Fatal(msg.Method)
		}
	})
	id, err := c.LoadSession(t.Context(), "original", ".")
	if err != nil || c.threadSettings(id).ServiceTier != "default" {
		t.Fatal(id, err, c.threadSettings(id))
	}
	for _, step := range []struct {
		model, tier                string
		explicit                   bool
		wire, effective, preferred string
	}{
		{"quick", "", false, "(inherit)", "default", ""},
		{"quick", "priority", true, "priority", "priority", "priority"},
		{"normal", "priority", false, "default", "default", "priority"},
		{"also", "priority", false, "priority", "priority", "priority"},
		{"also", "default", true, "default", "default", "default"},
	} {
		reply, err := c.PromptWithOptions(t.Context(), id, "hello", PromptOptions{Model: step.model, ServiceTier: step.tier, TierExplicit: step.explicit})
		got := c.threadSettings(id)
		if err != nil || reply != "ok" || sent[len(sent)-1] != step.wire || got.ServiceTier != step.effective || got.PreferredServiceTier != step.preferred {
			t.Fatalf("step %+v: sent=%v state=%+v reply=%q err=%v", step, sent, got, reply, err)
		}
	}
	before := len(sent)
	if _, err := c.PromptWithOptions(t.Context(), id, "must not send", PromptOptions{Model: "normal", ServiceTier: "priority", TierExplicit: true}); err == nil || len(sent) != before || c.threadSettings(id).ServiceTier != "default" {
		t.Fatal(err, sent, c.threadSettings(id))
	}
	remember := "priority"
	if _, err := c.PromptWithOptions(t.Context(), id, "queued at standard speed", PromptOptions{Model: "normal", ServiceTier: "default", TierExplicit: true, PreservedTier: &remember}); err != nil {
		t.Fatal(err)
	}
	if got := c.threadSettings(id); got.ServiceTier != "default" || got.PreferredServiceTier != "priority" || sent[len(sent)-1] != "default" {
		t.Fatal(got, sent)
	}
}

func TestCodexServiceTierRestoresConfiguredUnlistedTierAndIsolatesOtherTransports(t *testing.T) {
	c := &codexAppServerClient{responses: map[string]chan rpcEnvelope{}, done: make(chan struct{})}
	c.stdin = modelWire(func(msg rpcEnvelope) {
		if msg.Method != "model/list" {
			t.Fatal(msg.Method)
		}
		c.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(tierCatalog)}
	})
	tier, _, err := c.turnServiceTier(t.Context(), SessionState{Model: "quick", ServiceTier: "flex"}, PromptOptions{Model: "normal"})
	if tier != "flex" || err != nil {
		t.Fatal(tier, err)
	}
	for _, tc := range []struct {
		agent AgentName
		kind  TransportKind
	}{{AgentPi, TransportCLI}, {AgentCodex, TransportACP}, {AgentCodex, TransportCLI}} {
		if _, err := (*Broker)(nil).optionsForTurn(t.Context(), TurnRequest{Agent: tc.agent, ServiceTier: "priority"}, SessionState{}, tc.kind); err == nil {
			t.Fatal(tc)
		}
	}
}

func TestCodexServiceTierPersistenceUsesNativeAcknowledgementOnFailedGeneration(t *testing.T) {
	for _, tc := range []struct {
		name, pick, expected string
		starts               int
	}{
		{"accepted before generation failed", "priority", "priority", 1},
		{"rejected before turn started", "missing", "default", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			store := NewStateStore(root)
			const chat = "tier-chat"
			initial := SessionState{Agent: AgentCodex, BQSessionID: chat, Transport: TransportAppServer, ExternalSessionID: "native", Model: "quick", ServiceTier: "default"}
			if err := store.Save(initial); err != nil {
				t.Fatal(err)
			}
			b := NewBroker(store, map[AgentName]DetectionResult{AgentCodex: {Preferred: &AgentTransport{Agent: AgentCodex, Kind: TransportAppServer}}}, nil)
			defer b.Close()
			c := &codexAppServerClient{cmd: &exec.Cmd{Cancel: func() error { return nil }}, waitDone: make(chan struct{}), done: make(chan struct{}), responses: map[string]chan rpcEnvelope{}}
			close(c.waitDone)
			b.acpClients[acpClientKey{sessionID: chat, agent: AgentCodex}] = c
			starts := 0
			c.stdin = modelWire(func(msg rpcEnvelope) {
				switch msg.Method {
				case "thread/resume":
					c.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(`{"model":"quick","serviceTier":null,"thread":{"id":"native"}}`)}
				case "model/list":
					c.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(tierCatalog)}
				case "turn/start":
					starts++
					c.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(`{"turn":{"id":"t"}}`)}
					c.notification(rpcEnvelope{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"native","turn":{"id":"t","status":"failed","error":{"message":"provider failed"}}}`)})
				default:
					t.Fatal(msg.Method)
				}
			})
			if _, err := b.SendTurn(t.Context(), TurnRequest{BQSessionID: chat, Agent: AgentCodex, CWD: root, Prompt: "hi", Model: "quick", ServiceTier: tc.pick}); err == nil || starts != tc.starts {
				t.Fatal(err, starts)
			}
			saved, err := b.ModelCapabilities(t.Context(), AgentCodex, chat, root)
			if err != nil || !saved.SupportsServiceTier || saved.SelectedServiceTier != tc.expected || (saved.PreferredServiceTier == tc.pick) != (starts == 1) {
				t.Fatal(saved, err)
			}
		})
	}
}
