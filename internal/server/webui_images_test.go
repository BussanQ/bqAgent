package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bqagent/internal/extagent"
	"bqagent/internal/session"
)

type imagePromptClient struct {
	groupFakeACP
	received extagent.PromptOptions
	prompt   string
}

func (c *imagePromptClient) PromptWithOptions(_ context.Context, _, prompt string, options extagent.PromptOptions) (string, error) {
	c.prompt, c.received = prompt, options
	return "image received", nil
}

func TestWebUIRoutesImageOnlyToExternalAgentAndRestoresAPrivatePreview(t *testing.T) {
	root := t.TempDir()
	client := &imagePromptClient{groupFakeACP: groupFakeACP{name: "test"}}
	agentName := extagent.AgentPi
	b := extagent.NewBroker(extagent.NewStateStore(root), map[extagent.AgentName]extagent.DetectionResult{agentName: {Preferred: &extagent.AgentTransport{Agent: agentName, Kind: extagent.TransportACP, ACPImages: true}}}, func(context.Context, extagent.CommandSpec, string) (extagent.ACPClient, error) { return client, nil })
	defer b.Close()
	s := NewService(ServiceOptions{WorkspaceRoot: root, ExternalBroker: b})
	var img bytes.Buffer
	if err := png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(img.Bytes())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webui/chat", strings.NewReader(`{"agent":"bqagent","message":"/pi","images":[{"mime_type":"image/png","data_base64":"`+encoded+`"}]}`))
	req.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	NewWebUIChannel(s, true).handleStreamChat(out, req)
	_, tail, _ := strings.Cut(out.Body.String(), "event: done\ndata: ")
	var done webUIDoneEvent
	if json.Unmarshal([]byte(tail), &done) != nil || done.Reply != "image received" || done.ReplyAgent != agentName || client.prompt != "" || len(client.received.Images) != 1 || !bytes.Equal(client.received.Images[0].Data, img.Bytes()) {
		t.Fatal(out.Body.String(), client.prompt)
	}
	h := &handler{service: s}
	get := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.handleConversationHistory(w, httptest.NewRequest(method, path, nil))
		return w
	}
	url := "/api/v1/webui/conversations/" + done.SessionID
	history := get(http.MethodGet, url)
	var restored struct{ Messages []conversationMessage }
	if history.Code != 200 || strings.Contains(history.Body.String(), encoded) || json.Unmarshal(history.Body.Bytes(), &restored) != nil || len(restored.Messages[0].Images) != 1 {
		t.Fatal(history.Body.String())
	}
	ref := restored.Messages[0].Images[0]
	asset := get(http.MethodGet, url+"?image="+ref)
	if asset.Code != 200 || asset.Header().Get("Content-Type") != "image/png" || !bytes.Equal(asset.Body.Bytes(), img.Bytes()) {
		t.Fatal(asset.Code, asset.Body.String())
	}
	other, err := s.store.Create(session.CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	if w := get(http.MethodGet, "/api/v1/webui/conversations/"+other.ID()+"?image="+ref); w.Code != 404 {
		t.Fatal("image crossed sessions")
	}
	if w := get(http.MethodDelete, url+"?image="+ref); w.Code != 405 {
		t.Fatal("image request could delete the session")
	}
	if w := get(http.MethodGet, url+"?image=0.0."+strings.Repeat("0", 64)); w.Code != 404 {
		t.Fatal("unverified record was served")
	}
}
