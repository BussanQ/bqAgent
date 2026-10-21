package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const maxRecoveredToolCalls = 16

// Recover only complete sequences of JSON objects, preserving repeated calls.
// Do this before recording history so every executed operation has valid
// arguments and its own result. Shell calls additionally require command-only
// objects; no valid prefix of an invalid batch may execute.
func recoverConcatenatedToolCalls(calls []ToolCall) ([]ToolCall, bool) {
	usedIDs := make(map[string]bool, len(calls))
	idCounts := make(map[string]int, len(calls))
	for _, call := range calls {
		usedIDs[call.ID] = true
		idCounts[call.ID]++
	}
	var recovered []ToolCall
	changed := false
	for _, call := range calls {
		var objects []json.RawMessage
		if strings.TrimSpace(call.Function.Name) != "" && !json.Valid([]byte(call.Function.Arguments)) {
			objects = concatenatedToolArgumentObjects(call.Function.Arguments, maxRecoveredToolCalls)
			if call.Function.Name == "bash" {
				for _, object := range objects {
					if !isCommandOnlyObject(object) {
						objects = nil
						break
					}
				}
			}
		}
		if len(objects) < 2 {
			recovered = append(recovered, call)
			continue
		}
		changed = true
		for index, object := range objects {
			part := call
			part.Function.Arguments = string(object)
			if index > 0 || strings.TrimSpace(call.ID) == "" || idCounts[call.ID] > 1 {
				for salt := 0; ; salt++ {
					digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d\x00%s", call.ID, index, salt, object)))
					part.ID = fmt.Sprintf("call_split_%x", digest[:16])
					if !usedIDs[part.ID] {
						break
					}
				}
			}
			usedIDs[part.ID] = true
			recovered = append(recovered, part)
		}
	}
	if !changed {
		return calls, false
	}
	return recovered, true
}

// Decode the entire sequence before returning any objects. A zero limit is
// used for diagnostics; automatic recovery has a bounded batch size.
func concatenatedToolArgumentObjects(raw string, limit int) []json.RawMessage {
	decoder := json.NewDecoder(strings.NewReader(raw))
	var objects []json.RawMessage
	for {
		var object json.RawMessage
		err := decoder.Decode(&object)
		if err == io.EOF {
			return objects
		}
		if err != nil || (limit > 0 && len(objects) >= limit) {
			return nil
		}
		if len(object) == 0 || object[0] != '{' {
			return nil
		}
		objects = append(objects, object)
	}
}

func isCommandOnlyObject(object json.RawMessage) bool {
	// Token validation rejects duplicate command keys, extra fields,
	// arrays, null and non-string commands without dropping any data.
	fields := json.NewDecoder(strings.NewReader(string(object)))
	opening, err := fields.Token()
	if err != nil || opening != json.Delim('{') {
		return false
	}
	key, err := fields.Token()
	if err != nil || key != "command" {
		return false
	}
	var command string
	if err := fields.Decode(&command); err != nil || strings.TrimSpace(command) == "" {
		return false
	}
	closing, err := fields.Token()
	return err == nil && closing == json.Delim('}')
}
