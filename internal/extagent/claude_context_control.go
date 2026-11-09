package extagent

import (
	"encoding/json"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// A stream-json CLI can answer get_context_usage on the same child after its
// root result. Wrappers that need EOF still receive their entire original
// prompt once. Their turn proceeds without an optional report.
type claudeUsageControl struct {
	mu                                                        sync.Mutex
	writeMu                                                   sync.Mutex
	host                                                      *claudePromptHost
	setup                                                     *claudeModeSetup
	mode                                                      *claudePlanMode
	receivedRequest                                           bool
	input                                                     *os.File
	timer                                                     *time.Timer
	initID, reportID                                          string
	ready, working, sent, delivered, closed, releaseAfterUser bool
	session                                                   string
	report                                                    usageSink
	stopChild                                                 func() error
	probeTimer                                                *time.Timer
}

type claudeControlReply struct {
	ID       string `json:"request_id"`
	Subtype  string
	Response json.RawMessage
	Error    string
}

func openClaudeUsageControl(user io.Reader, report usageSink, stopChild func() error) (*os.File, *claudeUsageControl, error) {
	return openClaudeControl(user, report, stopChild, nil)
}

func openClaudeControl(user io.Reader, report usageSink, stopChild func() error, host *claudePromptHost) (*os.File, *claudeUsageControl, error) {
	return openClaudeTurnControl(user, report, stopChild, host, nil, "")
}

func openClaudeTurnControl(user io.Reader, report usageSink, stopChild func() error, host *claudePromptHost, setup *claudeModeSetup, launch string) (*os.File, *claudeUsageControl, error) {
	in, out, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	c := &claudeUsageControl{input: out, report: report, stopChild: stopChild, host: host, initID: newClaudeUUID(), reportID: newClaudeUUID()}
	c.setup = setup
	if host != nil {
		c.mode = &host.mode
	} else if setup != nil {
		c.mode = &claudePlanMode{launch: launch}
	}
	c.writeMu.Lock()
	go func() {
		var err error
		if setup == nil {
			err = c.writeRequest(c.initID, "initialize", "")
		} else {
			err = setup.prepare(c)
		}
		if err == nil {
			_, err = io.Copy(out, user)
		}
		c.writeMu.Unlock()
		c.mu.Lock()
		defer c.mu.Unlock()
		c.delivered = true
		if err != nil || c.releaseAfterUser {
			c.closeLocked()
			return
		}
		if c.session != "" {
			c.requestLocked()
			return
		}
		// Never close the pipe on a timer while an image or prompt is still
		// being written. A slow-starting CLI can fall back to estimates.
		if !c.closed && !c.keepInputOpen() {
			c.timer = time.AfterFunc(3*time.Second, func() {
				c.mu.Lock()
				defer c.mu.Unlock()
				if !c.keepInputOpen() && !c.sent {
					c.closeLocked()
				}
			})
		}
	}()
	return in, c, nil
}

func (c *claudeUsageControl) keepInputOpen() bool {
	return c.receivedRequest || c.ready && (c.working || c.host != nil || c.setup != nil)
}

func (c *claudeUsageControl) writeRequest(id, subtype, detail string) error {
	req := map[string]string{"subtype": subtype}
	if detail != "" {
		req["detail"] = detail
	}
	return json.NewEncoder(c.input).Encode(map[string]any{"type": "control_request", "request_id": id, "request": req})
}

func (c *claudeUsageControl) closeLocked() {
	if c.timer != nil {
		c.timer.Stop()
	}
	if !c.closed {
		c.closed = true
		_ = c.input.Close()
	}
}

func (c *claudeUsageControl) close() { c.mu.Lock(); defer c.mu.Unlock(); c.closeLocked() }

func (c *claudeUsageControl) release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.releaseAfterUser = true
	if c.delivered {
		c.closeLocked()
	}
}

func (c *claudeUsageControl) finish() bool {
	if c.setup != nil {
		c.setup.cancel()
	}
	if c.host != nil {
		c.host.end()
	}
	c.mu.Lock()
	c.closeLocked()
	if c.probeTimer != nil {
		c.probeTimer.Stop()
		c.probeTimer = nil
	}
	sent := c.sent
	c.mu.Unlock()
	if c.host != nil {
		c.host.workers.Wait()
	}
	return sent
}

func (c *claudeUsageControl) activity() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.working = true
	if c.ready && c.timer != nil && !c.sent {
		c.timer.Stop()
	}
}

func (c *claudeUsageControl) result(id string) {
	if c.host != nil {
		c.host.end()
	}
	if id == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != "" {
		return
	}
	c.session = id
	if c.delivered {
		c.requestLocked()
	}
}

func (c *claudeUsageControl) requestLocked() {
	if c.closed || c.sent {
		return
	}
	if !c.ready || c.report == nil {
		c.closeLocked()
		return
	}
	c.sent = true
	if c.timer != nil {
		c.timer.Stop()
	}
	if c.stopChild != nil {
		c.probeTimer = time.AfterFunc(5*time.Second, func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.probeTimer == nil {
				return
			}
			_ = c.stopChild()
		})
	}
	go func() {
		// Closing stdin after the line lets print mode exit even if the
		// optional control is unsupported. The report arrives on stdout.
		c.writeMu.Lock()
		_ = c.writeRequest(c.reportID, "get_context_usage", "summary")
		c.writeMu.Unlock()
		c.close()
	}()
}

func (c *claudeUsageControl) reply(reply claudeControlReply) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch reply.ID {
	case c.initID:
		c.ready = reply.Subtype == "success"
		if c.keepInputOpen() && c.timer != nil && !c.sent {
			c.timer.Stop()
		}
	case c.reportID:
		if c.probeTimer != nil {
			c.probeTimer.Stop()
			c.probeTimer = nil
		}
		if c.sent && reply.Subtype == "success" {
			if u, ok := parseClaudeContextReport(reply.Response); ok {
				c.report(c.session, u)
			}
		}
	}
}

func parseClaudeContextReport(raw json.RawMessage) (ContextUsage, bool) {
	var data struct {
		Model                   string
		TotalTokens             *int64
		Percentage              *float64
		RawMaxTokens, MaxTokens int64
		IsAutoCompactEnabled    bool
		AutoCompactThreshold    *int64
		Categories              []struct {
			Name       string
			Tokens     *int64
			IsDeferred bool
		}
	}
	if json.Unmarshal(raw, &data) != nil || strings.TrimSpace(data.Model) == "" || len([]rune(data.Model)) > 256 || data.TotalTokens == nil || *data.TotalTokens < 0 {
		return ContextUsage{}, false
	}
	window := data.RawMaxTokens
	if window <= 0 {
		window = data.MaxTokens
	}
	if window <= 0 {
		return ContextUsage{}, false
	}
	u := ContextUsage{Model: data.Model, Used: *data.TotalTokens, Window: window, Summary: true}
	if data.Percentage != nil && *data.Percentage >= 0 {
		u.Percentage = data.Percentage
	}
	if data.IsAutoCompactEnabled && data.AutoCompactThreshold != nil && *data.AutoCompactThreshold >= 0 {
		u.CompactAt = data.AutoCompactThreshold
	}
	for _, row := range data.Categories[:min(64, len(data.Categories))] {
		name := strings.Join(strings.Fields(row.Name), " ")
		if name != "" && len([]rune(name)) <= 80 && row.Tokens != nil && *row.Tokens > 0 {
			u.Categories = append(u.Categories, ContextCategory{Name: name, Tokens: *row.Tokens, Deferred: row.IsDeferred})
		}
	}
	slices.SortStableFunc(u.Categories, func(a, b ContextCategory) int {
		if a.Tokens > b.Tokens {
			return -1
		}
		if a.Tokens < b.Tokens {
			return 1
		}
		return 0
	})
	return u, true
}
