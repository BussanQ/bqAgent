package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// maxLineBytes bounds allocations while allowing prompts larger than Scanner's
// default 64 KiB limit. Oversized lines are drained before reading the next one.
const maxLineBytes = 8 << 20

var errLineTooLong = errors.New("JSON-RPC line exceeds 8 MiB")

type incoming struct {
	line []byte
	err  error
}

func readLines(ctx context.Context, input io.Reader, messages chan<- incoming) {
	r := bufio.NewReaderSize(input, 32<<10)
	var line []byte
	tooLong := false
	for {
		part, err := r.ReadSlice('\n')
		if !tooLong {
			if len(line)+len(part) > maxLineBytes {
				tooLong = true
				line = nil
			} else {
				line = append(line, part...)
			}
		}
		if err == bufio.ErrBufferFull {
			select {
			case <-ctx.Done():
				return
			default:
			}
			continue
		}
		if len(line) > 0 || tooLong {
			msg := incoming{line: line}
			if tooLong {
				msg.err = errLineTooLong
			}
			select {
			case messages <- msg:
			case <-ctx.Done():
				return
			}
		}
		line, tooLong = nil, false
		if err != nil {
			select {
			case messages <- incoming{err: err}:
			case <-ctx.Done():
			}
			return
		}
	}
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func invalidParams(message string) *rpcError { return &rpcError{-32602, message} }
func internalError(err error) *rpcError      { return &rpcError{-32603, err.Error()} }

func parseRequest(line []byte) (request, *rpcError) {
	var req request
	if !json.Valid(line) {
		return req, &rpcError{-32700, "Parse error"}
	}
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return req, &rpcError{-32600, "Invalid Request: expected an object"}
	}
	if err := json.Unmarshal(line, &req); err != nil {
		return request{}, &rpcError{-32600, "Invalid Request"}
	}
	if len(req.ID) > 0 {
		var id any
		decoder := json.NewDecoder(bytes.NewReader(req.ID))
		decoder.UseNumber()
		if err := decoder.Decode(&id); err != nil {
			return request{}, &rpcError{-32600, "Invalid request ID"}
		}
		switch id.(type) {
		case nil, string, json.Number:
		default:
			return request{}, &rpcError{-32600, "Invalid request ID"}
		}
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		return req, &rpcError{-32600, "Invalid Request"}
	}
	return req, nil
}

func decodeParams(raw json.RawMessage, dst any) *rpcError {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return invalidParams("params must be an object")
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return invalidParams("Invalid params: " + err.Error())
	}
	return nil
}

// wireWriter serializes complete JSONL frames. The first transport failure
// cancels the server and is preserved for Serve's return value.
type wireWriter struct {
	mu     sync.Mutex
	output io.Writer
	ctx    context.Context
	cancel context.CancelFunc
	err    error
}

func (w *wireWriter) send(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode ACP frame: %w", err)
	}
	data = append(data, '\n')
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	if err := w.ctx.Err(); err != nil {
		return err
	}
	n, err := w.output.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.err = fmt.Errorf("write ACP frame: %w", err)
		w.cancel()
	}
	return w.err
}

func (w *wireWriter) failure() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

func (w *wireWriter) reply(id json.RawMessage, result any, rpcErr *rpcError) error {
	if len(id) == 0 {
		return nil // JSON-RPC notifications never receive responses.
	}
	if rpcErr != nil {
		return w.send(map[string]any{"jsonrpc": "2.0", "id": id, "error": rpcErr})
	}
	return w.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

// updateStream makes finishing an operation a barrier: callbacks already writing
// finish first, and no callback can write after the terminal response.
type updateStream struct {
	mu        sync.Mutex
	closed    bool
	ctx       context.Context
	writer    *wireWriter
	sessionID string
}

func (u *updateStream) emit(update Update) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return errors.New("ACP update stream is closed")
	}
	if err := u.ctx.Err(); err != nil {
		return err
	}
	return u.writer.send(map[string]any{
		"jsonrpc": "2.0", "method": "session/update",
		"params": map[string]any{"sessionId": u.sessionID, "update": update},
	})
}

func (u *updateStream) finish(fn func()) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.closed = true
	fn()
}
