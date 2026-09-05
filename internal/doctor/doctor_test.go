package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bqagent/internal/extagent"
	"bqagent/internal/globalconfig"
	"bqagent/internal/mcp"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

type probeACP struct {
	extagent.ACPClient
	initialize func(context.Context) error
	closed     bool
}

func (client *probeACP) Initialize(ctx context.Context) error { return client.initialize(ctx) }
func (client *probeACP) Close() error                         { client.closed = true; return nil }

func TestDoctorACPProbeOnlyInitializesAndCloses(t *testing.T) {
	e := testEngine(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	client := &probeACP{initialize: func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 3*time.Second {
			t.Fatal("missing ACP timeout")
		}
		return nil
	}}
	e.options.External = extagent.Config{Agents: map[extagent.AgentName]extagent.AgentConfig{extagent.AgentClaude: {ACP: extagent.CommandSpec{Command: executable, Args: []string{"secret-argument"}}}}}
	e.options.ACPFactory = func(extagent.CommandSpec, string) (extagent.ACPClient, error) { return client, nil }
	report, err := e.Inspect(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !client.closed {
		t.Fatal("ACP process not closed")
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded), "secret-argument") {
		t.Fatal("command arguments leaked")
	}
	found := false
	for _, check := range report.Checks {
		if check.Group == "external_agents" && check.ID == "claude" && check.State == "available" {
			found = true
		}
	}
	if !found {
		t.Fatal(report)
	}
}

func TestDoctorReportsTimeoutWithoutRawRemoteError(t *testing.T) {
	e := testEngine(t)
	path := filepath.Join(filepath.Dir(e.options.Store.Path()), "mcp.json")
	os.WriteFile(path, []byte(`{"mcpServers":{"test":{"url":"https://secret-host.invalid"}}}`), 0600)
	e.options.MCPPaths = []string{path}
	e.options.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded })}
	report, err := e.Inspect(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded), "secret-host") {
		t.Fatal("raw endpoint leaked")
	}
	if !strings.Contains(string(encoded), "initialize_timeout") {
		t.Fatal(string(encoded))
	}
}

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func testEngine(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()
	store := globalconfig.NewStore(dir)
	if err := store.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	return New(Options{Store: store, Storage: []Storage{{ID: "global", Path: dir}}, Now: func() time.Time { return time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC) }})
}
func TestDoctorSnapshotDoesNotCreateOrProbe(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-created")
	e := New(Options{Store: globalconfig.NewStore(dir), Storage: []Storage{{ID: "global", Path: dir}}, HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("network probe"); return nil, nil })}})
	report, err := e.Inspect(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready || report.Status != "not_ready" {
		t.Fatalf("%+v", report)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("doctor created storage: %v", err)
	}
}
func TestDoctorReadinessAndCredentialRedaction(t *testing.T) {
	e := testEngine(t)
	report, err := e.Inspect(context.Background(), false)
	if err != nil || !report.Ready || report.Status != "degraded" {
		t.Fatalf("%+v %v", report, err)
	}
	cfg := globalconfig.Default()
	cfg.ActiveProvider = "p"
	cfg.Providers = []globalconfig.Provider{{ID: "p", Name: "p", Models: []string{"m"}, DefaultModel: "m", APIKey: globalconfig.Secret{Ciphertext: "do-not-leak"}}}
	if err := e.options.Store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	report, err = e.Inspect(context.Background(), false)
	if err != nil || !report.Ready {
		t.Fatalf("%+v %v", report, err)
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded), "admin123") || strings.Contains(string(encoded), "do-not-leak") {
		t.Fatal("secret leaked")
	}
	found := false
	for _, check := range report.Checks {
		if check.Reason == "credential_decryption_failed" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing credential failure")
	}
	if err := os.WriteFile(e.options.Store.Path(), []byte(`{"providers":`), 0600); err != nil {
		t.Fatal(err)
	}
	report, _ = e.Inspect(context.Background(), false)
	if report.Ready {
		t.Fatal("invalid config is ready")
	}
}
func TestDoctorActiveProbeIsIsolatedAndCleansStorage(t *testing.T) {
	e := testEngine(t)
	dir := filepath.Dir(e.options.Store.Path())
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"test":{"url":"https://example.invalid/mcp","headers":{"Authorization":"secret-value"}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	e.options.MCPPaths = []string{path}
	methods := []string{}
	e.options.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			t.Fatal("missing MCP deadline")
		}
		var request struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&request)
		methods = append(methods, request.Method)
		if request.Method == "tools/call" {
			t.Fatal("executed tool")
		}
		if request.ID == nil {
			return &http.Response{StatusCode: 202, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *request.ID, "result": map[string]any{"tools": []any{}}})
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(payload)))}, nil
	})}
	e.RecordMCP(mcp.ServerStatus{Name: "test", State: "error", Reason: "old_failure", CheckedAt: time.Now()})
	report, err := e.Inspect(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(methods, ",") != "initialize,notifications/initialized,tools/list" {
		t.Fatal(methods)
	}
	if !report.Ready {
		t.Fatal(report)
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".bqagent-doctor-") {
			t.Fatal("temporary file left behind")
		}
	}
	snapshot, _ := e.Inspect(context.Background(), false)
	found := false
	for _, check := range snapshot.Checks {
		if check.Group == "mcp" && check.Reason == "old_failure" {
			found = true
		}
	}
	if !found {
		t.Fatal("active probe overwrote live snapshot")
	}
}
func TestDoctorProbeCancellationAndConcurrency(t *testing.T) {
	e := testEngine(t)
	path := filepath.Join(filepath.Dir(e.options.Store.Path()), "mcp.json")
	os.WriteFile(path, []byte(`{"mcpServers":{"test":{"url":"https://example.invalid"}}}`), 0600)
	e.options.MCPPaths = []string{path}
	started := make(chan struct{})
	e.options.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := e.Inspect(ctx, true); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	if _, err := e.Inspect(context.Background(), true); !errors.Is(err, ErrProbeInProgress) {
		t.Fatalf("concurrent probe: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation stuck")
	}
}
func TestDoctorSummarizesOptionalAndDisabledChecks(t *testing.T) {
	report := Report{Checks: []Check{{State: "disabled"}}}
	Summarize(&report)
	if !report.Ready || report.Status != "healthy" {
		t.Fatal(report)
	}
	report.Checks = append(report.Checks, Check{State: "error"})
	Summarize(&report)
	if !report.Ready || report.Status != "degraded" {
		t.Fatal(report)
	}
	report.Checks = append(report.Checks, Check{State: "error", Required: true})
	Summarize(&report)
	if report.Ready || report.Status != "not_ready" {
		t.Fatal(report)
	}
}
