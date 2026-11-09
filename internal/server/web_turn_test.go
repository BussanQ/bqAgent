package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bqagent/internal/extagent"
	"bqagent/internal/session"
	"bqagent/internal/turn"
)

type replayTestAgent struct {
	groupFakeACP
	started, release chan struct{}
	calls            atomic.Int32
}

func (a *replayTestAgent) Prompt(ctx context.Context, _ string, _ string) (string, error) {
	a.calls.Add(1)
	sink := extagent.ActivitySinkFromContext(ctx)
	sink(extagent.Activity{Text: "before-disconnect"})
	sink(extagent.Activity{CallID: "tool", Name: "read", Status: "running"})
	close(a.started)
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-a.release:
	}
	sink(extagent.Activity{CallID: "tool", Name: "read", Status: "succeeded", Output: "after-reconnect"})
	return "final from same run", nil
}

func TestWebUITurnDisconnectReplayAndStop(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprintf("stop=%v", stop), func(t *testing.T) {
			root := t.TempDir()
			client := &replayTestAgent{groupFakeACP: groupFakeACP{name: "pi"}, started: make(chan struct{}), release: make(chan struct{})}
			b := extagent.NewBroker(extagent.NewStateStore(root), map[extagent.AgentName]extagent.DetectionResult{extagent.AgentPi: {Preferred: &extagent.AgentTransport{Agent: extagent.AgentPi, Kind: extagent.TransportACP}}}, func(context.Context, extagent.CommandSpec, string) (extagent.ACPClient, error) { return client, nil })
			defer b.Close()
			s := NewService(ServiceOptions{WorkspaceRoot: root, ExternalBroker: b})
			prior, err := s.store.Create(session.CreateOptions{Chat: true})
			if err != nil {
				t.Fatal(err)
			}
			if err := prior.RecordMessage(map[string]any{"role": "user", "content": "previous turn"}); err != nil {
				t.Fatal(err)
			}
			ch := NewWebUIChannel(s, true)
			life, shutdown := context.WithCancel(context.Background())
			defer shutdown()
			ch.Start(life)
			body := fmt.Sprintf(`{"turn_id":"replay","session_id":"%s","agent":"pi","message":"current turn"}`, prior.ID())
			req := httptest.NewRequest(http.MethodPost, "/api/v1/webui/chat", strings.NewReader(body))
			browser, disconnect := context.WithCancel(req.Context())
			defer disconnect()
			req = req.WithContext(browser)
			req.Header.Set("Content-Type", "application/json")
			initial := httptest.NewRecorder()
			subDone := make(chan struct{})
			go func() { ch.handleStreamChat(initial, req); close(subDone) }()
			select {
			case <-client.started:
			case <-time.After(3 * time.Second):
				t.Fatal("agent did not start")
			}
			job := s.webTurns.get("replay")
			defer s.webTurns.expire(job)
			disconnect()
			select {
			case <-subDone:
			case <-time.After(time.Second):
				t.Fatal("browser is still attached")
			}
			if s.webTurns.active(prior.ID()) != "replay" {
				t.Fatal("disconnect cancelled or hid the job")
			}
			get := func(query string) *httptest.ResponseRecorder {
				t.Helper()
				request := httptest.NewRequest(http.MethodGet, "/api/v1/webui/turns?"+query, nil)
				ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
				defer cancel()
				res := httptest.NewRecorder()
				ch.handleWebTurn(res, request.WithContext(ctx))
				return res
			}
			base := get("turn_id=replay&view=history")
			if base.Code != 200 || !strings.Contains(base.Body.String(), "previous turn") || strings.Contains(base.Body.String(), "current turn") {
				t.Fatalf("base %d %s", base.Code, base.Body)
			}
			if get("turn_id=replay&workspace_id=foreign").Code != 404 || get("turn_id=replay&after=9999").Code != 400 {
				t.Fatal("bad workspace or cursor accepted")
			}
			duplicate := httptest.NewRecorder()
			req2 := httptest.NewRequest(http.MethodPost, "/api/v1/webui/chat", strings.NewReader(body))
			req2.Header.Set("Content-Type", "application/json")
			ch.handleStreamChat(duplicate, req2)
			if duplicate.Code != 409 {
				t.Fatal(duplicate.Code)
			}
			if stop {
				if !s.StopTurn("replay") {
					t.Fatal("detached turn could not be stopped")
				}
			} else {
				close(client.release)
			}
			all := get("turn_id=replay")
			text := all.Body.String()
			if all.Code != 200 || strings.Count(text, "before-disconnect") != 1 || !strings.Contains(text, "event: turn_session") || !strings.Contains(text, "event: tool_start") {
				t.Fatal(text)
			}
			terminal := "event: done"
			if stop {
				terminal = "event: stopped"
			}
			if !strings.Contains(text, terminal) || !stop && !strings.Contains(text, "after-reconnect") {
				t.Fatal(text)
			}
			suffix := get("turn_id=replay&after=2").Body.String()
			if !strings.HasPrefix(suffix, "id: 3\n") || strings.Contains(suffix, "event: turn_start") || !strings.Contains(suffix, terminal) || client.calls.Load() != 1 {
				t.Fatal(suffix, client.calls.Load())
			}
			if active := s.webTurns.active(prior.ID()); active != "" {
				t.Fatalf("finished job still active: %s", active)
			}
		})
	}
}

func TestWebTurnLargeReplayAndSessionDeletion(t *testing.T) {
	var r webTurnRegistry
	j, _, _ := r.start(context.Background(), "t", "s")
	content := strings.Repeat("x", replayMemoryBytes+1)
	for _, frame := range []struct{ name, value string }{{"message", "small"}, {"message", content}, {"done", "complete"}} {
		if err := j.publish(frame.name, frame.value); err != nil {
			t.Fatal(err)
		}
	}
	data, next, _, done, err := j.batch(1)
	if err != nil || j.file == nil || next != 2 || !strings.Contains(string(data), content) || done {
		t.Fatal(next, err)
	}
	r.forgetSession("s")
	if r.get("t") != nil {
		t.Fatal("retained deleted session")
	}
	if _, _, _, _, err := j.batch(1); err == nil {
		t.Fatal("deleted replay still readable")
	}
}

func TestWebTurnBaselineSurvivesTranscriptReplacement(t *testing.T) {
	s := NewService(ServiceOptions{WorkspaceRoot: t.TempDir()})
	saved, err := s.store.Create(session.CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = saved.RecordMessage(map[string]any{"role": "user", "content": "original"}); err != nil {
		t.Fatal(err)
	}
	j, _, _ := s.webTurns.start(context.Background(), "history", "")
	defer s.webTurns.expire(j)
	j.bind(s, saved.ID())
	if err = saved.RewriteMessages([]map[string]any{{"role": "user", "content": "replacement"}}); err != nil {
		t.Fatal(err)
	}
	history, err := j.history(context.Background(), s)
	raw, _ := json.Marshal(history)
	if err != nil || !strings.Contains(string(raw), "original") || strings.Contains(string(raw), "replacement") {
		t.Fatal(string(raw), err)
	}
}

func TestWebTurnGrowingSnapshotsStayCompactAndReplayAsFullSSE(t *testing.T) {
	var r webTurnRegistry
	job, _, _ := r.start(context.Background(), "t", "")
	defer r.expire(job)
	text := strings.Repeat("a", 128)
	for i := 1; i <= 100; i++ {
		text += strings.Repeat("b", 16)
		if err := job.publish("message_snapshot", turn.Event{TurnID: "t", ItemID: "m", Seq: uint64(i), Kind: "message.updated", Phase: "draft", Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	if err := job.publish("done", map[string]string{"reply": text}); err != nil {
		t.Fatal(err)
	}
	if size := job.ends[len(job.ends)-1]; size > 30000 {
		t.Fatalf("journal grew to %d bytes", size)
	}
	for _, compact := range []bool{false, true} {
		w := httptest.NewRecorder()
		url := "/api/v1/webui/turns"
		if compact {
			url += "?compact=1"
		}
		job.subscribe(w, httptest.NewRequest(http.MethodGet, url, nil), 99)
		body := w.Body.String()
		name := "message_snapshot"
		if compact {
			name = "message_patch"
		}
		if !strings.HasPrefix(body, "id: 100\nevent: "+name+"\n") {
			t.Fatalf("unexpected frame: %.100s", body)
		}
		_, frame, _ := strings.Cut(body, "data: ")
		frame, _, _ = strings.Cut(frame, "\n\n")
		var item turn.Event
		if err := json.Unmarshal([]byte(frame), &item); err != nil {
			t.Fatal(err)
		}
		want := text
		if compact {
			want = strings.Repeat("b", 16)
		}
		if item.Text != want {
			t.Fatalf("cursor restored %d text bytes, want %d", len(item.Text), len(want))
		}
	}
}
