package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apptrace "bqagent/internal/trace"
)

type trackingCloser struct{ closed atomic.Int32 }

func (closer *trackingCloser) Close() error {
	closer.closed.Add(1)
	return nil
}

func TestWebUIWorkspaceRootsSplitUsingOSPathList(t *testing.T) {
	first := filepath.Join(string(filepath.Separator), "one")
	second := filepath.Join(string(filepath.Separator), "two")
	raw := first + string(os.PathListSeparator) + " " + second + " "
	got := SplitWorkspaceRoots(raw)
	if len(got) != 2 || got[0] != first || got[1] != second {
		t.Fatalf("SplitWorkspaceRoots(%q) = %#v, want [%q %q]", raw, got, first, second)
	}
}

func TestWebUIWorkspaceRegistryOpensExactRootAndCachesService(t *testing.T) {
	allowed := t.TempDir()
	defaultRoot := filepath.Join(allowed, "default")
	selectedRoot := filepath.Join(allowed, "projects", "demo")
	for _, directory := range []string{defaultRoot, selectedRoot} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	defaultService := newTestService(defaultRoot, "http://example.invalid")
	allowedCanonical, err := canonicalWorkspaceDirectory(allowed)
	if err != nil {
		t.Fatal(err)
	}
	selectedCanonical, err := canonicalWorkspaceDirectory(selectedRoot)
	if err != nil {
		t.Fatal(err)
	}
	defaultCloser := &trackingCloser{}
	var builds atomic.Int32
	createdCloser := &trackingCloser{}
	registry, err := NewWorkspaceRegistry(WorkspaceRegistryOptions{
		DefaultRoot: defaultRoot, DefaultService: defaultService, DefaultCloser: defaultCloser,
		AllowedRoots: []string{allowed},
		Factory: func(_ context.Context, root string) (*Service, io.Closer, error) {
			builds.Add(1)
			return newTestService(root, "http://example.invalid"), createdCloser, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var allowedRoot WorkspaceRootInfo
	for _, root := range registry.Roots() {
		if root.Path == allowedCanonical {
			allowedRoot = root
		}
	}
	if allowedRoot.ID == "" {
		t.Fatalf("allowed root not found: %#v", registry.Roots())
	}

	const callers = 8
	results := make(chan WorkspaceInfo, callers)
	errors := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			info, openErr := registry.Open(context.Background(), allowedRoot.ID, "projects/demo")
			results <- info
			errors <- openErr
		}()
	}
	group.Wait()
	close(results)
	close(errors)
	for openErr := range errors {
		if openErr != nil {
			t.Fatal(openErr)
		}
	}
	var selected WorkspaceInfo
	for info := range results {
		if selected.ID == "" {
			selected = info
		}
		if info.ID != selected.ID || info.Path != selectedCanonical {
			t.Fatalf("workspace info = %#v, want root %q and stable id %q", info, selectedCanonical, selected.ID)
		}
	}
	if builds.Load() != 1 {
		t.Fatalf("factory calls = %d, want 1", builds.Load())
	}
	service, _, err := registry.Resolve(selected.ID)
	if err != nil || service.workspaceRoot != selectedCanonical {
		t.Fatalf("resolved service root = %q, err=%v", service.workspaceRoot, err)
	}
	if _, err := os.Stat(filepath.Join(selectedRoot, ".agent", "AGENT.md")); err != nil {
		t.Fatalf("selected workspace was not initialized: %v", err)
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	if defaultCloser.closed.Load() != 1 || createdCloser.closed.Load() != 1 {
		t.Fatalf("closers called default=%d selected=%d, want once each", defaultCloser.closed.Load(), createdCloser.closed.Load())
	}
}

func TestWebUIWorkspaceRegistryRejectsTraversalAndSymlinks(t *testing.T) {
	allowed := t.TempDir()
	defaultRoot := filepath.Join(allowed, "default")
	outside := t.TempDir()
	if err := os.MkdirAll(defaultRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	registry, err := NewWorkspaceRegistry(WorkspaceRegistryOptions{
		DefaultRoot: defaultRoot, DefaultService: newTestService(defaultRoot, "http://example.invalid"),
		AllowedRoots: []string{allowed},
		Factory: func(_ context.Context, root string) (*Service, io.Closer, error) {
			return newTestService(root, "http://example.invalid"), nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var allowedRoot WorkspaceRootInfo
	for _, root := range registry.Roots() {
		if root.Path == allowed {
			allowedRoot = root
		}
	}
	if _, err := registry.Open(context.Background(), allowedRoot.ID, "../outside"); err == nil {
		t.Fatal("Open accepted parent traversal")
	}
	if err := os.Symlink(outside, filepath.Join(allowed, "link")); err == nil {
		if _, err := registry.Open(context.Background(), allowedRoot.ID, "link"); err == nil {
			t.Fatal("Open accepted symbolic link")
		}
	}
}

func TestWebUIWorkspaceSelectionRoutesFileExplorer(t *testing.T) {
	llmServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(writer, `data: {"choices":[{"delta":{"content":"selected reply"}}]}`)
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer llmServer.Close()
	allowed := t.TempDir()
	defaultRoot := filepath.Join(allowed, "default")
	selectedRoot := filepath.Join(allowed, "selected")
	for _, directory := range []string{defaultRoot, selectedRoot} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(defaultRoot, "default.txt"), []byte("default"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(selectedRoot, "selected.txt"), []byte("selected"), 0o644); err != nil {
		t.Fatal(err)
	}
	defaultService := newTestService(defaultRoot, llmServer.URL)
	allowedCanonical, err := canonicalWorkspaceDirectory(allowed)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewWorkspaceRegistry(WorkspaceRegistryOptions{
		DefaultRoot: defaultRoot, DefaultService: defaultService, AllowedRoots: []string{allowed},
		Factory: func(_ context.Context, root string) (*Service, io.Closer, error) {
			return newTestService(root, llmServer.URL), nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	channel := NewWebUIChannelWithWorkspaces(defaultService, registry, true)
	apiServer := httptest.NewServer(NewHandler(HandlerOptions{Service: defaultService, Workspaces: registry, Channels: []Channel{channel}}))
	defer apiServer.Close()

	var catalog webUIWorkspacesResponse
	getWebUIJSON(t, apiServer.URL, "/api/v1/webui/workspaces", &catalog)
	var allowedRoot WorkspaceRootInfo
	for _, root := range catalog.Roots {
		if root.Path == allowedCanonical {
			allowedRoot = root
		}
	}
	var directories webUIWorkspaceDirectoryResponse
	getWebUIJSON(t, apiServer.URL, "/api/v1/webui/workspaces/directories?root_id="+allowedRoot.ID, &directories)
	foundSelected := false
	for _, directory := range directories.Directories {
		if directory.Name == "selected" && directory.Path == "selected" {
			foundSelected = true
		}
	}
	if !foundSelected {
		t.Fatalf("directory picker response = %#v, want selected", directories.Directories)
	}
	requestBody := strings.NewReader(`{"root_id":"` + allowedRoot.ID + `","path":"selected"}`)
	response, err := http.Post(apiServer.URL+"/api/v1/webui/workspaces/open", "application/json", requestBody)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("open status=%d body=%s", response.StatusCode, body)
	}
	var selected WorkspaceInfo
	if err := json.NewDecoder(response.Body).Decode(&selected); err != nil {
		t.Fatal(err)
	}
	var listing webUIWorkspaceListResponse
	getWebUIJSON(t, apiServer.URL, "/api/v1/webui/workspace?workspace_id="+selected.ID, &listing)
	seen := map[string]bool{}
	for _, entry := range listing.Entries {
		seen[entry.Name] = true
	}
	if !seen["selected.txt"] || seen["default.txt"] {
		t.Fatalf("selected workspace listing = %#v", listing.Entries)
	}
	chat := postWebUIChat(t, apiServer.URL, `{"workspace_id":"`+selected.ID+`","message":"hello selected"}`)
	if chat.done.SessionID == "" {
		t.Fatal("selected workspace chat did not return a session")
	}
	if _, err := os.Stat(filepath.Join(selectedRoot, ".agent", "sessions", chat.done.SessionID, "meta.json")); err != nil {
		t.Fatalf("selected workspace session missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(defaultRoot, ".agent", "sessions", chat.done.SessionID, "meta.json")); !os.IsNotExist(err) {
		t.Fatalf("selected session leaked into default workspace: %v", err)
	}
	selectedService, _, err := registry.Resolve(selected.ID)
	if err != nil {
		t.Fatal(err)
	}
	turnContext, cancelTurn := context.WithCancel(context.Background())
	unregister, registered := selectedService.activeTurns.Register("selected-turn", cancelTurn)
	if !registered {
		t.Fatal("failed to register selected workspace turn")
	}
	defer unregister()
	stopResponse, err := http.Post(apiServer.URL+"/api/v1/chat/stop", "application/json", strings.NewReader(`{"turn_id":"selected-turn","workspace_id":"`+selected.ID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = stopResponse.Body.Close()
	select {
	case <-turnContext.Done():
	case <-time.After(time.Second):
		t.Fatal("workspace-scoped stop did not cancel selected turn")
	}
	selectedService.traceStore = apptrace.NewStore(selectedRoot)
	recorder, err := selectedService.traceStore.Create(chat.done.SessionID, "trace-turn", "", "agent", "test-model", "test prompt")
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.Finish("done", nil); err != nil {
		t.Fatal(err)
	}
	if status := webUIRequestStatus(t, http.MethodGet, apiServer.URL+"/api/v1/runs/"+recorder.RunID()+"?workspace_id="+selected.ID); status != http.StatusOK {
		t.Fatalf("workspace-scoped trace status = %d, want 200", status)
	}
	feedbackResponse, err := http.Post(apiServer.URL+"/api/v1/runs/"+recorder.RunID()+"/feedback?workspace_id="+selected.ID, "application/json", strings.NewReader(`{"rating":"up"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = feedbackResponse.Body.Close()
	if feedbackResponse.StatusCode != http.StatusOK {
		t.Fatalf("workspace-scoped feedback status = %d, want 200", feedbackResponse.StatusCode)
	}
	if status := webUIRequestStatus(t, http.MethodGet, apiServer.URL+"/api/v1/status?workspace_id=missing"); status != http.StatusNotFound {
		t.Fatalf("unknown workspace status = %d, want 404", status)
	}
}
