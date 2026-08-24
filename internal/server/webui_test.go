package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"bqagent/internal/agent"
	"bqagent/internal/session"
	"bqagent/internal/tools"
)

func TestWebUIServesIndex(t *testing.T) {
	root := t.TempDir()
	service := newTestService(root, "http://example.invalid")
	handler := NewHandler(HandlerOptions{Service: service, Channels: []Channel{NewWebUIChannel(service, true)}})
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	response, err := http.Get(apiServer.URL + "/")
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	if ct := response.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content-type = %q, want text/html", ct)
	}
	if cacheControl := response.Header.Get("Cache-Control"); cacheControl != "no-store" {
		t.Fatalf("cache-control = %q, want no-store", cacheControl)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body failed: %v", err)
	}
	if !strings.Contains(string(body), "/api/v1/webui/chat") {
		t.Fatal("served page does not reference the chat endpoint")
	}
	page := string(body)
	for _, expected := range []string{
		`id="theme-toggle"`,
		`--bg: #e6ecf5`,
		`--panel-strong: rgba(255, 255, 255, .88)`,
		`--text: #17222f`,
		`--signal: #2f6feb`,
		`--line: rgba(23, 51, 89, .13)`,
		`--select-bg: #0a1b32`,
		`.model-controls select option`,
		`function renderMarkdown(source)`,
		`class="table-wrap"`,
		`class="copy-code"`,
		`row.className = "message-actions"`,
		`.message-meta`,
		`function addMessageMeta(bubble, generation)`,
		`addMessageMeta(bubble, done.generation)`,
		`/api/v1/chat/stop`,
		`/api/v1/status`,
		`id="model-select"`,
		`id="provider-settings-backdrop"`,
		`class="global-settings" id="provider-settings-trigger"`,
		`font: 650 14px/1.2 ui-sans-serif`,
		`--font-body: 16px/1.6`,
		`--radius: 16px`,
		`id="provider-fetch-models"`,
		`/api/v1/webui/provider-models`,
		`id="conversation-sidebar"`,
		`id="conversation-list"`,
		`/api/v1/webui/conversations`,
		`function openConversation(id)`,
		`loadRuntimeModel()`,
		`"?session_id=" + encodeURIComponent(sessionId)`,
		`updateRuntimeModel(done.api_type, done.model, providerSettingsState.active_provider)`,
		`document.createElement("optgroup")`,
		`var finalReply = typeof done.reply === "string" ? done.reply : "";`,
		`服务端未返回有效回复`,
		`响应在完成事件到达前中断`,
		`class="stop-icon"`,
		`classList.add("is-streaming")`,
		`role="status"`,
		`status.setAttribute("aria-live", "polite")`,
		`status.setAttribute("aria-atomic", "true")`,
		`id="particle-field"`,
		`getContext("2d")`,
		`PARTICLE_MAX_COUNT = 80`,
		`PARTICLE_TARGET_FPS = 30`,
		`PARTICLE_TRAIL_MAX_POINTS = 8`,
		`PARTICLE_PULSE_MAX_COUNT = 3`,
		`Math.min(window.devicePixelRatio || 1, 1.5)`,
		`refreshParticlePalette`,
		`particleBuckets`,
		`function addParticleTrailPoint`,
		`function addParticlePulse`,
		`function drawParticleInteractionFeedback`,
		`event.button !== 0`,
		`window.addEventListener("pointerdown", handleParticlePointerDown, { passive: true });`,
		`pointer-events: none`,
		`requestAnimationFrame`,
		`cancelAnimationFrame`,
		`visibilitychange`,
		`(hover: hover) and (pointer: fine)`,
		`event.pointerType !== "mouse"`,
		`id="add-attachment"`,
		`id="file-input"`,
		`id="server-file-path"`,
		`var pendingFiles = []`,
		`files: sentFiles`,
		`id="reasoning-effort-toggle"`,
		`id="reasoning-effort-menu"`,
		`id="reasoning-effort-range"`,
		`bqagent.webui.reasoning-effort`,
		`reasoning_effort:`,
		`id="workspace-toggle"`,
		`id="workspace-sidebar"`,
		`--workspace-width: clamp(240px, 20.8vw, 336px)`,
		`order: 2`,
		`margin-right: calc(0px - var(--workspace-width))`,
		`inset: 0 0 0 auto`,
		`transform: translateX(104%)`,
		`id="workspace-tree"`,
		`id="workspace-preview-view"`,
		`id="workspace-preview-attach"`,
		`id="workspace-select"`,
		`id="workspace-create-agent"`,
		`id="workspace-picker-backdrop"`,
		`id="workspace-picker-root"`,
		`id="workspace-picker-confirm"`,
		`bqagent.webui.workspace-sessions`,
		`function initializeWorkspaceSelection()`,
		`function applyWorkspace(info, switching)`,
		`workspace_id: currentWorkspace ? currentWorkspace.id : ""`,
		`function renderWorkspaceTree()`,
		`function loadWorkspacePreview()`,
		`function addWorkspacePath(value, size)`,
		`/api/v1/webui/workspace?path=`,
		`/api/v1/webui/workspace/preview?path=`,
		`/api/v1/webui/workspaces/directories?root_id=`,
		`/api/v1/webui/workspaces/open`,
		`/api/v1/webui/workspace/config`,
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("served page missing WebUI feature %q", expected)
		}
	}
	if strings.Contains(page, "<script src=") {
		t.Fatal("served page should remain self-contained without external scripts")
	}
	for _, obsolete := range []string{"ambient-particles", "ambient-particle", "cursor-stars", "ambientDrift", "done.reply || streamed"} {
		if strings.Contains(page, obsolete) {
			t.Fatalf("served page still contains obsolete particle implementation %q", obsolete)
		}
	}
}

func TestWebUIComposerPlacesAttachmentButtonOnLeft(t *testing.T) {
	page := string(webUIIndex)
	attachment := strings.Index(page, `id="attachment-actions"`)
	content := strings.Index(page, `class="composer-content"`)
	if attachment < 0 || content < 0 || attachment > content {
		t.Fatalf("attachment action index = %d, composer content index = %d; want attachment action first", attachment, content)
	}
	for _, obsolete := range []string{`.composer::before {`, `padding-left: 22px`, `padding-left: 18px`} {
		if strings.Contains(page, obsolete) {
			t.Fatalf("composer still contains obsolete left prompt styling %q", obsolete)
		}
	}
	for _, expected := range []string{
		`.attachment-menu { position: absolute; left: 0;`,
		`.attachment-menu::after { content: ""; position: absolute; left: 11px;`,
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("composer missing left-aligned attachment styling %q", expected)
		}
	}
}

func TestWebUICreateAgentUsesDistinctAgentIcon(t *testing.T) {
	page := string(webUIIndex)
	selectIcon := `<svg viewBox="0 0 24 24" aria-hidden="true" fill="none"><path d="M3.5 7h6l2 2h9v9a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2zM12 12v5m-2.5-2.5h5"`
	agentIcon := `<svg class="workspace-create-agent-icon" viewBox="0 0 24 24" aria-hidden="true" fill="none">`
	if !strings.Contains(page, selectIcon) {
		t.Fatal("workspace selector folder icon is missing")
	}
	if !strings.Contains(page, agentIcon) {
		t.Fatal("create .agent action is missing its distinct agent icon")
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
	if !strings.Contains(string(webUIIndex), `model: option.dataset.model, session_id: sessionId`) {
		t.Fatal("WebUI model selector does not send the current session ID")
	}

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

func TestWebUISendWaitsForPendingModelSelection(t *testing.T) {
	page := string(webUIIndex)
	for _, expected := range []string{
		`var runtimeModelSelectionPromise = null;`,
		`modelSelect.addEventListener("change", beginRuntimeModelSelection);`,
		`var pendingModelSelection = runtimeModelSelectionPromise;`,
		`if (pendingModelSelection && !await pendingModelSelection)`,
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("WebUI model selection synchronization missing %q", expected)
		}
	}
	selectionWait := strings.Index(page, `var pendingModelSelection = runtimeModelSelectionPromise;`)
	chatRequest := strings.Index(page, `var res = await fetch("/api/v1/webui/chat"`)
	if selectionWait < 0 || chatRequest < 0 || selectionWait > chatRequest {
		t.Fatalf("model selection wait index = %d, chat request index = %d; want selection wait first", selectionWait, chatRequest)
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
		} `json:"messages"`
	}
	getWebUIJSON(t, apiServer.URL, "/api/v1/webui/conversations/"+saved.ID(), &history)
	if history.ID != saved.ID() || len(history.Messages) != 3 || history.Messages[0].Content != "请介绍项目" || history.Messages[1].Content != "这是项目说明。" || history.Messages[2].Content != "查看图片\n[图片]" {
		t.Fatalf("conversation history = %#v", history)
	}
}

func TestWebUILightThemeUsesSoftSurfacesAndVisibleParticles(t *testing.T) {
	page := string(webUIIndex)
	for _, expected := range []string{
		`--bg: #e6ecf5`,
		`--bg-soft: #f2f5fa`,
		`--panel: rgba(255, 255, 255, .70)`,
		`--panel-strong: rgba(255, 255, 255, .88)`,
		`--header-bg: rgba(240, 244, 250, .80)`,
		`--footer-bg: rgba(230, 236, 245, .94)`,
		`--code-bg: #f3f6fc`,
		`--bubble-user: rgba(224, 234, 251, .92)`,
		`--quote-bg: rgba(47, 111, 235, .07)`,
		`--table-head: rgba(47, 111, 235, .08)`,
		`--surface-subtle: rgba(47, 111, 235, .05)`,
		`--surface-hover: rgba(47, 111, 235, .09)`,
		`--surface-active: rgba(47, 111, 235, .13)`,
		`--line: rgba(23, 51, 89, .13)`,
		`--text: #17222f`,
		`--signal: #2f6feb`,
		`--particle-dot: rgba(38, 74, 128, .52)`,
		`--particle-static: rgba(38, 74, 128, .26)`,
		`--particle-strength: 1.7`,
		`--vignette-rgb: 33, 56, 92`,
		`--ambient-vignette: .09`,
		`--grid-opacity: .05`,
		`--scan-opacity: .012`,
	} {
		if count := strings.Count(page, expected); count != 2 {
			t.Fatalf("light theme token %q count = %d, want automatic and explicit definitions", expected, count)
		}
	}
	for _, expected := range []string{
		`--particle-strength: 1;`,
		`rgba(var(--vignette-rgb), var(--ambient-vignette))`,
		`function particleAlpha(base)`,
		`particlePalette.strength`,
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("page missing theme-aware particle rule %q", expected)
		}
	}
	for _, obsolete := range []string{
		`--particle-dot: transparent`,
		`--particle-static: transparent`,
		`--shadow: none`,
		`isLightTheme`,
		`:root[data-theme="light"] body { background: #fff; }`,
		`:root[data-theme="light"] #particle-field,`,
	} {
		if strings.Contains(page, obsolete) {
			t.Fatalf("light theme still flattens the page with %q", obsolete)
		}
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
		fmt.Fprintln(writer, `data: {"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":6,"total_tokens":9,"completion_tokens_details":{"reasoning_tokens":1}}}`)
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
