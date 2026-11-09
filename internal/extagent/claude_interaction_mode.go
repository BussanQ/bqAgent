package extagent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// One CLI child owns both the mode request and the prompt. Correlate the
// control reply before writing any part of the user's stream-json message.
type claudeModeSetup struct {
	ctx        context.Context
	cancel     context.CancelFunc
	abortChild func()
	requested  string
	done       chan struct{}
	err        error // published by closing done

	mu       sync.Mutex
	id, mode string
	response chan claudeControlReply
}

func newClaudeModeSetup(ctx context.Context, requested string, abortChild func()) *claudeModeSetup {
	probe, cancel := context.WithTimeout(ctx, 20*time.Second)
	return &claudeModeSetup{ctx: probe, cancel: cancel, requested: requested, abortChild: abortChild, done: make(chan struct{})}
}

// The stdout reader applies confirmed changes in wire order, before reading
// any later permission status. Other control responses have their own owners.
func (s *claudeModeSetup) receive(r claudeControlReply, modes *claudePlanMode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.ID != s.id || s.response == nil {
		return
	}
	if s.mode != "" && s.ctx.Err() == nil {
		if applied, err := claudeAcknowledgedMode(r, s.mode); err == nil {
			modes.observe("status", applied)
		}
	}
	s.response <- r
	s.response = nil
}

// Enter and return with the writer held. Other native controls (including
// approval replies) can use stdin while this request waits for stdout.
func (s *claudeModeSetup) request(ctx context.Context, c *claudeUsageControl, id, subtype, mode string) (claudeControlReply, error) {
	if err := ctx.Err(); err != nil {
		return claudeControlReply{}, err
	}
	ch := make(chan claudeControlReply, 1)
	s.mu.Lock()
	s.id, s.mode, s.response = id, mode, ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.id == id {
			s.response = nil
		}
		s.mu.Unlock()
	}()
	req := map[string]string{"subtype": subtype}
	if mode != "" {
		req["mode"] = mode
	}
	err := json.NewEncoder(c.input).Encode(map[string]any{"type": "control_request", "request_id": id, "request": req})
	if err != nil {
		return claudeControlReply{}, err
	}
	c.writeMu.Unlock()
	defer c.writeMu.Lock()
	select {
	case r := <-ch:
		return r, ctx.Err()
	case <-ctx.Done():
		return claudeControlReply{}, ctx.Err()
	}
}

func (s *claudeModeSetup) prepare(c *claudeUsageControl) (err error) {
	defer func() {
		s.err = err
		if err != nil {
			s.abortChild()
		}
		s.cancel()
		close(s.done)
	}()
	reply, err := s.request(s.ctx, c, c.initID, "initialize", "")
	if err != nil {
		return err
	}
	if reply.Subtype != "success" {
		return fmt.Errorf("Claude 未确认初始化，无法切换交互模式")
	}
	m := c.mode
	m.mu.Lock()
	if m.launch != "" && m.launch != "plan" {
		m.configured = m.launch
	}
	needsSettings := m.execution == "" && (m.launch == "" || m.launch == "plan")
	m.mu.Unlock()
	if needsSettings {
		// A recent CLI can report the merged settings from this workspace.
		// The plan itself doesn't depend on this optional restoration hint.
		probe, cancel := context.WithTimeout(s.ctx, 3*time.Second)
		r, readErr := s.request(probe, c, newClaudeUUID(), "get_settings", "")
		cancel()
		if err := s.ctx.Err(); err != nil {
			return err
		}
		var result struct {
			Effective *struct {
				Permissions struct {
					DefaultMode string `json:"defaultMode"`
				}
			}
		}
		known := readErr == nil && r.Subtype == "success" && json.Unmarshal(r.Response, &result) == nil && result.Effective != nil
		m.mu.Lock()
		if known {
			m.configured = result.Effective.Permissions.DefaultMode
		}
		hasPolicy := known || m.execution != "" || m.current == "plan" || m.launch == "plan"
		m.mu.Unlock()
		if !hasPolicy && s.requested == "default" {
			return fmt.Errorf("Claude 未提供此会话的执行权限设置，无法确认执行模式")
		}
	}
	mode := "plan"
	if s.requested == "default" {
		m.mu.Lock()
		mode, err = m.buildPolicy()
		m.mu.Unlock()
		if err != nil {
			return err
		}
	}
	r, err := s.request(s.ctx, c, newClaudeUUID(), "set_permission_mode", mode)
	if err != nil {
		return err
	}
	if _, err := claudeAcknowledgedMode(r, mode); err != nil {
		return err
	}
	m.mu.Lock()
	current := m.current
	m.mu.Unlock()
	if current != mode {
		return fmt.Errorf("Claude 的交互模式在发送前已改变")
	}
	return s.ctx.Err()
}
