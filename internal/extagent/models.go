package extagent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
)

type ModelOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ModelCatalog struct {
	SupportsInteractionMode bool     `json:"supports_interaction_mode,omitempty"`
	InteractionModes        []string `json:"interaction_modes"`
	modeListMissing         bool
	InteractionMode         string                         `json:"interaction_mode,omitempty"`
	SupportsServiceTier     bool                           `json:"supports_service_tier,omitempty"`
	ServiceTiers            map[string][]ServiceTierOption `json:"service_tiers,omitempty"`
	SelectedServiceTier     string                         `json:"selected_service_tier,omitempty"`
	PreferredServiceTier    string                         `json:"preferred_service_tier,omitempty"`
	DefaultServiceTier      string                         `json:"default_service_tier,omitempty"`
	ContextUsage            *ContextUsage                  `json:"context_usage,omitempty"`
	SupportsCompact         bool                           `json:"supports_compact"`
	SupportsImages          bool                           `json:"supports_images"`
	Models                  []ModelOption                  `json:"models"`
	Selected                string                         `json:"selected,omitempty"`
	AllowCustom             bool                           `json:"allow_custom,omitempty"`
	DefaultModel            string                         `json:"default_model,omitempty"`
	SelectedEffort          string                         `json:"selected_effort,omitempty"`
	Efforts                 map[string]EffortMenu          `json:"efforts,omitempty"`
	SessionScopedEffort     bool                           `json:"session_scoped_effort,omitempty"`
}

type EffortMenu struct {
	Options []ModelOption `json:"options"`
	Default string        `json:"default,omitempty"`
}

func (m *ModelCatalog) setEfforts(model string, menu EffortMenu) {
	if model == "" {
		return
	}
	if m.Efforts == nil {
		m.Efforts = make(map[string]EffortMenu)
	}
	m.Efforts[model] = menu
}

func (m *EffortMenu) add(id, name string) {
	if validateModel(id) != nil || m.supports(id) {
		return
	}
	m.Options = append(m.Options, ModelOption{ID: id, Name: firstNonEmpty(name, id)})
}

func (m EffortMenu) supports(id string) bool {
	return slices.ContainsFunc(m.Options, func(option ModelOption) bool { return option.ID == id })
}

func (broker *Broker) optionsForTurn(ctx context.Context, req TurnRequest, state SessionState, kind TransportKind) (PromptOptions, error) {
	opts := PromptOptions{Model: state.Model, Effort: state.Effort}
	if err := validateInteractionMode(req.InteractionMode, req.Agent, kind); err != nil {
		return PromptOptions{}, err
	}
	opts.InteractionMode = req.InteractionMode
	if supportsInteractionMode(req.Agent, kind) {
		opts.savedInteractionMode = state.InteractionMode
	}
	if err := validateTierPick(req.ServiceTier, req.Agent, kind); err != nil {
		return PromptOptions{}, err
	}
	if kind == TransportAppServer && req.Agent == AgentCodex {
		opts.ServiceTier = firstNonEmpty(req.ServiceTier, state.PreferredServiceTier)
		opts.TierExplicit = req.ServiceTier != ""
		if req.InheritTierPreference && req.ServiceTier != "" {
			opts.PreservedTier = &state.PreferredServiceTier
		}
	}
	if req.Model != "" {
		if err := validateModel(req.Model); err != nil {
			return PromptOptions{}, err
		}
		opts.Model = req.Model
	}
	modelChanged := opts.Model != state.Model
	if modelChanged {
		opts.Effort = ""
	}
	if req.Effort != "" {
		if validateModel(req.Effort) != nil {
			return PromptOptions{}, fmt.Errorf("无效的外部 Agent 推理强度")
		}
		opts.Effort = req.Effort
	}
	// ACP checks refreshed config options on the actual session. Codex
	// remembers turn overrides in its thread, so replace an old override with
	// the new model's declared default on a model-only switch.
	restoreDefault := req.Agent == AgentCodex && modelChanged && state.Effort != "" && req.Effort == ""
	newPick := req.Effort != "" && (modelChanged || opts.Effort != state.Effort)
	if kind == TransportACP || (!newPick && !restoreDefault) {
		return opts, nil
	}
	listCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	menu, err := broker.Models(listCtx, req.Agent, req.BQSessionID, req.CWD)
	if err != nil {
		return PromptOptions{}, fmt.Errorf("读取推理强度：%w", err)
	}
	model := firstNonEmpty(opts.Model, menu.Selected, menu.DefaultModel)
	choices := menu.Efforts[model]
	if restoreDefault {
		if choices.supports(choices.Default) {
			opts.Effort = choices.Default
		}
	} else if !choices.supports(opts.Effort) {
		return PromptOptions{}, fmt.Errorf("%s 模型 %q 未提供推理强度 %q", req.Agent, model, opts.Effort)
	}
	return opts, nil
}

func validateModel(id string) error {
	if id == "" || len(id) > 512 || strings.TrimSpace(id) != id || strings.HasPrefix(id, "-") || strings.IndexFunc(id, unicode.IsControl) >= 0 {
		return fmt.Errorf("无效的外部 Agent 模型 ID")
	}
	return nil
}

func (m *ModelCatalog) add(id, name string) {
	if validateModel(id) != nil {
		return
	}
	for _, existing := range m.Models {
		if id == existing.ID {
			return
		}
	}
	m.Models = append(m.Models, ModelOption{ID: id, Name: firstNonEmpty(name, id)})
}

// A saved ACP session ID cannot be resumed as an App Server thread, or vice versa.
func transportForSession(d DetectionResult, state SessionState) (*AgentTransport, error) {
	t := d.Preferred
	if t == nil {
		return nil, fmt.Errorf("agent %q is unavailable", d.Agent)
	}
	if (state.ExternalSessionID == "" && state.ClaudeNewID == "") || state.Transport == "" || state.Transport == t.Kind {
		return t, nil
	}
	switch {
	case state.Transport == TransportACP:
		t = d.ACP
	case state.Transport == TransportCLI && t.Kind == TransportACP:
		t = d.CLI
	case state.Transport == TransportAppServer && t.Kind == TransportACP:
		t = d.AppServer
		if t == nil {
			t = d.CLI
		}
	}
	if t == nil || state.ForkBinding && t.Kind != state.Transport {
		return nil, fmt.Errorf("saved %s session requires %s transport; restore its configuration or reset the external session", state.Agent, state.Transport)
	}
	return t, nil
}

func (broker *Broker) Models(ctx context.Context, name AgentName, sessionID, cwd string) (ModelCatalog, error) {
	return broker.modelCatalog(ctx, name, sessionID, cwd, "")
}

// The native CLI/App Server inventory belongs to the launch configuration and
// workspace. Chats share advertised choices and supply their own selections.
type modelInventory struct {
	modeListMissing bool
	modes           []string
	cwd             string
	listed          time.Time
	models          []ModelOption
	tiers           map[string][]ServiceTierOption
}

func (broker *Broker) recentInventory(t *AgentTransport, cwd string) modelInventory {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	inventory := broker.modelInventories[t]
	if inventory.cwd != cwd || time.Since(inventory.listed) > 5*time.Minute {
		return modelInventory{}
	}
	inventory.models = slices.Clone(inventory.models)
	inventory.modes = slices.Clone(inventory.modes)
	inventory.tiers = cloneServiceTiers(inventory.tiers)
	return inventory
}

func (broker *Broker) rememberListing(t *AgentTransport, cwd string, started time.Time, menu *ModelCatalog, err error) {
	if t.Kind != TransportCLI && t.Kind != TransportAppServer {
		return
	}
	if err != nil {
		if len(menu.Models) == 0 {
			inventory := broker.recentInventory(t, cwd)
			menu.Models, menu.ServiceTiers, menu.InteractionModes = inventory.models, inventory.tiers, inventory.modes
			menu.modeListMissing = inventory.modeListMissing
		}
		return
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if !broker.modelInventories[t].listed.After(started) {
		broker.modelInventories[t] = modelInventory{cwd: cwd, listed: started, models: slices.Clone(menu.Models), tiers: cloneServiceTiers(menu.ServiceTiers), modes: slices.Clone(menu.InteractionModes), modeListMissing: menu.modeListMissing}
	}
}

// ModelCapabilities returns immediately after transport detection. A previous
// successful listing can populate the picker while the live request is pending.
func (broker *Broker) ModelCapabilities(ctx context.Context, name AgentName, sessionID, cwd string) (ModelCatalog, error) {
	t, state, err := broker.catalogTransport(ctx, name, sessionID)
	if err != nil {
		return ModelCatalog{}, err
	}
	inventory := broker.recentInventory(t, cwd)
	menu := ModelCatalog{Models: inventory.models, ServiceTiers: inventory.tiers, Selected: state.Model, SelectedEffort: state.Effort, InteractionModes: inventory.modes, modeListMissing: inventory.modeListMissing}
	menu.applyTransport(t)
	menu.applySessionOptions(t, state)
	if state.Transport == t.Kind {
		menu.ContextUsage = broker.readContextUsage(state)
	}
	return menu, nil
}

// Snapshot the target independently of the running agent. Taking its execution
// lock here would make enqueue wait for the very turn it is meant to follow.
func (broker *Broker) QueuedMessageState(ctx context.Context, name AgentName, id, model, effort, serviceTier string, hasImages bool) (SessionState, error) {
	return broker.QueuedMessageOptions(ctx, name, id, PromptOptions{Model: model, Effort: effort, ServiceTier: serviceTier}, hasImages)
}

func (broker *Broker) QueuedMessageOptions(ctx context.Context, name AgentName, id string, picks PromptOptions, hasImages bool) (SessionState, error) {
	t, state, err := broker.catalogTransport(ctx, name, id)
	if err != nil {
		return SessionState{}, err
	}
	if hasImages && !t.SupportsImages() {
		return SessionState{}, fmt.Errorf("%s 当前使用的 %s 接口不支持图片", name, t.Kind)
	}
	if err := validateTierPick(picks.ServiceTier, name, t.Kind); err != nil {
		return SessionState{}, err
	}
	if err := validateInteractionMode(picks.InteractionMode, name, t.Kind); err != nil {
		return SessionState{}, err
	}
	if supportsInteractionMode(name, t.Kind) {
		state.InteractionMode = firstNonEmpty(picks.InteractionMode, state.InteractionMode)
	} else {
		state.InteractionMode = ""
	}
	if t.Kind != TransportAppServer {
		state.ServiceTier, state.PreferredServiceTier = "", ""
	}
	// If the model changes without an explicit tier, resolve its support on
	// the actual thread at dispatch, before sending the prompt.
	if picks.Model != "" && picks.Model != state.Model || picks.ServiceTier != "" {
		state.ServiceTier = picks.ServiceTier
	}
	if picks.Model != "" {
		if err := validateModel(picks.Model); err != nil {
			return SessionState{}, err
		}
		if state.Model != picks.Model {
			state.Effort = ""
		}
		state.Model = picks.Model
	}
	if picks.Effort != "" {
		if err := validateModel(picks.Effort); err != nil {
			return SessionState{}, err
		}
		state.Effort = picks.Effort
	}
	return state, nil
}

func (m *ModelCatalog) applyTransport(t *AgentTransport) {
	m.AllowCustom = t.Kind != TransportACP
	m.SupportsImages = t.SupportsImages()
	m.SupportsCompact = compactSupported(t)
}

// PreviewModels reads the selected model's native settings. ACP may expose
// different config options only after selecting that model in a probe session.
func (broker *Broker) PreviewModels(ctx context.Context, name AgentName, sessionID, cwd, model string) (ModelCatalog, error) {
	if err := validateModel(model); err != nil {
		return ModelCatalog{}, err
	}
	return broker.modelCatalog(ctx, name, sessionID, cwd, model)
}

func (broker *Broker) catalogTransport(ctx context.Context, name AgentName, sessionID string) (*AgentTransport, SessionState, error) {
	var state SessionState
	if broker == nil {
		return nil, state, fmt.Errorf("外部 Agent 未配置")
	}
	if err := broker.waitForDetections(ctx); err != nil {
		return nil, state, err
	}
	if sessionID != "" && broker.store != nil {
		var err error
		state, err = broker.store.stateForAgent(sessionID, name)
		if err != nil {
			return nil, state, err
		}
	}
	t, err := transportForSession(broker.Detection(name), state)
	return t, state, err
}

func (broker *Broker) modelCatalog(ctx context.Context, name AgentName, sessionID, cwd, previewModel string) (ModelCatalog, error) {
	var menu ModelCatalog
	t, state, err := broker.catalogTransport(ctx, name, sessionID)
	if err != nil {
		return menu, err
	}
	started := time.Now()
	switch t.Kind {
	case TransportAppServer:
		var c SessionClient
		c, err = NewCodexAppServerClient(ctx, t.Command, cwd)
		if err == nil {
			defer c.Close()
			if err = c.Initialize(ctx); err == nil {
				rpc := c.(*codexAppServerClient).request
				menu, err = codexModelCatalog(ctx, rpc)
				if err == nil {
					readCodexModelDefaults(ctx, cwd, rpc, &menu)
					probe, cancel := context.WithTimeout(ctx, 2*time.Second)
					modes, modeErr := codexModeCatalog(probe, rpc)
					cancel()
					switch modeErr {
					case nil:
						menu.InteractionModes = modes
					case errNoCodexModeList:
						menu.InteractionModes, menu.modeListMissing = []string{}, true
					default:
						cached := broker.recentInventory(t, cwd)
						menu.InteractionModes, menu.modeListMissing = cached.modes, cached.modeListMissing
					}
				}
			}
		}
	case TransportACP:
		menu, err = broker.acpModelCatalog(ctx, t, sessionID, state.ExternalSessionID, cwd, previewModel)
		menu.SessionScopedEffort = true
	case TransportCLI:
		menu, err = cliModelCatalog(ctx, t, cwd)
	default:
		err = fmt.Errorf("unsupported transport %q", t.Kind)
	}
	broker.rememberListing(t, cwd, started, &menu, err)
	if previewModel == "" && state.Model != "" {
		if state.Model != firstNonEmpty(menu.Selected, menu.DefaultModel) {
			menu.SelectedEffort = ""
		}
		menu.Selected = state.Model
	}
	if previewModel == "" && state.Effort != "" {
		menu.SelectedEffort = state.Effort
	}
	menu.applyTransport(t)
	menu.applySessionOptions(t, state)
	if previewModel == "" && state.Transport == t.Kind {
		menu.ContextUsage = broker.readContextUsage(state)
	}
	return menu, err
}

func codexModelCatalog(ctx context.Context, request func(context.Context, string, any) (rpcEnvelope, error)) (ModelCatalog, error) {
	var menu ModelCatalog
	cursor := ""
	for page := 0; page < 50; page++ {
		params := map[string]any{"limit": 100, "includeHidden": false}
		if cursor != "" {
			params["cursor"] = cursor
		}
		msg, err := request(ctx, "model/list", params)
		if err != nil {
			return menu, err
		}
		var result struct {
			Data []struct {
				ID, Model, DisplayName    string
				Hidden                    bool
				IsDefault                 bool
				DefaultReasoningEffort    string
				SupportedReasoningEfforts []struct{ ReasoningEffort string }
				ServiceTiers              []ServiceTierOption
				AdditionalSpeedTiers      []string
			}
			NextCursor string
		}
		if err := json.Unmarshal(msg.Result, &result); err != nil {
			return menu, err
		}
		for _, row := range result.Data {
			if !row.Hidden {
				id := firstNonEmpty(row.Model, row.ID)
				menu.add(id, row.DisplayName)
				menu.addServiceTiers(id, row.ServiceTiers, row.AdditionalSpeedTiers)
				opts := EffortMenu{Default: row.DefaultReasoningEffort}
				for _, level := range row.SupportedReasoningEfforts {
					opts.add(level.ReasoningEffort, "")
				}
				menu.setEfforts(id, opts)
				if row.IsDefault {
					menu.DefaultModel = id
				}
			}
		}
		if result.NextCursor == "" {
			return menu, nil
		}
		if result.NextCursor == cursor {
			break
		}
		cursor = result.NextCursor
	}
	return menu, fmt.Errorf("Codex 模型目录分页未完成")
}

func readCodexModelDefaults(ctx context.Context, cwd string, rpc func(context.Context, string, any) (rpcEnvelope, error), menu *ModelCatalog) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	result, err := rpc(ctx, "config/read", map[string]string{"cwd": cwd})
	if err != nil {
		return
	}
	var settings struct {
		Config struct {
			Model       string
			Effort      string `json:"model_reasoning_effort"`
			ServiceTier string `json:"service_tier"`
		}
	}
	if json.Unmarshal(result.Result, &settings) != nil {
		return
	}
	if settings.Config.Model != "" {
		menu.DefaultModel = settings.Config.Model
	}
	if validateModel(settings.Config.ServiceTier) == nil {
		menu.DefaultServiceTier = settings.Config.ServiceTier
	}
	if opts := menu.Efforts[menu.DefaultModel]; opts.supports(settings.Config.Effort) {
		opts.Default = settings.Config.Effort
		menu.setEfforts(menu.DefaultModel, opts)
	}
}
