package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"bqagent/internal/extagent"
)

func TestWebUIModelPicksPersistLaunchModelAndPerModelEffort(t *testing.T) {
	root := t.TempDir()
	store := extagent.NewStateStore(root)
	channel := func(dir string) *WebUIChannel {
		t.Helper()
		pi := &extagent.AgentTransport{Agent: extagent.AgentPi, Kind: extagent.TransportCLI}
		cursor := &extagent.AgentTransport{Agent: extagent.AgentCursor, Kind: extagent.TransportACP}
		b := extagent.NewBroker(extagent.NewStateStore(dir), map[extagent.AgentName]extagent.DetectionResult{
			extagent.AgentPi: {Preferred: pi, CLI: pi}, extagent.AgentCursor: {Preferred: cursor, ACP: cursor},
		}, nil)
		t.Cleanup(func() { _ = b.Close() })
		return NewWebUIChannel(NewService(ServiceOptions{WorkspaceRoot: dir, ExternalBroker: b}), true)
	}
	ch := channel(root)
	request := func(ch *WebUIChannel, name, chat, payload string, want int) extagent.ModelPreferences {
		t.Helper()
		method := http.MethodGet
		if payload != "" {
			method = http.MethodPost
		}
		r := httptest.NewRequest(method, "/api/v1/webui/agent-models?view=capabilities&agent="+name+"&session_id="+chat, strings.NewReader(payload))
		w := httptest.NewRecorder()
		ch.handleAgentModels(w, r)
		if w.Code != want {
			t.Errorf("status %d, want %d: %s", w.Code, want, w.Body.String())
			return extagent.ModelPreferences{}
		}
		var result struct {
			Preferences extagent.ModelPreferences `json:"preferences"`
		}
		if method == http.MethodGet && json.Unmarshal(w.Body.Bytes(), &result) != nil {
			t.Error(w.Body.String())
		}
		return result.Preferences
	}
	request(ch, "pi", "", `{"model":"p/a"}`, 200)
	if err := store.Save(extagent.SessionState{Agent: extagent.AgentPi, BQSessionID: "bound", Transport: extagent.TransportCLI, ExternalSessionID: "native"}); err != nil {
		t.Fatal(err)
	}
	// Independent tabs edit different models. Both deltas must survive,
	// and an existing chat's effort pick must not change the launch model.
	var writes sync.WaitGroup
	otherProcess := channel(root)
	for i, pick := range []string{`{"model":"p/a","effort":"high"}`, `{"model":"p/b","effort":"low"}`} {
		writer := []*WebUIChannel{ch, otherProcess}[i]
		writes.Go(func() { request(writer, "pi", "bound", pick, 200) })
	}
	writes.Wait()
	ch = channel(root)
	prefs := request(ch, "pi", "", "", 200)
	if prefs.Model != "p/a" || prefs.Efforts["p/a"] != "high" || prefs.Efforts["p/b"] != "low" {
		t.Fatal(prefs)
	}
	if got := request(ch, "pi", "bound", "", 200); got.Model != "" || got.Efforts["p/b"] != "low" {
		t.Fatal(got)
	}
	if got := request(ch, "cursor", "", "", 200); got.Model != "" || len(got.Efforts) != 0 {
		t.Fatal(got)
	}
	if got := request(channel(t.TempDir()), "pi", "", "", 200); got.Model != "" {
		t.Fatal(got)
	}
	request(ch, "pi", "", `{"model":"p/b","effort":"medium"}`, 200)
	request(ch, "pi", "bound", `{"model":"p/b","effort":""}`, 200)
	if got := request(ch, "pi", "", "", 200); got.Model != "p/b" || got.Efforts["p/b"] != "" || got.Efforts["p/a"] != "high" {
		t.Fatal(got)
	}
	request(ch, "pi", "bound", `{"model":"p/private","custom":true}`, 200)
	if got := request(ch, "pi", "", "", 200); got.Model != "p/private" || !got.Custom {
		t.Fatal(got)
	}
	request(ch, "pi", "", `{"effort":"high"}`, 400)
	request(ch, "cursor", "", `{"model":"anything","custom":true}`, 400)
	request(ch, "pi", "", `{"model":"--bad"}`, 400)
	request(ch, "pi", "", `{"model":""}`, 200)
	if got := request(ch, "pi", "", "", 200); got.Model != "" || got.Custom || got.Efforts["p/a"] != "high" {
		t.Fatal(got)
	}
}
