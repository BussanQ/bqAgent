package extagent

import (
	"context"
	"encoding/json"
	"time"
)

type piContextReader struct {
	id     string
	model  piModel
	report usageSink
	after  *int64
}

func (p *piContextReader) modelID() string {
	return p.model.optionID()
}

func (p *piContextReader) settledModel(model piModel) {
	id := model.optionID()
	if id == "" {
		return
	}
	if id != p.modelID() {
		p.after = nil
		p.model = model
		p.publish(nil, 0, false)
	} else if model.ContextWindow > 0 {
		p.model.ContextWindow = model.ContextWindow
	}
}

func (p *piContextReader) publish(used *int64, capacity int64, estimate bool) {
	u := ContextUsage{Model: p.modelID()}
	if used != nil && *used >= 0 && capacity > 0 {
		u = ContextUsage{Used: *used, Window: capacity, Estimated: estimate, Model: p.modelID()}
	}
	p.report(p.id, u)
}

func (p *piContextReader) observe(e piEvent) {
	switch e.Type {
	case "compaction_start", "auto_compaction_start":
		p.after = nil
		p.publish(nil, 0, false)
	case "compaction_end", "auto_compaction_end":
		p.compacted(e.Result)
	case "message_end":
		var m struct {
			Role, Provider, Model string
			Usage                 struct{ Input, CacheRead, CacheWrite, Output int64 }
		}
		if json.Unmarshal(e.Message, &m) != nil || m.Role != "assistant" {
			return
		}
		if m.Provider != "" && m.Provider != p.model.Provider || m.Model != "" && m.Model != p.model.ID {
			p.publish(nil, 0, false)
			return
		}
		used := m.Usage.Input + m.Usage.CacheRead + m.Usage.CacheWrite
		if used <= 0 {
			return
		}
		p.after = nil
		p.publish(&used, p.model.ContextWindow, true)
	}
}

func (p *piContextReader) compacted(raw json.RawMessage) {
	var result struct{ EstimatedTokensAfter *int64 }
	_ = json.Unmarshal(raw, &result)
	p.after = result.EstimatedTokensAfter
	p.publish(p.after, p.model.ContextWindow, true)
}

func (rpc piRPC) readContextStats(ctx context.Context) {
	if rpc.usage == nil {
		return
	}
	probe, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	data, err := rpc.call(probe, "get_session_stats", nil)
	var stats struct {
		SessionID    string
		ContextUsage *struct {
			Tokens        *int64
			ContextWindow int64
			Percent       *float64
		}
	}
	if err != nil || json.Unmarshal(data, &stats) != nil || stats.SessionID != "" && stats.SessionID != rpc.usage.id || stats.ContextUsage == nil || stats.ContextUsage.ContextWindow <= 0 {
		return
	}
	v := stats.ContextUsage
	if v.Tokens != nil {
		if *v.Tokens >= 0 {
			u := ContextUsage{Used: *v.Tokens, Window: v.ContextWindow, Model: rpc.usage.modelID()}
			if v.Percent != nil && *v.Percent >= 0 {
				u.Percentage = v.Percent
			}
			rpc.usage.report(rpc.usage.id, u)
		}
	} else {
		rpc.usage.publish(rpc.usage.after, v.ContextWindow, true)
	}
}
