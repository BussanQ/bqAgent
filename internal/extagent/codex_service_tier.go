package extagent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

const standardServiceTier = "default"

type ServiceTierOption struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

func validateTierPick(tier string, agent AgentName, transport TransportKind) error {
	if tier == "" {
		return nil
	}
	if agent != AgentCodex || transport != TransportAppServer {
		return fmt.Errorf("%s 当前接口不支持选择服务档位", agent)
	}
	if validateModel(tier) != nil {
		return fmt.Errorf("无效的 Codex 服务档位")
	}
	return nil
}

func (m *ModelCatalog) applySessionOptions(t *AgentTransport, state SessionState) {
	if supportsInteractionMode(t.Agent, t.Kind) {
		if t.Agent == AgentClaude {
			m.InteractionModes = []string{"default", "plan"}
		}
		if m.modeListMissing && state.InteractionMode != "" {
			m.InteractionModes = nil
		}
		m.SupportsInteractionMode, m.InteractionMode = m.InteractionModes == nil || len(m.InteractionModes) > 0, state.InteractionMode
	}
	if t.Kind == TransportAppServer && t.Agent == AgentCodex {
		m.SupportsServiceTier = true
		m.SelectedServiceTier, m.PreferredServiceTier = state.ServiceTier, state.PreferredServiceTier
	}
}

func hasServiceTier(rows []ServiceTierOption, id string) bool {
	return slices.ContainsFunc(rows, func(row ServiceTierOption) bool { return row.ID == id })
}

func cloneServiceTiers(tiers map[string][]ServiceTierOption) map[string][]ServiceTierOption {
	if tiers == nil {
		return nil
	}
	copied := make(map[string][]ServiceTierOption, len(tiers))
	for model, choices := range tiers {
		copied[model] = slices.Clone(choices)
	}
	return copied
}

func (m *ModelCatalog) addServiceTiers(model string, modern []ServiceTierOption, legacy []string) {
	if validateModel(model) != nil || modern == nil && legacy == nil {
		return
	}
	if m.ServiceTiers == nil {
		m.ServiceTiers = make(map[string][]ServiceTierOption)
	}
	if _, exists := m.ServiceTiers[model]; exists {
		return
	}
	if len(modern) == 0 {
		for _, id := range legacy {
			name := id
			if strings.EqualFold(name, "fast") {
				name = "Fast"
			}
			modern = append(modern, ServiceTierOption{ID: id, Name: name})
		}
	}
	choices := []ServiceTierOption{}
	for _, tier := range modern {
		if validateModel(tier.ID) == nil && strings.TrimSpace(tier.Name) != "" && !hasServiceTier(choices, tier.ID) {
			choices = append(choices, tier)
		}
	}
	m.ServiceTiers[model] = choices
}

// Missing means this version did not report a tier. Null means standard.
func openedServiceTier(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if string(raw) == "null" {
		return standardServiceTier
	}
	var id string
	if json.Unmarshal(raw, &id) == nil && validateModel(id) == nil {
		return id
	}
	return ""
}

func (c *codexAppServerClient) threadSettings(id string) SessionState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.threadOptions.ExternalSessionID == id {
		return c.threadOptions
	}
	return SessionState{Agent: AgentCodex, ExternalSessionID: id}
}

// Resolve on the opened thread's own connection. An explicit unsupported
// choice fails before turn/start. A saved choice can become usable again
// when the user returns to its model.
func (c *codexAppServerClient) turnServiceTier(ctx context.Context, opened SessionState, opts PromptOptions) (tier, preferred string, err error) {
	wanted := firstNonEmpty(opts.ServiceTier, opened.ServiceTier)
	if wanted == "" || wanted == standardServiceTier {
		return opts.ServiceTier, opts.ServiceTier, nil
	}
	model := firstNonEmpty(opts.Model, opened.Model)
	if wanted == opened.ServiceTier && model == opened.Model {
		return opts.ServiceTier, opts.ServiceTier, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	catalog, err := codexModelCatalog(ctx, c.request)
	if err != nil {
		return "", "", fmt.Errorf("读取 Codex 服务档位：%w", err)
	}
	rows, known := catalog.ServiceTiers[model]
	if hasServiceTier(rows, wanted) {
		return wanted, opts.ServiceTier, nil
	}
	if wanted == opened.ServiceTier {
		if !known {
			return wanted, opts.ServiceTier, nil
		}
		// A configured tier such as flex can be absent from the entire catalog.
		reportedOnly := true
		for _, tiers := range catalog.ServiceTiers {
			if hasServiceTier(tiers, wanted) {
				reportedOnly = false
				break
			}
		}
		if reportedOnly {
			return wanted, opts.ServiceTier, nil
		}
	}
	if opts.TierExplicit {
		return "", "", fmt.Errorf("Codex 模型 %q 未提供服务档位 %q", model, wanted)
	}
	if !known {
		return wanted, opts.ServiceTier, nil
	}
	return standardServiceTier, wanted, nil
}
