package tools

import (
	"bytes"
	"sync"
)

const (
	// DefaultBashOutputMaxBytes bounds captured combined stdout/stderr from a
	// single execute_bash invocation.
	DefaultBashOutputMaxBytes int64 = 1 << 20
	// DefaultReadFileMaxBytes bounds content returned by one read_file call.
	DefaultReadFileMaxBytes int64 = 1 << 20

	truncatedOutputMarker = "\n[output truncated]\n"
)

// boundedOutput stores no more than limit bytes of process or file content.
// It still reports a successful full write so callers keep draining their
// source after the returned content reaches the budget.
type boundedOutput struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	limit     int64
	truncated bool
}

func newBoundedOutput(limit, fallback int64) *boundedOutput {
	if limit <= 0 {
		limit = fallback
	}
	return &boundedOutput{limit: limit}
}

func (output *boundedOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()

	remaining := output.limit - int64(output.buffer.Len())
	if remaining > 0 {
		kept := len(data)
		if int64(kept) > remaining {
			kept = int(remaining)
		}
		_, _ = output.buffer.Write(data[:kept])
	}
	if int64(len(data)) > remaining {
		output.truncated = true
	}
	return len(data), nil
}

func (output *boundedOutput) String() string {
	output.mu.Lock()
	defer output.mu.Unlock()

	result := output.buffer.String()
	if output.truncated {
		return result + truncatedOutputMarker
	}
	return result
}
