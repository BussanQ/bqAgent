package extagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"testing"
)

func TestCodexConversationForkVerifiesSelectedPrefixAndReusesNativeID(t *testing.T) {
	dir := t.TempDir()
	p := ForkCheckpoint{State: SessionState{ExternalSessionID: "parent"}, Turn: "second"}
	forks := 0
	long := true
	rpc := func(_ context.Context, method string, params any) (rpcEnvelope, error) {
		attrs := params.(map[string]any)
		var raw string
		switch method {
		case "thread/turns/list":
			if attrs["sortDirection"] != "asc" {
				t.Fatal(attrs)
			}
			if attrs["threadId"] == "parent" {
				if c, ok := attrs["cursor"].(*string); ok && c != nil {
					raw = `{"data":[{"id":"second","status":"completed"},{"id":"newer","status":"inProgress"}]}`
				} else {
					raw = `{"data":[{"id":"first","status":"completed"}],"nextCursor":"more"}`
				}
			} else if long {
				raw = `{"data":[{"id":"one","status":"completed"},{"id":"two","status":"completed"},{"id":"extra","status":"completed"}]}`
			} else {
				raw = `{"data":[{"id":"one","status":"completed"},{"id":"two","status":"completed"}]}`
			}
		case "thread/fork":
			forks++
			if attrs["lastTurnId"] != "second" || attrs["threadId"] != "parent" || attrs["cwd"] != "/workspace" {
				t.Fatal("wrong fork RPC", attrs)
			}
			raw = `{"thread":{"id":"child","forkedFromId":"parent"}}`
		case "thread/read":
			if attrs["threadId"] != "child" {
				t.Fatal(attrs)
			}
			raw = `{"thread":{"id":"child","forkedFromId":"parent"}}`
		default:
			t.Fatal("unexpected RPC", method)
		}
		return rpcEnvelope{Result: json.RawMessage(raw)}, nil
	}
	if _, _, err := copyCodexCheckpoint(t.Context(), rpc, dir, "/workspace", p); err == nil {
		t.Fatal("accepted later turns")
	}
	long = false
	child, ids, err := copyCodexCheckpoint(t.Context(), rpc, dir, "/workspace", p)
	if err != nil || child != "child" || ids["first"] != "one" || ids["second"] != "two" || forks != 1 {
		t.Fatal(child, ids, err, forks)
	}
}

func TestCodexConversationForkDoesNotRepeatUnknownNativeCreation(t *testing.T) {
	dir := t.TempDir()
	p := ForkCheckpoint{State: SessionState{ExternalSessionID: "parent"}, Turn: "a"}
	n := 0
	rpc := func(_ context.Context, method string, _ any) (rpcEnvelope, error) {
		switch method {
		case "thread/turns/list":
			return rpcEnvelope{Result: json.RawMessage(`{"data":[{"id":"a","status":"completed"}]}`)}, nil
		case "thread/fork":
			n++
			return rpcEnvelope{}, io.EOF
		}
		t.Fatal(method)
		return rpcEnvelope{}, nil
	}
	for range 2 {
		if _, _, err := copyCodexCheckpoint(t.Context(), rpc, dir, "/workspace", p); !errors.Is(err, ErrForkUnknown) {
			t.Fatal(err)
		}
	}
	if n != 1 {
		t.Fatal(n)
	}
}

type redirectedForkClient struct {
	reconnectingACPClient
	prompted bool
}

func (c *redirectedForkClient) LoadSession(context.Context, string, string) (string, error) {
	return "other-session", nil
}

func (c *redirectedForkClient) Prompt(context.Context, string, string) (string, error) {
	c.prompted = true
	return "", nil
}

func TestForkedConversationRequiresItsExactTransportAndSession(t *testing.T) {
	cli := &AgentTransport{Agent: AgentCodex, Kind: TransportCLI}
	state := SessionState{Agent: AgentCodex, BQSessionID: "chat", Transport: TransportAppServer, ExternalSessionID: "child", ForkBinding: true}
	if _, err := transportForSession(DetectionResult{Agent: AgentCodex, Preferred: cli, CLI: cli}, state); err == nil {
		t.Fatal("opened fork with a different transport")
	}
	root := t.TempDir()
	store := NewStateStore(root)
	state.Agent, state.Transport = AgentOpenCode, TransportACP
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	c := &redirectedForkClient{}
	acp := &AgentTransport{Agent: AgentOpenCode, Kind: TransportACP}
	broker := NewBroker(store, map[AgentName]DetectionResult{AgentOpenCode: {Preferred: acp, ACP: acp}},
		func(context.Context, CommandSpec, string) (ACPClient, error) { return c, nil })
	defer broker.Close()
	_, err := broker.SendTurn(t.Context(), TurnRequest{Agent: state.Agent, BQSessionID: state.BQSessionID, CWD: root, Prompt: "hi"})
	after, readErr := store.Load(state.BQSessionID)
	if err == nil || c.prompted || readErr != nil || after != state {
		t.Fatal(err, c.prompted, after, readErr)
	}
}

func TestConversationForkOnlyRetriesExplicitRPCRejections(t *testing.T) {
	for _, code := range []int{-32601, -32602, -32000} {
		path := filepath.Join(t.TempDir(), "receipt.json")
		calls := 0
		for range 2 {
			_, err := nativeForkID(path, "parent", "turn", nil, func() (string, bool, error) {
				calls++
				return "", forkRequestRefused(rpcEnvelope{Error: &rpcError{Code: code}}), errors.New("RPC failed")
			})
			if err == nil || errors.Is(err, ErrForkUnknown) != (code == -32000) {
				t.Fatal(code, err)
			}
		}
		if (calls == 1) != (code == -32000) {
			t.Fatal(code, calls)
		}
	}
}

func TestOpenCodeForkUsesAdvertisedCapabilityAndLoadsOnlyNewID(t *testing.T) {
	for _, supported := range []bool{true, false} {
		c := &stdioACPClient{responses: map[string]chan rpcEnvelope{}}
		calls := 0
		c.stdin = modelWire(func(msg rpcEnvelope) {
			var raw string
			switch msg.Method {
			case "initialize":
				caps := "{}"
				if supported {
					caps = `{"fork":{}}`
				}
				raw = `{"agentCapabilities":{"loadSession":true,"sessionCapabilities":` + caps + `}}`
			case "session/fork":
				calls++
				var p struct {
					SessionID, Cwd string
					McpServers     []any
				}
				if json.Unmarshal(msg.Params, &p) != nil || p.SessionID != "source" || p.Cwd != "/repo" || p.McpServers == nil {
					t.Fatal(string(msg.Params))
				}
				raw = `{"sessionId":"child"}`
			case "session/load":
				var p struct{ SessionID string }
				if json.Unmarshal(msg.Params, &p) != nil || p.SessionID != "child" {
					t.Fatal(string(msg.Params))
				}
				raw = `{}`
			default:
				t.Fatal(msg.Method)
			}
			c.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(raw)}
		})
		if err := c.Initialize(t.Context()); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		for range 2 {
			id, err := copyACPTip(t.Context(), c, dir, "/repo", ForkCheckpoint{State: SessionState{ExternalSessionID: "source"}, Turn: "last", Tip: true})
			if supported && (err != nil || id != "child") || !supported && err == nil {
				t.Fatal(supported, id, err)
			}
		}
		if supported && calls != 1 || !supported && calls != 0 {
			t.Fatal(supported, calls)
		}
	}
}
