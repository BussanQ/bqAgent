package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sync"
	"time"
)

const (
	codeNotInitialized  = -32000
	codeBusy            = -32001
	codeSessionConflict = -32002
)

// Serve runs ACP version 1 until EOF, context cancellation, or an I/O failure.
// EOF returns nil; cancellation and I/O failures return their cause. It does not
// own input/output and never closes them. A generic Reader cannot be interrupted:
// at most one read goroutine can remain blocked until its Read returns. Serve
// itself never waits for that read or for a backend operation to honor cancellation.
func Serve(ctx context.Context, input io.Reader, output io.Writer, backend Backend) error {
	if input == nil || output == nil || backend == nil {
		return errors.New("ACP requires input, output, and backend")
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &server{
		ctx: ctx, cancel: cancel, backend: backend,
		sessions: make(map[string]*session), resources: make(map[*resource]struct{}),
	}
	s.writer = &wireWriter{output: output, ctx: ctx, cancel: cancel}
	defer s.shutdown()
	messages := make(chan incoming)
	go readLines(ctx, input, messages)
	for {
		select {
		case <-ctx.Done():
			if err := s.writer.failure(); err != nil {
				return err
			}
			return ctx.Err()
		case msg := <-messages:
			if ctx.Err() != nil {
				if err := s.writer.failure(); err != nil {
					return err
				}
				return ctx.Err()
			}
			if errors.Is(msg.err, errLineTooLong) {
				_ = s.writer.reply(json.RawMessage("null"), nil, &rpcError{-32600, msg.err.Error()})
				continue
			}
			if msg.err != nil {
				if errors.Is(msg.err, io.EOF) {
					return nil
				}
				return msg.err
			}
			req, rpcErr := parseRequest(msg.line)
			if rpcErr != nil {
				id := req.ID
				if len(id) == 0 {
					id = json.RawMessage("null")
				}
				_ = s.writer.reply(id, nil, rpcErr)
				continue
			}
			s.dispatch(req)
		}
	}
}

type resource struct {
	conv Conversation
	once sync.Once
}

func (r *resource) close() { r.once.Do(r.conv.Close) }

type session struct {
	resource *resource
	busy     bool
	turn     context.CancelFunc
}

type server struct {
	ctx         context.Context
	cancel      context.CancelFunc
	backend     Backend
	writer      *wireWriter
	initialized bool // only accessed by the reader loop

	mu        sync.Mutex
	closed    bool
	sessions  map[string]*session
	resources map[*resource]struct{}
}

func (s *server) shutdown() {
	s.cancel()
	s.mu.Lock()
	s.closed = true
	resources := make([]*resource, 0, len(s.resources))
	for r := range s.resources {
		resources = append(resources, r)
	}
	s.mu.Unlock()
	// Close may unblock a backend call. Do not let a broken Close or Prompt
	// prevent the transport from terminating indefinitely.
	done := make(chan struct{}, len(resources))
	for _, r := range resources {
		go func() { r.close(); done <- struct{}{} }()
	}
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	for range resources {
		select {
		case <-done:
		case <-timer.C:
			return
		}
	}
}

func (s *server) dispatch(req request) {
	if req.Method == "initialize" {
		s.initialize(req)
		return
	}
	if !s.initialized {
		_ = s.writer.reply(req.ID, nil, &rpcError{codeNotInitialized, "initialize is required"})
		return
	}
	switch req.Method {
	case "session/new", "session/load":
		if len(req.ID) != 0 {
			s.open(req)
		}
	case "session/prompt":
		if len(req.ID) != 0 {
			s.prompt(req)
		}
	case "session/cancel":
		if len(req.ID) != 0 {
			_ = s.writer.reply(req.ID, nil, &rpcError{-32600, "session/cancel must be a notification"})
			return
		}
		var p struct {
			SessionID string `json:"sessionId"`
		}
		if decodeParams(req.Params, &p) != nil || p.SessionID == "" {
			return
		}
		s.mu.Lock()
		if session := s.sessions[p.SessionID]; session != nil && session.turn != nil {
			session.turn()
		}
		s.mu.Unlock()
	case "session/set_model", "session/set_config_option":
		if len(req.ID) != 0 {
			s.setModel(req)
		}
	default:
		_ = s.writer.reply(req.ID, nil, &rpcError{-32601, "Method not found"})
	}
}

func (s *server) initialize(req request) {
	if len(req.ID) == 0 {
		return
	}
	var p struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if rpcErr := decodeParams(req.Params, &p); rpcErr != nil {
		_ = s.writer.reply(req.ID, nil, rpcErr)
		return
	}
	if p.ProtocolVersion < 1 {
		_ = s.writer.reply(req.ID, nil, invalidParams("protocolVersion must be a positive integer"))
		return
	}
	// Repeated initialize is idempotent and never resets sessions. Respond with
	// our supported version even when a client advertises a newer version.
	s.initialized = true
	_ = s.writer.reply(req.ID, map[string]any{
		"protocolVersion": 1,
		"agentInfo":       map[string]any{"name": "bqagent", "version": "1"},
		"agentCapabilities": map[string]any{
			"loadSession":        true,
			"promptCapabilities": map[string]any{"image": false, "audio": false, "embeddedContext": false},
			"mcpCapabilities":    map[string]any{"http": false, "sse": false},
		},
		"authMethods": []any{},
		"_meta":       map[string]any{"bqagent/manualPermissions": false},
	}, nil)
}

func (s *server) open(req request) {
	var p struct {
		CWD        string            `json:"cwd"`
		SessionID  string            `json:"sessionId"`
		MCPServers []json.RawMessage `json:"mcpServers"`
	}
	if rpcErr := decodeParams(req.Params, &p); rpcErr != nil {
		_ = s.writer.reply(req.ID, nil, rpcErr)
		return
	}
	load := req.Method == "session/load"
	if p.CWD == "" || (load && p.SessionID == "") || (!load && p.SessionID != "") {
		_ = s.writer.reply(req.ID, nil, invalidParams("cwd and, for load, sessionId are required"))
		return
	}
	if len(p.MCPServers) != 0 {
		_ = s.writer.reply(req.ID, nil, invalidParams("mcpServers are not supported"))
		return
	}
	var slot *session
	if load {
		s.mu.Lock()
		slot = s.sessions[p.SessionID]
		if slot != nil && slot.busy {
			s.mu.Unlock()
			_ = s.writer.reply(req.ID, nil, &rpcError{codeBusy, "Session is busy"})
			return
		}
		if slot == nil {
			slot = &session{}
			s.sessions[p.SessionID] = slot
		}
		slot.busy = true
		s.mu.Unlock()
	}
	go s.runOpen(req, p.CWD, p.SessionID, slot)
}

// own registers ownership before calling any conversation methods so shutdown
// also closes conversations whose ID, Models, or Replay is blocked.
func (s *server) own(conv Conversation) (*resource, *rpcError) {
	if conv == nil {
		return nil, internalError(errors.New("backend returned a nil conversation"))
	}
	r := &resource{conv: conv}
	s.mu.Lock()
	for existing := range s.resources {
		// A backend returning the same pointer twice must not cause us to close
		// the active conversation when rejecting a conflicting Open.
		if reflect.TypeOf(conv).Comparable() && existing.conv == conv {
			s.mu.Unlock()
			return nil, internalError(errors.New("backend returned an already open conversation"))
		}
	}
	if s.closed || s.ctx.Err() != nil {
		s.mu.Unlock()
		r.close()
		return nil, internalError(s.ctx.Err())
	}
	s.resources[r] = struct{}{}
	s.mu.Unlock()
	return r, nil
}

func (s *server) release(r *resource) {
	if r == nil {
		return
	}
	// Keep the resource registered while Close runs so shutdown can account for
	// it too. once prevents duplicate closes under that race.
	r.close()
	s.mu.Lock()
	delete(s.resources, r)
	s.mu.Unlock()
}

func (s *server) runOpen(req request, cwd, requestedID string, slot *session) {
	conv, err := s.backend.Open(s.ctx, cwd, requestedID)
	var r *resource
	var rpcErr *rpcError
	if err != nil {
		rpcErr = internalError(err)
		// Be defensive about a backend returning both a resource and an error.
		if conv != nil {
			if owned, _ := s.own(conv); owned != nil {
				s.release(owned)
			}
		}
	} else {
		r, rpcErr = s.own(conv)
	}
	id := requestedID
	if rpcErr == nil {
		id = conv.ID()
		if id == "" || (requestedID != "" && id != requestedID) {
			rpcErr = internalError(errors.New("backend returned an invalid session ID"))
		} else if requestedID == "" {
			s.mu.Lock()
			if s.sessions[id] != nil {
				rpcErr = &rpcError{codeSessionConflict, "Session ID already exists"}
			} else if !s.closed {
				slot = &session{busy: true}
				s.sessions[id] = slot
			}
			s.mu.Unlock()
		}
	}
	u := &updateStream{ctx: s.ctx, writer: s.writer, sessionID: id}
	if rpcErr == nil && requestedID != "" {
		if err := conv.Replay(s.ctx, u.emit); err != nil {
			rpcErr = internalError(err)
		}
	}
	var result any
	if rpcErr == nil {
		models := modelState(conv)
		result = map[string]any{"models": models, "configOptions": configOptions(models)}
		if requestedID == "" {
			result.(map[string]any)["sessionId"] = id
		}
	}
	var old *resource
	u.finish(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if slot != nil {
			if rpcErr == nil && !s.closed {
				old = slot.resource
				slot.resource = r
			} else if slot.resource == nil {
				delete(s.sessions, idForSlot(requestedID, id))
			}
			slot.busy = false
		}
		_ = s.writer.reply(req.ID, result, rpcErr)
	})
	if rpcErr != nil {
		s.release(r)
	}
	s.release(old)
}

func idForSlot(requestedID, actualID string) string {
	if requestedID != "" {
		return requestedID
	}
	return actualID
}

func (s *server) reserve(id string) (*session, *rpcError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	slot := s.sessions[id]
	if slot == nil {
		return nil, invalidParams("Unknown sessionId")
	}
	if slot.busy {
		return nil, &rpcError{codeBusy, "Session is busy"}
	}
	if slot.resource == nil {
		return nil, invalidParams("Unknown sessionId")
	}
	slot.busy = true
	return slot, nil
}

func (s *server) prompt(req request) {
	var p struct {
		SessionID string         `json:"sessionId"`
		Prompt    []ContentBlock `json:"prompt"`
	}
	if rpcErr := decodeParams(req.Params, &p); rpcErr != nil {
		_ = s.writer.reply(req.ID, nil, rpcErr)
		return
	}
	if p.SessionID == "" || p.Prompt == nil {
		_ = s.writer.reply(req.ID, nil, invalidParams("sessionId and prompt are required"))
		return
	}
	for _, block := range p.Prompt {
		if block.Type != "text" {
			_ = s.writer.reply(req.ID, nil, invalidParams("Only text content blocks are supported"))
			return
		}
	}
	slot, rpcErr := s.reserve(p.SessionID)
	if rpcErr != nil {
		_ = s.writer.reply(req.ID, nil, rpcErr)
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.mu.Lock()
	slot.turn = cancel
	s.mu.Unlock()
	go func() {
		defer cancel()
		u := &updateStream{ctx: ctx, writer: s.writer, sessionID: p.SessionID}
		stopReason, err := slot.resource.conv.Prompt(ctx, p.Prompt, u.emit)
		u.finish(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			var rpcErr *rpcError
			if ctx.Err() != nil {
				stopReason = "cancelled"
			} else if err != nil {
				rpcErr = internalError(err)
			} else if stopReason == "" {
				stopReason = "end_turn"
			}
			slot.turn, slot.busy = nil, false
			_ = s.writer.reply(req.ID, map[string]any{"stopReason": stopReason}, rpcErr)
		})
	}()
}

func modelState(conv Conversation) ModelState {
	state := conv.Models()
	if state.AvailableModels == nil {
		state.AvailableModels = []Model{}
	}
	return state
}

func configOptions(state ModelState) []any {
	options := make([]map[string]any, 0, len(state.AvailableModels))
	for _, model := range state.AvailableModels {
		option := map[string]any{"value": model.ModelID, "name": model.Name}
		if model.Description != "" {
			option["description"] = model.Description
		}
		options = append(options, option)
	}
	return []any{map[string]any{
		"id": "model", "name": "Model", "category": "model", "type": "select",
		"currentValue": state.CurrentModelID, "options": options,
	}}
}

func (s *server) setModel(req request) {
	var p struct {
		SessionID string `json:"sessionId"`
		ModelID   string `json:"modelId"`
		ConfigID  string `json:"configId"`
		Value     string `json:"value"`
	}
	if rpcErr := decodeParams(req.Params, &p); rpcErr != nil {
		_ = s.writer.reply(req.ID, nil, rpcErr)
		return
	}
	if req.Method == "session/set_config_option" {
		if p.ConfigID != "model" {
			_ = s.writer.reply(req.ID, nil, invalidParams("Only the model config option is supported"))
			return
		}
		p.ModelID = p.Value
	}
	if p.SessionID == "" || p.ModelID == "" {
		_ = s.writer.reply(req.ID, nil, invalidParams("sessionId and model value are required"))
		return
	}
	slot, rpcErr := s.reserve(p.SessionID)
	if rpcErr != nil {
		_ = s.writer.reply(req.ID, nil, rpcErr)
		return
	}
	go func() {
		var rpcErr *rpcError
		found := false
		for _, model := range slot.resource.conv.Models().AvailableModels {
			if model.ModelID == p.ModelID {
				found = true
				break
			}
		}
		if !found {
			rpcErr = invalidParams("Unknown modelId")
		} else if err := slot.resource.conv.SetModel(s.ctx, p.ModelID); err != nil {
			rpcErr = internalError(err)
		}
		result := map[string]any{}
		if rpcErr == nil {
			options := configOptions(modelState(slot.resource.conv))
			if req.Method == "session/set_config_option" {
				result["configOptions"] = options
			} else {
				// The legacy response is empty. Publish the complete updated
				// configuration before acknowledging it so clients using the
				// configOptions advertised by new/load stay synchronized.
				u := &updateStream{ctx: s.ctx, writer: s.writer, sessionID: p.SessionID}
				if err := u.emit(Update{"sessionUpdate": "config_option_update", "configOptions": options}); err != nil {
					rpcErr = internalError(err)
				}
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		slot.busy = false
		_ = s.writer.reply(req.ID, result, rpcErr)
	}()
}
