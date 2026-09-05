// Package doctor inspects system readiness without starting a conversation.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"bqagent/internal/extagent"
	"bqagent/internal/globalconfig"
	"bqagent/internal/mcp"
)

type Check struct {
	ID        string    `json:"id"`
	Group     string    `json:"group"`
	State     string    `json:"state"`
	Reason    string    `json:"reason,omitempty"`
	Hint      string    `json:"hint,omitempty"`
	Source    string    `json:"source"`
	CheckedAt time.Time `json:"checked_at"`
	Required  bool      `json:"required"`
}

type Report struct {
	Ready     bool      `json:"ready"`
	Status    string    `json:"status"`
	CheckedAt time.Time `json:"checked_at"`
	Mode      string    `json:"mode"`
	Checks    []Check   `json:"checks"`
}

type Storage struct {
	ID, Path  string
	MustExist bool
}
type Options struct {
	Store         *globalconfig.Store
	WorkspaceRoot string
	Storage       []Storage
	MCPPaths      []string
	Getenv        func(string) string
	External      extagent.Config
	HTTPClient    *http.Client
	ACPFactory    extagent.ACPClientFactory
	// Snapshot returns sanitized status from an already-running broker.
	Snapshot func() ([]extagent.DetectionStatus, bool)
	Now      func() time.Time
}

type Engine struct {
	options   Options
	mu        sync.Mutex
	mcpStatus map[string]mcp.ServerStatus
	active    chan struct{}
}

var ErrProbeInProgress = errors.New("active diagnostic already in progress")

func New(options Options) *Engine {
	if options.Getenv == nil {
		options.Getenv = func(string) string { return "" }
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Engine{options: options, mcpStatus: make(map[string]mcp.ServerStatus), active: make(chan struct{}, 1)}
}

func (e *Engine) RecordMCP(status mcp.ServerStatus) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.mcpStatus[status.Name] = status
}

// Inspect reads configuration and snapshots. Active probes are bounded and isolated
// from the live tool catalog; no model request, tool call or channel message is sent.
func (e *Engine) Inspect(ctx context.Context, active bool) (Report, error) {
	if active {
		select {
		case e.active <- struct{}{}:
			defer func() { <-e.active }()
		default:
			return Report{}, ErrProbeInProgress
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	report := Report{Ready: true, Status: "healthy", CheckedAt: e.options.Now().UTC(), Mode: "snapshot", Checks: []Check{}}
	if active {
		report.Mode = "active"
	}
	add := func(group, id, state, reason, hint, source string, required bool) {
		report.Checks = append(report.Checks, Check{ID: id, Group: group, State: state, Reason: reason, Hint: hint, Source: source, Required: required, CheckedAt: report.CheckedAt})
	}
	store := e.options.Store
	configured := strings.TrimSpace(e.options.Getenv("LLM_API_KEY")) != "" || strings.TrimSpace(e.options.Getenv("OPENAI_API_KEY")) != "" || strings.TrimSpace(e.options.Getenv("ANTHROPIC_API_KEY")) != ""
	if store == nil {
		add("config", "global", "error", "configuration_unavailable", "Initialize the global configuration.", "local", true)
	} else {
		_, statErr := os.Stat(store.Path())
		cfg, err := store.Load()
		if statErr != nil || err != nil {
			add("config", "global", "error", "configuration_unreadable_or_invalid", "Check config.json existence, permissions and JSON syntax.", "local", true)
		} else if globalconfig.Validate(cfg) != nil {
			add("config", "global", "error", "invalid_provider_reference_or_fields", "Check provider IDs, required fields and active_provider.", "local", true)
		} else {
			add("config", "global", "available", "configuration_loaded", "", "local", true)
			for _, provider := range cfg.Providers {
				_, err := store.DecryptAPIKey(provider.APIKey)
				state, reason := "available", "credential_readable"
				if err != nil {
					state, reason = "error", "credential_decryption_failed"
				}
				add("config", "provider:"+provider.ID, state, reason, "Preserve the matching .config.key when moving configuration.", "local", false)
				if provider.ID == cfg.ActiveProvider && provider.DefaultModel != "" && err == nil {
					configured = true
				}
			}
		}
	}
	if configured {
		add("config", "model", "available", "configured_not_probed", "No model generation request was sent.", "local", false)
	} else {
		add("config", "model", "unverified", "model_not_configured", "Configure a Provider or model environment variables.", "local", false)
	}
	for _, storage := range e.options.Storage {
		state, reason := inspectStorage(ctx, storage.Path, active)
		if storage.MustExist {
			if _, err := os.Stat(storage.Path); err != nil {
				state, reason = "error", "required_directory_missing_or_inaccessible"
			}
		}
		add("storage", storage.ID, state, reason, "Snapshot checks do not verify writes; active checks use a temporary file in the nearest existing directory.", "local", true)
	}
	e.externalChecks(ctx, active, &report)
	e.mcpChecks(ctx, active, &report)
	report.Checks = append(report.Checks, LocalChannels(e.options.Getenv, report.CheckedAt)...)
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	Summarize(&report)
	return report, nil
}

func Summarize(report *Report) {
	report.Ready = true
	report.Status = "healthy"
	for _, check := range report.Checks {
		if check.State == "error" && check.Required {
			report.Ready = false
		}
		if check.State == "error" || check.State == "unverified" || check.State == "detecting" {
			report.Status = "degraded"
		}
	}
	if !report.Ready {
		report.Status = "not_ready"
	}
}

func inspectStorage(ctx context.Context, path string, active bool) (string, string) {
	if path == "" {
		return "error", "storage_path_missing"
	}
	existing := path
	for {
		info, err := os.Stat(existing)
		if err == nil {
			if !info.IsDir() {
				return "error", "not_a_directory"
			}
			if info.Mode().Perm()&0222 == 0 {
				return "error", "directory_not_writable"
			}
			dir, err := os.Open(existing)
			if err != nil {
				return "error", "directory_unreadable"
			}
			_, readErr := dir.Readdirnames(1)
			dir.Close()
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return "error", "directory_unreadable"
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "error", "directory_inaccessible"
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "error", "directory_missing"
		}
		existing = parent
	}
	if !active {
		return "available", "metadata_checked_write_unverified"
	}
	if ctx.Err() != nil {
		return "unverified", "cancelled"
	}
	file, err := os.CreateTemp(existing, ".bqagent-doctor-*")
	if err != nil {
		return "error", "write_probe_failed"
	}
	name := file.Name()
	_, writeErr := file.Write([]byte("doctor"))
	syncErr := file.Sync()
	closeErr := file.Close()
	removeErr := os.Remove(name)
	if writeErr != nil || syncErr != nil || closeErr != nil || removeErr != nil {
		return "error", "write_probe_or_cleanup_failed"
	}
	return "available", "temporary_write_verified"
}

func (e *Engine) externalChecks(ctx context.Context, active bool, report *Report) {
	var snapshots []extagent.DetectionStatus
	complete := false
	if e.options.Snapshot != nil {
		snapshots, complete = e.options.Snapshot()
	}
	lookup := make(map[extagent.AgentName]extagent.DetectionStatus)
	for _, status := range snapshots {
		lookup[status.Agent] = status
	}
	for _, name := range extagent.SupportedAgents() {
		cfg := e.options.External.Agents[name]
		check := Check{ID: string(name), Group: "external_agents", State: "unverified", Reason: "not_probed", Hint: "Verify the configured executable and ACP authentication.", Source: "local", CheckedAt: report.CheckedAt}
		if cfg.ACP.Command == "" && cfg.CLI.Command == "" && e.options.Snapshot == nil {
			check.State = "disabled"
			check.Reason = "not_configured"
			report.Checks = append(report.Checks, check)
			continue
		}
		if active {
			if !executable(cfg.CLI.Command, e.options.External.WorkspaceRoot) {
				cfg.CLI = extagent.CommandSpec{}
			}
			if !executable(cfg.ACP.Command, e.options.External.WorkspaceRoot) {
				cfg.ACP = extagent.CommandSpec{}
			}
			config := e.options.External
			config.Agents = map[extagent.AgentName]extagent.AgentConfig{name: cfg}
			// Detect ignores absent commands and only initializes ACP; it never sends a prompt.
			probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			result := extagent.Detect(probeCtx, config, e.options.ACPFactory)[name]
			probeErr := probeCtx.Err()
			cancel()
			if result.Preferred != nil {
				check.State = "available"
				check.Reason = "transport_" + string(result.Preferred.Kind)
				if result.CLIFallback {
					check.Reason = "cli_fallback_acp_failed"
				}
			} else {
				check.State = "error"
				check.Reason = "executable_or_acp_unavailable"
				if errors.Is(probeErr, context.DeadlineExceeded) {
					check.Reason = "acp_timeout"
				}
			}
			check.Source = "active"
		} else if e.options.Snapshot != nil {
			check.Source = "runtime"
			if !complete {
				check.State = "detecting"
				check.Reason = "background_detection"
			} else if result, ok := lookup[name]; ok {
				check.CheckedAt = result.CheckedAt
				check.State = "error"
				check.Reason = "transport_unavailable"
				if result.Available {
					check.State = "available"
					check.Reason = "transport_" + result.Transport
				}
				if result.Fallback {
					check.Reason = "cli_fallback_acp_failed"
				}
			}
		} else if !executable(cfg.ACP.Command, e.options.External.WorkspaceRoot) && !executable(cfg.CLI.Command, e.options.External.WorkspaceRoot) {
			check.State = "error"
			check.Reason = "executable_not_found"
		}
		report.Checks = append(report.Checks, check)
	}
}

func executable(command, root string) bool {
	if strings.TrimSpace(command) == "" {
		return false
	}
	if strings.ContainsAny(command, "/\\") && !filepath.IsAbs(command) {
		command = filepath.Join(root, command)
	}
	_, err := exec.LookPath(command)
	return err == nil
}

func (e *Engine) mcpChecks(ctx context.Context, active bool, report *Report) {
	cfg, err := mcp.LoadMergedConfig(e.options.MCPPaths, e.options.Getenv)
	if err != nil {
		report.Checks = append(report.Checks, Check{ID: "configuration", Group: "mcp", State: "error", Reason: "invalid_configuration", Hint: "Check mcp.json syntax and environment placeholders.", Source: "local", CheckedAt: report.CheckedAt})
		return
	}
	statuses := cfg.Status(e.options.Getenv)
	e.mu.Lock()
	saved := make(map[string]mcp.ServerStatus, len(e.mcpStatus))
	for key, value := range e.mcpStatus {
		saved[key] = value
	}
	e.mu.Unlock()
	if active {
		mcp.Probe(ctx, cfg, e.options.Getenv, e.options.HTTPClient, func(status mcp.ServerStatus) { saved[status.Name] = status })
	}
	if len(statuses) == 0 {
		report.Checks = append(report.Checks, Check{ID: "servers", Group: "mcp", State: "disabled", Reason: "no_servers_configured", Source: "local", CheckedAt: report.CheckedAt})
	}
	for _, status := range statuses {
		source := "local"
		if prior, ok := saved[status.Name]; ok && status.State == "unverified" {
			status = prior
			source = "runtime"
			if active {
				source = "active"
			}
		}
		report.Checks = append(report.Checks, Check{ID: status.Name, Group: "mcp", State: status.State, Reason: status.Reason, Hint: fmt.Sprintf("%d tools discovered. Check server transport, credentials and connectivity.", status.Tools), Source: source, CheckedAt: status.CheckedAt})
	}
}

func LocalChannels(getenv func(string) string, now time.Time) []Check {
	enabled := func(value string) bool {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "", "1", "true", "yes", "on":
			return true
		}
		return false
	}
	qqEnabled := true
	switch strings.ToLower(strings.TrimSpace(getenv("QQ_BOT_ENABLED"))) {
	case "0", "false", "no", "off":
		qqEnabled = false
	}
	flags := map[string]bool{"webui": enabled(getenv("WEBUI_ENABLED")), "serverchan": true, "qq": qqEnabled && getenv("QQ_BOT_APP_ID") != "" && getenv("QQ_BOT_CLIENT_SECRET") != "", "ilink": enabled(getenv("WEIXIN_ILINK_ENABLED"))}
	names := []string{}
	for name := range flags {
		names = append(names, name)
	}
	sort.Strings(names)
	checks := []Check{}
	for _, name := range names {
		state, reason := "unverified", "runtime_not_observed"
		if !flags[name] {
			state, reason = "disabled", "disabled"
		}
		checks = append(checks, Check{ID: name, Group: "channels", State: state, Reason: reason, Hint: "Live status is available from the running server. No login, reconnect or message is triggered.", Source: "local", CheckedAt: now})
	}
	return checks
}
