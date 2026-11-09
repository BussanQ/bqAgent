package extagent

import (
	"encoding/json"
	"strings"
)

type claudeContextReader struct {
	id, initModel, model string
	used                 int64
}

type claudeWindow struct {
	ContextWindow  int64
	CanonicalModel string
}

func claudeBaseModel(model string) string {
	base, _, _ := strings.Cut(strings.ToLower(model), "[")
	return base
}

func (c *claudeContextReader) observe(sink usageSink, id, kind, subtype, init string, message json.RawMessage, windows map[string]claudeWindow) {
	if sink == nil || id == "" || c.id != "" && c.id != id {
		return
	}
	if c.id == "" && !(kind == "system" && subtype == "init") {
		return
	}
	c.id = id
	emit := func(u ContextUsage) { sink(id, u) }
	switch {
	case kind == "system" && subtype == "init":
		c.initModel = init
	case kind == "conversation_reset" || kind == "system" && subtype == "compact_boundary":
		c.used = 0
		emit(ContextUsage{})
	case kind == "assistant":
		var m struct {
			Model string
			Usage struct {
				Input      int64 `json:"input_tokens"`
				CacheRead  int64 `json:"cache_read_input_tokens"`
				CacheWrite int64 `json:"cache_creation_input_tokens"`
			}
		}
		if json.Unmarshal(message, &m) != nil || m.Model == "" {
			return
		}
		used := m.Usage.Input + m.Usage.CacheRead + m.Usage.CacheWrite
		if used <= 0 {
			return
		}
		if c.model != "" && c.model != m.Model {
			emit(ContextUsage{})
		}
		c.model, c.used = m.Model, used
		if strings.HasSuffix(strings.ToLower(c.initModel), "[1m]") && claudeBaseModel(c.initModel) == claudeBaseModel(c.model) {
			emit(ContextUsage{Used: used, Window: 1000000, Estimated: true, Model: c.initModel})
		}
	case kind == "result" && c.used > 0:
		var capacity int64
		for key, w := range windows {
			if claudeBaseModel(key) == claudeBaseModel(c.model) || claudeBaseModel(w.CanonicalModel) == claudeBaseModel(c.model) {
				capacity = max(capacity, w.ContextWindow)
			}
		}
		if exact, ok := windows[c.initModel]; ok && exact.ContextWindow > 0 && claudeBaseModel(c.initModel) == claudeBaseModel(c.model) {
			capacity = exact.ContextWindow
		}
		if capacity > 0 {
			emit(ContextUsage{Used: c.used, Window: capacity, Estimated: true, Model: c.model})
		}
	}
}
