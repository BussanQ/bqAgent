package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"bqagent/internal/agent"
	"bqagent/internal/extagent"
	"bqagent/internal/session"
	"bqagent/internal/tools"
)

func TestWebUIStreamsACPPermissionRequest(t *testing.T) {
	recorder := httptest.NewRecorder()
	stream := &sseStream{writer: recorder, flusher: recorder}
	sseACPPermissionSink{stream: stream}.EmitACPPermissionRequest(extagent.ACPPermissionRequest{
		RequestID: "permission-1", BQSessionID: "group-1", Agent: extagent.AgentCursor, ExternalSessionID: "cursor-session",
		ToolCall: json.RawMessage(`{"toolCallId":"tool-1","title":"修改文件"}`),
		Options:  []extagent.ACPPermissionOption{{OptionID: "allow", Name: "允许", Kind: "allow_once"}},
	})
	body := recorder.Body.String()
	if !strings.Contains(body, "event: acp_permission") || !strings.Contains(body, `"option_id":"allow"`) || !strings.Contains(body, `"agent":"cursor"`) {
		t.Fatalf("SSE body = %s", body)
	}
}

func TestWebUIEmbeddedAssetContract(t *testing.T) {
	root := t.TempDir()
	service := newTestService(root, "http://example.invalid")
	channel := NewWebUIChannel(service, true)
	handler := NewHandler(HandlerOptions{Service: service, Channels: []Channel{channel}})
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	response, err := http.Get(apiServer.URL + "/")
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	page, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatalf("read index failed: %v", err)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(response.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("GET / status = %d, content-type = %q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	if cache := response.Header.Get("Cache-Control"); cache != "no-store" {
		t.Fatalf("index cache-control = %q, want no-store", cache)
	}
	if bytes.Contains(page, []byte(`/src/`)) {
		t.Fatal("production index still references Vite source files")
	}
	for _, target := range []string{"/", "/favicon.ico", "/favicon.svg"} {
		headRequest, err := http.NewRequest(http.MethodHead, apiServer.URL+target, nil)
		if err != nil {
			t.Fatal(err)
		}
		headResponse, err := http.DefaultClient.Do(headRequest)
		if err != nil {
			t.Fatalf("HEAD %s failed: %v", target, err)
		}
		headBody, _ := io.ReadAll(headResponse.Body)
		headResponse.Body.Close()
		if headResponse.StatusCode != http.StatusOK || len(headBody) != 0 {
			t.Fatalf("HEAD %s status = %d, body bytes = %d", target, headResponse.StatusCode, len(headBody))
		}
	}

	assetPattern := regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`)
	matches := assetPattern.FindAllSubmatch(page, -1)
	if len(matches) < 2 {
		t.Fatalf("index asset references = %d, want hashed JavaScript and CSS", len(matches))
	}
	for _, match := range matches {
		assetPath := string(match[1])
		if _, err := fs.ReadFile(webUIFiles, strings.TrimPrefix(assetPath, "/")); err != nil {
			t.Fatalf("embedded asset %q is missing: %v", assetPath, err)
		}
		assetResponse, err := http.Get(apiServer.URL + assetPath)
		if err != nil {
			t.Fatalf("GET %s failed: %v", assetPath, err)
		}
		assetBody, readErr := io.ReadAll(assetResponse.Body)
		assetResponse.Body.Close()
		if readErr != nil || assetResponse.StatusCode != http.StatusOK || len(assetBody) == 0 {
			t.Fatalf("GET %s status = %d, bytes = %d, err = %v", assetPath, assetResponse.StatusCode, len(assetBody), readErr)
		}
		if cache := assetResponse.Header.Get("Cache-Control"); cache != "public, max-age=31536000, immutable" {
			t.Fatalf("asset %s cache-control = %q", assetPath, cache)
		}
		if strings.HasSuffix(assetPath, ".js") && !strings.Contains(assetResponse.Header.Get("Content-Type"), "javascript") {
			t.Fatalf("JavaScript content-type = %q", assetResponse.Header.Get("Content-Type"))
		}
		if strings.HasSuffix(assetPath, ".css") && !strings.Contains(assetResponse.Header.Get("Content-Type"), "text/css") {
			t.Fatalf("CSS content-type = %q", assetResponse.Header.Get("Content-Type"))
		}

		headRequest, err := http.NewRequest(http.MethodHead, apiServer.URL+assetPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		headResponse, err := http.DefaultClient.Do(headRequest)
		if err != nil {
			t.Fatalf("HEAD %s failed: %v", assetPath, err)
		}
		headBody, _ := io.ReadAll(headResponse.Body)
		headResponse.Body.Close()
		if headResponse.StatusCode != http.StatusOK || len(headBody) != 0 {
			t.Fatalf("HEAD %s status = %d, body bytes = %d", assetPath, headResponse.StatusCode, len(headBody))
		}
	}

	for _, target := range []string{"/missing", "/assets/missing.js"} {
		missing, err := http.Get(apiServer.URL + target)
		if err != nil {
			t.Fatal(err)
		}
		missing.Body.Close()
		if missing.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want 404", target, missing.StatusCode)
		}
	}

	traversal := httptest.NewRecorder()
	channel.handleAsset(traversal, httptest.NewRequest(http.MethodGet, "/assets/../index.html", nil))
	if traversal.Code != http.StatusNotFound {
		t.Fatalf("asset traversal status = %d, want 404", traversal.Code)
	}

	for _, target := range []string{"/", string(matches[0][1]), "/favicon.ico"} {
		request, err := http.NewRequest(http.MethodPost, apiServer.URL+target, nil)
		if err != nil {
			t.Fatal(err)
		}
		methodResponse, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		methodResponse.Body.Close()
		if methodResponse.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("POST %s status = %d, want 405", target, methodResponse.StatusCode)
		}
	}
}

func TestWebUIAttachmentTriggerShowsOnlyPlusIcon(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("webui", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`(?s)<button[^>]+id="add-attachment"[^>]*>(.*?)</button>`)
	match := pattern.FindSubmatch(page)
	if len(match) != 2 {
		t.Fatal("add attachment trigger not found")
	}
	if !bytes.Contains(match[1], []byte(`href="#icon-plus"`)) {
		t.Fatalf("attachment trigger does not contain plus icon: %s", match[1])
	}
	if bytes.Contains(match[1], []byte("<span")) || bytes.Contains(page, []byte(`id="chat-mode-label"`)) {
		t.Fatalf("attachment trigger still displays a mode label: %s", match[1])
	}
}

func TestWebUIComposerPrioritizesModelLabel(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("webui", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`(?s)<button[^>]+id="reasoning-effort-toggle"[^>]*>(.*?)</button>`)
	match := pattern.FindSubmatch(page)
	if len(match) != 2 {
		t.Fatal("reasoning effort trigger not found")
	}
	if bytes.Contains(match[1], []byte(`reasoning-icon`)) || !bytes.Contains(match[1], []byte(`id="reasoning-effort-label"`)) {
		t.Fatalf("reasoning effort trigger should display its label without a leading icon: %s", match[1])
	}
	if !bytes.Contains(page, []byte(`<wa-select id="model-select"`)) || bytes.Contains(page, []byte(`<select id="model-select"`)) {
		t.Fatal("model selector should use the Web Awesome select component")
	}

	styles, err := os.ReadFile(filepath.Join("webui", "src", "styles.css"))
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range [][]byte{
		[]byte("field-sizing: content"),
		[]byte("text-align: right"),
		[]byte("#model-select::part(listbox)"),
	} {
		if !bytes.Contains(styles, rule) {
			t.Fatalf("model selector styles do not contain %q", rule)
		}
	}
}

func TestWebUIGroupAskOptionHonorsHiddenState(t *testing.T) {
	styles, err := os.ReadFile(filepath.Join("webui", "src", "styles.css"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(styles, []byte(".chat-mode-option[hidden] { display: none; }")) {
		t.Fatal("chat mode option display styles override the group-mode hidden state")
	}
}

func TestWebUIServesFavicon(t *testing.T) {
	root := t.TempDir()
	service := newTestService(root, "http://example.invalid")
	handler := NewHandler(HandlerOptions{Service: service, Channels: []Channel{NewWebUIChannel(service, true)}})
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	response, err := http.Get(apiServer.URL + "/favicon.ico")
	if err != nil {
		t.Fatalf("GET /favicon.ico failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "image/x-icon" {
		t.Fatalf("content-type = %q, want image/x-icon", contentType)
	}
	if cacheControl := response.Header.Get("Cache-Control"); cacheControl != "public, max-age=86400" {
		t.Fatalf("cache-control = %q, want public, max-age=86400", cacheControl)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read favicon failed: %v", err)
	}
	wantHeader := []byte{0, 0, 1, 0, 1, 0}
	if len(body) < len(wantHeader) {
		t.Fatalf("favicon length = %d, want at least %d", len(body), len(wantHeader))
	}
	for index := range wantHeader {
		if body[index] != wantHeader[index] {
			t.Fatalf("favicon header byte %d = %d, want %d", index, body[index], wantHeader[index])
		}
	}
	pngOffset := int(binary.LittleEndian.Uint32(body[18:22]))
	if pngOffset < 22 || pngOffset >= len(body) {
		t.Fatalf("favicon PNG offset = %d, want a valid ICO payload offset", pngOffset)
	}
	iconImage, err := png.Decode(bytes.NewReader(body[pngOffset:]))
	if err != nil {
		t.Fatalf("decode favicon PNG failed: %v", err)
	}
	_, _, _, alpha := iconImage.At(0, 0).RGBA()
	if alpha != 0 {
		t.Fatalf("favicon corner alpha = %d, want transparent corner without a white border", alpha)
	}

	svgResponse, err := http.Get(apiServer.URL + "/favicon.svg")
	if err != nil {
		t.Fatalf("GET /favicon.svg failed: %v", err)
	}
	defer svgResponse.Body.Close()
	if svgResponse.StatusCode != http.StatusOK {
		t.Fatalf("SVG status = %d, want 200", svgResponse.StatusCode)
	}
	if contentType := svgResponse.Header.Get("Content-Type"); contentType != "image/svg+xml; charset=utf-8" {
		t.Fatalf("SVG content-type = %q, want image/svg+xml; charset=utf-8", contentType)
	}
	svgBody, err := io.ReadAll(svgResponse.Body)
	if err != nil {
		t.Fatalf("read SVG favicon failed: %v", err)
	}
	if !bytes.Contains(svgBody, []byte(`<svg xmlns="http://www.w3.org/2000/svg"`)) {
		t.Fatal("served SVG favicon is invalid")
	}
}

func TestWebUIProviderSettingsEncryptKeyAndApplySelection(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(t.TempDir(), ".agent")
	service := NewService(ServiceOptions{WorkspaceRoot: root, AgentDir: agentDir, Model: "environment-model"})
	apiServer := httptest.NewServer(NewHandler(HandlerOptions{Service: service}))
	defer apiServer.Close()

	body := `{"active_provider":"deepseek","providers":[{"id":"deepseek","name":"DeepSeek","api_type":"openai","base_url":"https://api.deepseek.example/v1","api_key":"secret-token","models":["deepseek-chat","deepseek-reasoner"],"default_model":"deepseek-chat"}]}`
	request, err := http.NewRequest(http.MethodPut, apiServer.URL+"/api/v1/webui/providers", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("PUT providers status = %d, body = %s", response.StatusCode, data)
	}
	raw, err := os.ReadFile(filepath.Join(agentDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret-token") {
		t.Fatal("config.json contains plaintext API key")
	}

	var settings struct {
		Providers []struct {
			APIKeyConfigured bool   `json:"api_key_configured"`
			APIKey           string `json:"api_key"`
		} `json:"providers"`
	}
	getWebUIJSON(t, apiServer.URL, "/api/v1/webui/providers", &settings)
	if len(settings.Providers) != 1 || !settings.Providers[0].APIKeyConfigured || settings.Providers[0].APIKey != "" {
		t.Fatalf("provider response exposed or omitted key state: %#v", settings.Providers)
	}

	selection := `{"provider_id":"deepseek","model":"deepseek-reasoner"}`
	response, err = http.Post(apiServer.URL+"/api/v1/webui/provider-selection", "application/json", strings.NewReader(selection))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("POST provider selection status = %d, body = %s", response.StatusCode, data)
	}
	info := service.RuntimeLLMInfo()
	if info.ProviderID != "deepseek" || info.Model != "deepseek-reasoner" {
		t.Fatalf("runtime info = %#v", info)
	}
}

func TestWebUIProviderSelectionUpdatesCurrentSessionModel(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(t.TempDir(), ".agent")
	service := NewService(ServiceOptions{WorkspaceRoot: root, AgentDir: agentDir, Model: "environment-model", SystemPrompt: "base prompt"})
	apiServer := httptest.NewServer(NewHandler(HandlerOptions{Service: service}))
	defer apiServer.Close()

	providerBody := `{"active_provider":"deepseek","providers":[{"id":"deepseek","name":"DeepSeek","api_type":"openai","base_url":"https://api.deepseek.example/v1","api_key":"secret-token","models":["deepseek-chat","deepseek-reasoner"],"default_model":"deepseek-chat"}]}`
	request, err := http.NewRequest(http.MethodPut, apiServer.URL+"/api/v1/webui/providers", strings.NewReader(providerBody))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("PUT providers status = %d", response.StatusCode)
	}

	switchResponse, err := service.HandleTurn(context.Background(), TurnRequest{Message: "/model deepseek-chat"})
	if err != nil {
		t.Fatalf("create selected-model session: %v", err)
	}
	selection := fmt.Sprintf(`{"provider_id":"deepseek","model":"deepseek-reasoner","session_id":%q}`, switchResponse.SessionID)
	response, err = http.Post(apiServer.URL+"/api/v1/webui/provider-selection", "application/json", strings.NewReader(selection))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("POST provider selection status = %d, body = %s", response.StatusCode, data)
	}
	if info := service.RuntimeLLMInfoForSession(switchResponse.SessionID); info.Model != "deepseek-reasoner" {
		t.Fatalf("session runtime model = %q, want deepseek-reasoner", info.Model)
	}

	client := &modelRecordingClient{}
	service.client = client
	if _, err := service.HandleTurn(context.Background(), TurnRequest{SessionID: switchResponse.SessionID, Message: "hello"}); err != nil {
		t.Fatalf("follow-up returned error: %v", err)
	}
	if len(client.models) != 1 || client.models[0] != "deepseek-reasoner" {
		t.Fatalf("models = %#v, want deepseek-reasoner", client.models)
	}
}

func TestWebUIProviderModelsUsesSavedEncryptedKey(t *testing.T) {
	var authorization string
	providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorization = request.Header.Get("Authorization")
		writeJSON(writer, http.StatusOK, map[string]any{"data": []map[string]string{{"id": "model-z"}, {"id": "model-a"}}})
	}))
	defer providerServer.Close()

	agentDir := filepath.Join(t.TempDir(), ".agent")
	service := NewService(ServiceOptions{WorkspaceRoot: t.TempDir(), AgentDir: agentDir})
	apiServer := httptest.NewServer(NewHandler(HandlerOptions{Service: service}))
	defer apiServer.Close()
	settings := fmt.Sprintf(`{"active_provider":"custom","providers":[{"id":"custom","name":"Custom","api_type":"openai","base_url":%q,"api_key":"saved-secret","models":["old-model"],"default_model":"old-model"}]}`, providerServer.URL)
	request, err := http.NewRequest(http.MethodPut, apiServer.URL+"/api/v1/webui/providers", strings.NewReader(settings))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("save provider status = %d", response.StatusCode)
	}

	response, err = http.Post(apiServer.URL+"/api/v1/webui/provider-models", "application/json", strings.NewReader(`{"provider_id":"custom","api_type":"anthropic","base_url":"http://127.0.0.1:1","api_key":""}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("discover models status = %d, body = %s", response.StatusCode, data)
	}
	var payload struct {
		Models []string `json:"models"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if authorization != "Bearer saved-secret" {
		t.Fatalf("Authorization = %q", authorization)
	}
	if fmt.Sprint(payload.Models) != "[model-a model-z]" {
		t.Fatalf("models = %#v", payload.Models)
	}
}

func TestWebUIConversationListAndHistory(t *testing.T) {
	agentDir := filepath.Join(t.TempDir(), ".agent")
	root := t.TempDir()
	service := NewService(ServiceOptions{WorkspaceRoot: root, AgentDir: agentDir})
	saved, err := service.store.Create(session.CreateOptions{Task: "梳理项目结构", Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := saved.RecordMessages(
		map[string]any{"role": "user", "content": "请介绍项目"},
		map[string]any{"role": "assistant", "content": "这是项目说明。"},
		map[string]any{"role": "tool", "content": "private tool output"},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "查看图片"}, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AA=="}}}},
		map[string]any{"role": "user", "content": "查看\n\n<attachment name=\"TODO.md\" path=\"docs/TODO.md\">\n# TODO\n</attachment>"},
	); err != nil {
		t.Fatal(err)
	}
	if err := saved.MarkCompleted(); err != nil {
		t.Fatal(err)
	}
	other := NewService(ServiceOptions{WorkspaceRoot: t.TempDir(), AgentDir: agentDir})
	if _, err := other.store.Create(session.CreateOptions{Task: "其他工作区", Chat: true}); err != nil {
		t.Fatal(err)
	}

	apiServer := httptest.NewServer(NewHandler(HandlerOptions{Service: service}))
	defer apiServer.Close()
	var list struct {
		Conversations []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"conversations"`
	}
	getWebUIJSON(t, apiServer.URL, "/api/v1/webui/conversations", &list)
	if len(list.Conversations) != 1 || list.Conversations[0].ID != saved.ID() || list.Conversations[0].Title != "梳理项目结构" {
		t.Fatalf("conversation list = %#v", list.Conversations)
	}
	var history struct {
		ID       string `json:"id"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
			Files   []struct {
				Name string `json:"name"`
				Path string `json:"path"`
			} `json:"files"`
		} `json:"messages"`
	}
	getWebUIJSON(t, apiServer.URL, "/api/v1/webui/conversations/"+saved.ID(), &history)
	if history.ID != saved.ID() || len(history.Messages) != 4 || history.Messages[0].Content != "请介绍项目" || history.Messages[1].Content != "这是项目说明。" || history.Messages[2].Content != "查看图片\n[图片]" {
		t.Fatalf("conversation history = %#v", history)
	}
	attachmentMessage := history.Messages[3]
	if attachmentMessage.Content != "查看" || len(attachmentMessage.Files) != 1 || attachmentMessage.Files[0].Name != "TODO.md" || attachmentMessage.Files[0].Path != "docs/TODO.md" {
		t.Fatalf("attachment history = %#v", attachmentMessage)
	}
}

func TestWebUIConversationContextMenuDeletesConversation(t *testing.T) {
	agentDir := filepath.Join(t.TempDir(), ".agent")
	service := NewService(ServiceOptions{WorkspaceRoot: t.TempDir(), AgentDir: agentDir})
	target, err := service.store.Create(session.CreateOptions{Task: "删除这个对话", Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	kept, err := service.store.Create(session.CreateOptions{Task: "保留这个对话", Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	nonChat, err := service.store.Create(session.CreateOptions{Task: "后台任务"})
	if err != nil {
		t.Fatal(err)
	}

	apiServer := httptest.NewServer(NewHandler(HandlerOptions{Service: service}))
	defer apiServer.Close()
	request, err := http.NewRequest(http.MethodDelete, apiServer.URL+"/api/v1/webui/conversations/"+target.ID(), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("DELETE conversation status = %d, want 200", response.StatusCode)
	}
	if _, err := service.store.Open(target.ID()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Open deleted conversation error = %v, want not exist", err)
	}
	if _, err := service.store.Open(kept.ID()); err != nil {
		t.Fatalf("kept conversation was affected: %v", err)
	}

	request, err = http.NewRequest(http.MethodDelete, apiServer.URL+"/api/v1/webui/conversations/"+nonChat.ID(), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("DELETE non-chat session status = %d, want 404", response.StatusCode)
	}
	if _, err := service.store.Open(nonChat.ID()); err != nil {
		t.Fatalf("non-chat session was deleted: %v", err)
	}
}

func TestWebUIDisabledDoesNotServeIndex(t *testing.T) {
	root := t.TempDir()
	service := newTestService(root, "http://example.invalid")
	handler := NewHandler(HandlerOptions{Service: service, Channels: []Channel{NewWebUIChannel(service, false)}})
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	response, err := http.Get(apiServer.URL + "/")
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when web UI disabled", response.StatusCode)
	}

	workspaceResponse, err := http.Get(apiServer.URL + "/api/v1/webui/workspace")
	if err != nil {
		t.Fatalf("GET workspace endpoint failed: %v", err)
	}
	defer workspaceResponse.Body.Close()
	if workspaceResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("workspace status = %d, want 404 when web UI disabled", workspaceResponse.StatusCode)
	}
}

func TestWebUIStreamChat(t *testing.T) {
	llmServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := writer.(http.Flusher)
		time.Sleep(time.Millisecond)
		for _, chunk := range []string{"Hello", ", world"} {
			fmt.Fprintf(writer, "data: %s\n\n", streamDeltaJSON(chunk))
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(time.Millisecond)
		}
		fmt.Fprintln(writer, `data: {"choices":[],"usage":{"prompt_tokens":4,"completion_tokens":6,"total_tokens":10,"prompt_tokens_details":{"cached_tokens":3},"completion_tokens_details":{"reasoning_tokens":1}}}`)
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer llmServer.Close()

	root := t.TempDir()
	service := newTestService(root, llmServer.URL)
	handler := NewHandler(HandlerOptions{Service: service, Channels: []Channel{NewWebUIChannel(service, true)}})
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	first := postWebUIChat(t, apiServer.URL, `{"message":"hi"}`)
	if ct := first.contentType; !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
	if !strings.Contains(first.raw, `"delta":"Hello"`) {
		t.Fatalf("stream missing delta events:\n%s", first.raw)
	}
	if !strings.Contains(first.raw, "event: progress") || !strings.Contains(first.raw, `"message":`) {
		t.Fatalf("stream missing progress event:\n%s", first.raw)
	}
	if first.done.Reply != "Hello, world" {
		t.Fatalf("reply = %q, want %q", first.done.Reply, "Hello, world")
	}
	if first.done.SessionID == "" {
		t.Fatal("done event missing session_id")
	}
	if first.done.Generation == nil {
		t.Fatal("done event missing generation metrics")
	}
	if first.done.Generation.FirstTokenLatencyMS <= 0 || first.done.Generation.GenerationDurationMS <= 0 {
		t.Fatalf("generation timing = %#v, want positive values", first.done.Generation)
	}
	if first.done.Generation.CompletionTokens != 6 || first.done.Generation.ReasoningTokens != 1 || first.done.Generation.TokensPerSecond <= 0 {
		t.Fatalf("generation metrics = %#v", first.done.Generation)
	}
	if !first.done.Generation.CacheUsageAvailable || first.done.Generation.PromptTokens != 4 || first.done.Generation.CachedPromptTokens != 3 {
		t.Fatalf("cache metrics = %#v", first.done.Generation)
	}

	// A follow-up carrying the session_id must reuse the same conversation.
	second := postWebUIChat(t, apiServer.URL, fmt.Sprintf(`{"session_id":%q,"message":"again"}`, first.done.SessionID))
	if second.done.SessionID != first.done.SessionID {
		t.Fatalf("second session_id = %q, want %q", second.done.SessionID, first.done.SessionID)
	}
}

func TestWebUIReasoningEffortReachesModel(t *testing.T) {
	var seenReasoningEffort string
	llmServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload struct {
			ReasoningEffort string `json:"reasoning_effort"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode model request failed: %v", err)
		}
		seenReasoningEffort = payload.ReasoningEffort
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer llmServer.Close()

	root := t.TempDir()
	service := newTestService(root, llmServer.URL)
	handler := NewHandler(HandlerOptions{Service: service, Channels: []Channel{NewWebUIChannel(service, true)}})
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	result := postWebUIChat(t, apiServer.URL, `{"message":"hi","reasoning_effort":"high"}`)
	if result.done.Reply != "done" {
		t.Fatalf("reply = %q, want done", result.done.Reply)
	}
	if seenReasoningEffort != "high" {
		t.Fatalf("upstream reasoning_effort = %q, want high", seenReasoningEffort)
	}
}

func TestWebUIEmptyFinalResponseFailsSession(t *testing.T) {
	root := t.TempDir()
	client := &sequenceChatClient{responses: []agent.AssistantMessage{
		{},
		{Content: " \n\t"},
		{Content: ""},
	}}
	service := NewService(ServiceOptions{
		WorkspaceRoot:   root,
		Client:          client,
		SystemPrompt:    "You are a helpful assistant. Be concise.",
		DefaultMaxTurns: agent.DefaultMaxIterations,
	})
	handler := NewHandler(HandlerOptions{Service: service, Channels: []Channel{NewWebUIChannel(service, true)}})
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	response, err := http.Post(apiServer.URL+"/api/v1/webui/chat", "application/json", strings.NewReader(`{"message":"finish the task"}`))
	if err != nil {
		t.Fatalf("POST /api/v1/webui/chat failed: %v", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read stream failed: %v", err)
	}
	raw := string(payload)
	if !strings.Contains(raw, "event: error") || !strings.Contains(raw, "empty final response") {
		t.Fatalf("stream missing empty-response error:\n%s", raw)
	}
	if strings.Contains(raw, "event: done") {
		t.Fatalf("empty final response must not emit done:\n%s", raw)
	}
	if client.requests != 3 {
		t.Fatalf("model requests = %d, want 3", client.requests)
	}

	entries, err := os.ReadDir(filepath.Join(root, ".agent", "sessions"))
	if err != nil {
		t.Fatalf("read sessions directory failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("session directories = %d, want 1", len(entries))
	}
	savedSession, err := session.NewStore(root).Open(entries[0].Name())
	if err != nil {
		t.Fatalf("open saved session failed: %v", err)
	}
	meta := savedSession.Meta()
	if meta.Status != session.StatusFailed {
		t.Fatalf("session status = %q, want failed", meta.Status)
	}
	if !strings.Contains(meta.LastError, "empty final response") {
		t.Fatalf("last error = %q, want empty-response error", meta.LastError)
	}
}

func TestWebUIDefaultStageBudgetsAreDisabled(t *testing.T) {
	if maxIterations := WebUIStageMaxIterations(); maxIterations != 0 {
		t.Fatalf("WebUIStageMaxIterations = %d, want disabled", maxIterations)
	}
	if timeout := WebUIStageTimeout(); timeout != 0 {
		t.Fatalf("WebUIStageTimeout = %s, want disabled", timeout)
	}
}

func TestWebUIRunsUntilCompletionBeyondChannelBudget(t *testing.T) {
	responses := make([]agent.AssistantMessage, 0, DefaultChannelMaxIterations+2)
	for iteration := 0; iteration <= DefaultChannelMaxIterations; iteration++ {
		responses = append(responses, agent.AssistantMessage{ToolCalls: []agent.ToolCall{{
			ID: fmt.Sprintf("tool-%d", iteration),
			Function: agent.FunctionCall{
				Name:      "missing_tool",
				Arguments: fmt.Sprintf(`{"step":%d}`, iteration),
			},
		}}})
	}
	responses = append(responses, agent.AssistantMessage{Content: "one-shot complete"})
	client := &sequenceChatClient{responses: responses}
	service := NewService(ServiceOptions{
		WorkspaceRoot:   t.TempDir(),
		Client:          client,
		SystemPrompt:    "You are a helpful assistant. Be concise.",
		ToolDefinitions: []tools.Definition{{Type: "function", Function: tools.FunctionDefinition{Name: "missing_tool"}}},
		DefaultMaxTurns: agent.DefaultMaxIterations,
	})
	handler := NewHandler(HandlerOptions{Service: service, Channels: []Channel{NewWebUIChannel(service, true)}})
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	result := postWebUIChat(t, apiServer.URL, `{"message":"finish the whole task"}`)
	if result.done.Reply != "one-shot complete" {
		t.Fatalf("reply = %q, want final completion", result.done.Reply)
	}
	if client.requests != DefaultChannelMaxIterations+2 {
		t.Fatalf("requests = %d, want %d", client.requests, DefaultChannelMaxIterations+2)
	}
	for _, unexpected := range []string{"Preparing stage summary", "建议下一步", "回复‘继续’", "回复“继续”"} {
		if strings.Contains(result.raw, unexpected) {
			t.Fatalf("stream unexpectedly contains checkpoint text %q:\n%s", unexpected, result.raw)
		}
	}
}

func TestWebUIDoesNotApplyChannelTurnTimeout(t *testing.T) {
	previousTimeout := ChannelTurnTimeout()
	SetChannelTurnTimeout(time.Hour)
	defer SetChannelTurnTimeout(previousTimeout)

	client := &deadlineCheckingTurnClient{}
	service := NewService(ServiceOptions{
		WorkspaceRoot: t.TempDir(),
		Client:        client,
		SystemPrompt:  "You are a helpful assistant. Be concise.",
	})
	handler := NewHandler(HandlerOptions{Service: service, Channels: []Channel{NewWebUIChannel(service, true)}})
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	result := postWebUIChat(t, apiServer.URL, `{"message":"long task","turn_id":"turn-no-timeout"}`)
	if client.deadlineSeen {
		t.Fatal("WebUI model context inherited a channel or stage deadline")
	}
	if result.done.Reply != "completed without deadline" {
		t.Fatalf("reply = %q, want completion without deadline", result.done.Reply)
	}
}

func TestWebUIModelCommandReturnsUpdatedRuntimeModel(t *testing.T) {
	service := NewService(ServiceOptions{
		WorkspaceRoot: t.TempDir(),
		APIType:       agent.APITypeOpenAI,
		Model:         "default-model",
		Models:        []string{"fast=selected-model"},
		SystemPrompt:  "base prompt",
	})
	handler := NewHandler(HandlerOptions{Service: service, Channels: []Channel{NewWebUIChannel(service, true)}})
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	switchResult := postWebUIChat(t, apiServer.URL, `{"message":"/model fast"}`)
	if switchResult.done.Model != "selected-model" || switchResult.done.APIType != string(agent.APITypeOpenAI) {
		t.Fatalf("switch done event = %#v, want openai selected-model", switchResult.done)
	}
	if switchResult.done.Generation != nil {
		t.Fatalf("model command generation = %#v, want nil", switchResult.done.Generation)
	}

	resetBody := fmt.Sprintf(`{"session_id":%q,"message":"/model default"}`, switchResult.done.SessionID)
	resetResult := postWebUIChat(t, apiServer.URL, resetBody)
	if resetResult.done.Model != "default-model" {
		t.Fatalf("reset done model = %q, want default-model", resetResult.done.Model)
	}
}

func TestDecodeWebUIChatRequestReasoningEffort(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		want agent.ReasoningEffort
	}{
		{name: "omitted", raw: "", want: agent.ReasoningEffortAuto},
		{name: "auto", raw: "auto", want: agent.ReasoningEffortAuto},
		{name: "low", raw: "low", want: agent.ReasoningEffortLow},
		{name: "medium", raw: "medium", want: agent.ReasoningEffortMedium},
		{name: "high", raw: "high", want: agent.ReasoningEffortHigh},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"message":"hi","reasoning_effort":%q}`, test.raw)
			request := httptest.NewRequest(http.MethodPost, "/api/v1/webui/chat", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			turn, err := decodeWebUIChatRequest(httptest.NewRecorder(), request)
			if err != nil {
				t.Fatalf("decode returned error: %v", err)
			}
			if turn.ReasoningEffort != test.want {
				t.Fatalf("reasoning effort = %q, want %q", turn.ReasoningEffort, test.want)
			}
		})
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/webui/chat", strings.NewReader(`{"message":"hi","reasoning_effort":"xhigh"}`))
	request.Header.Set("Content-Type", "application/json")
	_, err := decodeWebUIChatRequest(httptest.NewRecorder(), request)
	requestErr, ok := err.(*webUIRequestError)
	if !ok || requestErr.Status != http.StatusBadRequest {
		t.Fatalf("invalid effort error = %#v, want HTTP 400 request error", err)
	}
}

func TestDecodeWebUIChatRequestMode(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want ChatMode
	}{
		{raw: `{"message":"hi"}`, want: ""},
		{raw: `{"message":"hi","mode":"run"}`, want: ChatModeRun},
		{raw: `{"message":"hi","mode":"agent"}`, want: ChatModeRun},
		{raw: `{"message":"hi","mode":"ask"}`, want: ChatModeAsk},
	} {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/webui/chat", strings.NewReader(test.raw))
		request.Header.Set("Content-Type", "application/json")
		turn, err := decodeWebUIChatRequest(httptest.NewRecorder(), request)
		if err != nil {
			t.Fatalf("decode %s returned error: %v", test.raw, err)
		}
		if turn.Mode != test.want {
			t.Fatalf("decode %s mode = %q, want %q", test.raw, turn.Mode, test.want)
		}
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/webui/chat", strings.NewReader(`{"message":"hi","mode":"write"}`))
	request.Header.Set("Content-Type", "application/json")
	_, err := decodeWebUIChatRequest(httptest.NewRecorder(), request)
	requestErr, ok := err.(*webUIRequestError)
	if !ok || requestErr.Status != http.StatusBadRequest {
		t.Fatalf("invalid mode error = %#v, want HTTP 400 request error", err)
	}
}

func TestDecodeWebUIChatRequestAcceptsUploadedAndWorkspaceFiles(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("hello attachment"))
	body := fmt.Sprintf(`{"files":[{"name":"notes.txt","data_base64":%q},{"path":"docs/design.md"}]}`, encoded)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/webui/chat", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")

	turn, err := decodeWebUIChatRequest(httptest.NewRecorder(), request)
	if err != nil {
		t.Fatalf("decode returned error: %v", err)
	}
	if len(turn.Files) != 2 {
		t.Fatalf("files = %#v, want 2", turn.Files)
	}
	if turn.Files[0].Name != "notes.txt" || string(turn.Files[0].Data) != "hello attachment" {
		t.Fatalf("uploaded file = %#v", turn.Files[0])
	}
	if turn.Files[1].Path != "docs/design.md" {
		t.Fatalf("path file = %#v", turn.Files[1])
	}
}

func TestDecodeWebUIChatRequestRejectsInvalidFilePayloads(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "missing source", body: `{"files":[{"name":"notes.txt"}]}`},
		{name: "both sources", body: `{"files":[{"name":"notes.txt","data_base64":"","path":"notes.txt"}]}`},
		{name: "invalid base64", body: `{"files":[{"name":"notes.txt","data_base64":"***"}]}`},
		{name: "unknown field", body: `{"files":[{"name":"notes.txt","data_base64":"","extra":true}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/webui/chat", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			if _, err := decodeWebUIChatRequest(httptest.NewRecorder(), request); err == nil {
				t.Fatal("decode returned nil error")
			}
		})
	}
}

func TestDecodeWebUIChatRequestAcceptsFileOnlyAndEnforcesLimit(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/webui/chat", strings.NewReader(`{"files":[{"name":"empty.txt","data_base64":""}]}`))
	request.Header.Set("Content-Type", "application/json")
	turn, err := decodeWebUIChatRequest(httptest.NewRecorder(), request)
	if err != nil {
		t.Fatalf("file-only decode returned error: %v", err)
	}
	if len(turn.Files) != 1 || turn.Files[0].Data == nil {
		t.Fatalf("file-only turn = %#v", turn)
	}

	tooLarge := base64.StdEncoding.EncodeToString(make([]byte, maxWebUIFileBytes+1))
	request = httptest.NewRequest(http.MethodPost, "/api/v1/webui/chat", strings.NewReader(fmt.Sprintf(`{"files":[{"name":"large.bin","data_base64":%q}]}`, tooLarge)))
	request.Header.Set("Content-Type", "application/json")
	if _, err := decodeWebUIChatRequest(httptest.NewRecorder(), request); err == nil {
		t.Fatal("oversized file decode returned nil error")
	}
}

func TestWebUIStopCancelsActiveTurn(t *testing.T) {
	root := t.TempDir()
	client := &cancelAwareTurnClient{started: make(chan struct{}), canceled: make(chan struct{})}
	service := newTestService(root, "http://example.invalid")
	service.client = client
	channel := NewWebUIChannel(service, true)
	handler := NewHandler(HandlerOptions{Service: service, Channels: []Channel{channel}})
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	type streamResult struct {
		body string
		err  error
	}
	streamDone := make(chan streamResult, 1)
	go func() {
		response, err := http.Post(apiServer.URL+"/api/v1/webui/chat", "application/json", strings.NewReader(`{"message":"wait","turn_id":"turn-stop-1"}`))
		if err != nil {
			streamDone <- streamResult{err: err}
			return
		}
		defer response.Body.Close()
		payload, readErr := io.ReadAll(response.Body)
		streamDone <- streamResult{body: string(payload), err: readErr}
	}()

	select {
	case <-client.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for LLM request to start")
	}

	stopResponse, err := http.Post(apiServer.URL+"/api/v1/chat/stop", "application/json", strings.NewReader(`{"turn_id":"turn-stop-1"}`))
	if err != nil {
		t.Fatalf("POST stop failed: %v", err)
	}
	defer stopResponse.Body.Close()
	var stopPayload struct {
		Stopped bool `json:"stopped"`
	}
	if err := json.NewDecoder(stopResponse.Body).Decode(&stopPayload); err != nil {
		t.Fatalf("decode stop response: %v", err)
	}
	if !stopPayload.Stopped {
		t.Fatal("stop response reported no active turn")
	}

	select {
	case <-client.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for LLM request cancellation")
	}
	select {
	case result := <-streamDone:
		if result.err != nil {
			t.Fatalf("read stopped stream: %v", result.err)
		}
		if !strings.Contains(result.body, "event: stopped") {
			t.Fatalf("stream missing stopped event:\n%s", result.body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stopped stream to finish")
	}

	idleResponse, err := http.Post(apiServer.URL+"/api/v1/chat/stop", "application/json", strings.NewReader(`{"turn_id":"turn-stop-1"}`))
	if err != nil {
		t.Fatalf("POST idle stop failed: %v", err)
	}
	defer idleResponse.Body.Close()
	stopPayload.Stopped = true
	if err := json.NewDecoder(idleResponse.Body).Decode(&stopPayload); err != nil {
		t.Fatalf("decode idle stop response: %v", err)
	}
	if stopPayload.Stopped {
		t.Fatal("completed turn still reported as active")
	}
}

func TestWebUIWorkspaceList(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceTestFile := func(relative string, data []byte) {
		t.Helper()
		absolute := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
			t.Fatalf("create parent for %s: %v", relative, err)
		}
		if err := os.WriteFile(absolute, data, 0o644); err != nil {
			t.Fatalf("write %s: %v", relative, err)
		}
	}
	writeWorkspaceTestFile(".git/secret", []byte("hidden"))
	writeWorkspaceTestFile(".agent/AGENT.md", []byte("visible agent config"))
	writeWorkspaceTestFile(".env", []byte("VISIBLE_IN_EXPLORER=true"))
	writeWorkspaceTestFile("docs/readme.txt", []byte("nested"))
	writeWorkspaceTestFile("root.txt", []byte("root"))
	for index := 0; index < webUIWorkspacePageSize+1; index++ {
		writeWorkspaceTestFile(fmt.Sprintf("many/%03d.txt", index), []byte("page"))
	}
	symlinkCreated := os.Symlink("docs", filepath.Join(root, "docs-link")) == nil

	service := newTestService(root, "http://example.invalid")
	handler := NewHandler(HandlerOptions{Service: service, Channels: []Channel{NewWebUIChannel(service, true)}})
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	var listing webUIWorkspaceListResponse
	headers := getWebUIJSON(t, apiServer.URL, "/api/v1/webui/workspace", &listing)
	if cacheControl := headers.Get("Cache-Control"); cacheControl != "no-store" {
		t.Fatalf("cache-control = %q, want no-store", cacheControl)
	}
	if listing.Path != "" {
		t.Fatalf("root listing path = %q, want empty", listing.Path)
	}
	entriesByName := make(map[string]webUIWorkspaceEntry)
	seenNonDirectory := false
	for _, entry := range listing.Entries {
		entriesByName[entry.Name] = entry
		if entry.Name == ".git" {
			t.Fatal("root listing exposed .git")
		}
		if entry.Type == "directory" {
			if seenNonDirectory {
				t.Fatalf("directory %q appeared after a non-directory", entry.Name)
			}
		} else {
			seenNonDirectory = true
		}
	}
	for _, visible := range []string{".agent", ".env", "docs", "many", "root.txt"} {
		if _, ok := entriesByName[visible]; !ok {
			t.Fatalf("root listing missing %q: %#v", visible, listing.Entries)
		}
	}
	if entry := entriesByName["root.txt"]; entry.Type != "file" || !entry.Attachable || entry.Size != 4 {
		t.Fatalf("root.txt entry = %#v, want attachable four-byte file", entry)
	}
	if symlinkCreated {
		if entry := entriesByName["docs-link"]; entry.Type != "symlink" || entry.Attachable {
			t.Fatalf("docs-link entry = %#v, want non-attachable symlink", entry)
		}
		if status := webUIRequestStatus(t, http.MethodGet, apiServer.URL+"/api/v1/webui/workspace?path=docs-link"); status != http.StatusBadRequest {
			t.Fatalf("symlink directory status = %d, want 400", status)
		}
	}

	var nested webUIWorkspaceListResponse
	getWebUIJSON(t, apiServer.URL, "/api/v1/webui/workspace?path="+url.QueryEscape("docs"), &nested)
	if nested.Path != "docs" || len(nested.Entries) != 1 || nested.Entries[0].Path != "docs/readme.txt" {
		t.Fatalf("nested listing = %#v", nested)
	}

	var firstPage webUIWorkspaceListResponse
	getWebUIJSON(t, apiServer.URL, "/api/v1/webui/workspace?path=many", &firstPage)
	if len(firstPage.Entries) != webUIWorkspacePageSize || firstPage.NextOffset == nil || *firstPage.NextOffset != webUIWorkspacePageSize {
		t.Fatalf("first page entries=%d next=%v", len(firstPage.Entries), firstPage.NextOffset)
	}
	var secondPage webUIWorkspaceListResponse
	getWebUIJSON(t, apiServer.URL, fmt.Sprintf("/api/v1/webui/workspace?path=many&offset=%d", *firstPage.NextOffset), &secondPage)
	if len(secondPage.Entries) != 1 || secondPage.NextOffset != nil || secondPage.Entries[0].Name != "250.txt" {
		t.Fatalf("second page = %#v", secondPage)
	}

	for _, test := range []struct {
		name   string
		target string
		status int
	}{
		{name: "git hidden", target: "/api/v1/webui/workspace?path=.git", status: http.StatusNotFound},
		{name: "parent traversal", target: "/api/v1/webui/workspace?path=..%2Foutside", status: http.StatusBadRequest},
		{name: "absolute drive", target: "/api/v1/webui/workspace?path=C%3A%2FWindows", status: http.StatusBadRequest},
		{name: "negative offset", target: "/api/v1/webui/workspace?offset=-1", status: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			if status := webUIRequestStatus(t, http.MethodGet, apiServer.URL+test.target); status != test.status {
				t.Fatalf("status = %d, want %d", status, test.status)
			}
		})
	}
	if status := webUIRequestStatus(t, http.MethodPost, apiServer.URL+"/api/v1/webui/workspace"); status != http.StatusMethodNotAllowed {
		t.Fatalf("POST workspace status = %d, want 405", status)
	}
}

func TestWebUIWorkspacePreview(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceTestFile := func(relative string, data []byte) {
		t.Helper()
		absolute := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
			t.Fatalf("create parent for %s: %v", relative, err)
		}
		if err := os.WriteFile(absolute, data, 0o644); err != nil {
			t.Fatalf("write %s: %v", relative, err)
		}
	}
	writeWorkspaceTestFile("notes.txt", []byte("hello workspace"))
	largeText := strings.Repeat("界", maxWebUIPreviewTextBytes/3+10)
	writeWorkspaceTestFile("large.txt", []byte(largeText))
	writeWorkspaceTestFile("binary.bin", []byte{0, 1, 2, 3, 4})
	pngData, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode PNG fixture: %v", err)
	}
	writeWorkspaceTestFile("pixel.png", pngData)
	oversizedPNG := make([]byte, maxWebUIPreviewImageBytes+1)
	copy(oversizedPNG, pngData)
	writeWorkspaceTestFile("oversized.png", oversizedPNG)
	writeWorkspaceTestFile("folder/file.txt", []byte("nested"))
	writeWorkspaceTestFile(".git/secret.txt", []byte("hidden"))

	service := newTestService(root, "http://example.invalid")
	handler := NewHandler(HandlerOptions{Service: service, Channels: []Channel{NewWebUIChannel(service, true)}})
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	var textPreview webUIWorkspacePreviewResponse
	headers := getWebUIJSON(t, apiServer.URL, "/api/v1/webui/workspace/preview?path=notes.txt", &textPreview)
	if headers.Get("Cache-Control") != "no-store" {
		t.Fatalf("cache-control = %q, want no-store", headers.Get("Cache-Control"))
	}
	if textPreview.PreviewType != "text" || textPreview.Content != "hello workspace" || textPreview.Truncated || !textPreview.Attachable {
		t.Fatalf("text preview = %#v", textPreview)
	}

	var largePreview webUIWorkspacePreviewResponse
	getWebUIJSON(t, apiServer.URL, "/api/v1/webui/workspace/preview?path=large.txt", &largePreview)
	if largePreview.PreviewType != "text" || !largePreview.Truncated || len(largePreview.Content) > maxWebUIPreviewTextBytes || !utf8.ValidString(largePreview.Content) {
		t.Fatalf("large preview type=%q truncated=%v bytes=%d valid=%v", largePreview.PreviewType, largePreview.Truncated, len(largePreview.Content), utf8.ValidString(largePreview.Content))
	}

	var imagePreview webUIWorkspacePreviewResponse
	getWebUIJSON(t, apiServer.URL, "/api/v1/webui/workspace/preview?path=pixel.png", &imagePreview)
	decodedImage, decodeErr := base64.StdEncoding.DecodeString(imagePreview.DataBase64)
	if imagePreview.PreviewType != "image" || imagePreview.MIMEType != "image/png" || decodeErr != nil || string(decodedImage) != string(pngData) {
		t.Fatalf("image preview type=%q mime=%q decodeErr=%v bytes=%d", imagePreview.PreviewType, imagePreview.MIMEType, decodeErr, len(decodedImage))
	}

	var binaryPreview webUIWorkspacePreviewResponse
	getWebUIJSON(t, apiServer.URL, "/api/v1/webui/workspace/preview?path=binary.bin", &binaryPreview)
	if binaryPreview.PreviewType != "binary" || binaryPreview.Reason == "" || binaryPreview.Content != "" || binaryPreview.DataBase64 != "" {
		t.Fatalf("binary preview = %#v", binaryPreview)
	}

	var oversizedPreview webUIWorkspacePreviewResponse
	getWebUIJSON(t, apiServer.URL, "/api/v1/webui/workspace/preview?path=oversized.png", &oversizedPreview)
	if oversizedPreview.PreviewType != "unavailable" || oversizedPreview.Reason == "" || oversizedPreview.Attachable {
		t.Fatalf("oversized preview = %#v", oversizedPreview)
	}

	for _, test := range []struct {
		name   string
		target string
		status int
	}{
		{name: "directory", target: "/api/v1/webui/workspace/preview?path=folder", status: http.StatusBadRequest},
		{name: "missing", target: "/api/v1/webui/workspace/preview?path=missing.txt", status: http.StatusNotFound},
		{name: "git hidden", target: "/api/v1/webui/workspace/preview?path=.git%2Fsecret.txt", status: http.StatusNotFound},
		{name: "outside", target: "/api/v1/webui/workspace/preview?path=..%2Foutside.txt", status: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			if status := webUIRequestStatus(t, http.MethodGet, apiServer.URL+test.target); status != test.status {
				t.Fatalf("status = %d, want %d", status, test.status)
			}
		})
	}
}

func getWebUIJSON(t *testing.T, baseURL, target string, destination any) http.Header {
	t.Helper()
	response, err := http.Get(baseURL + target)
	if err != nil {
		t.Fatalf("GET %s failed: %v", target, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("GET %s status = %d, want 200: %s", target, response.StatusCode, body)
	}
	if err := json.NewDecoder(response.Body).Decode(destination); err != nil {
		t.Fatalf("decode GET %s response: %v", target, err)
	}
	return response.Header.Clone()
}

func webUIRequestStatus(t *testing.T, method, target string) int {
	t.Helper()
	request, err := http.NewRequest(method, target, nil)
	if err != nil {
		t.Fatalf("create %s request: %v", method, err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("%s %s failed: %v", method, target, err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode
}

type deadlineCheckingTurnClient struct {
	deadlineSeen bool
}

func (client *deadlineCheckingTurnClient) CreateChatCompletion(ctx context.Context, _ string, _ []map[string]any, _ []tools.Definition) (agent.AssistantMessage, error) {
	return client.complete(ctx)
}

func (client *deadlineCheckingTurnClient) CreateChatCompletionStream(ctx context.Context, _ string, _ []map[string]any, _ []tools.Definition, _ func(string)) (agent.AssistantMessage, error) {
	return client.complete(ctx)
}

func (client *deadlineCheckingTurnClient) complete(ctx context.Context) (agent.AssistantMessage, error) {
	if _, ok := ctx.Deadline(); ok {
		client.deadlineSeen = true
		return agent.AssistantMessage{}, fmt.Errorf("unexpected WebUI deadline")
	}
	return agent.AssistantMessage{Content: "completed without deadline"}, nil
}

type cancelAwareTurnClient struct {
	started  chan struct{}
	canceled chan struct{}
}

func (client *cancelAwareTurnClient) CreateChatCompletion(ctx context.Context, _ string, _ []map[string]any, _ []tools.Definition) (agent.AssistantMessage, error) {
	return client.wait(ctx)
}

func (client *cancelAwareTurnClient) CreateChatCompletionStream(ctx context.Context, _ string, _ []map[string]any, _ []tools.Definition, _ func(string)) (agent.AssistantMessage, error) {
	return client.wait(ctx)
}

func (client *cancelAwareTurnClient) wait(ctx context.Context) (agent.AssistantMessage, error) {
	close(client.started)
	<-ctx.Done()
	close(client.canceled)
	return agent.AssistantMessage{}, ctx.Err()
}

type webUIResult struct {
	raw         string
	contentType string
	done        doneEvent
}

type doneEvent struct {
	SessionID  string             `json:"session_id"`
	Reply      string             `json:"reply"`
	APIType    string             `json:"api_type"`
	Model      string             `json:"model"`
	Generation *GenerationMetrics `json:"generation"`
}

func postWebUIChat(t *testing.T, baseURL, body string) webUIResult {
	t.Helper()
	response, err := http.Post(baseURL+"/api/v1/webui/chat", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/v1/webui/chat failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read stream failed: %v", err)
	}
	result := webUIResult{raw: string(payload), contentType: response.Header.Get("Content-Type")}
	result.done = parseDoneEvent(t, result.raw)
	return result
}

func parseDoneEvent(t *testing.T, raw string) doneEvent {
	t.Helper()
	idx := strings.Index(raw, "event: done")
	if idx < 0 {
		t.Fatalf("stream missing done event:\n%s", raw)
	}
	for _, line := range strings.Split(raw[idx:], "\n") {
		if strings.HasPrefix(line, "data: ") {
			var event doneEvent
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				t.Fatalf("failed to decode done payload: %v", err)
			}
			return event
		}
	}
	t.Fatalf("done event had no data line:\n%s", raw)
	return doneEvent{}
}

func streamDeltaJSON(content string) string {
	encoded, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{
			{"delta": map[string]string{"content": content}},
		},
	})
	return string(encoded)
}
