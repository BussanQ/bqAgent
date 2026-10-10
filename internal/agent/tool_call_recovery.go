package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const maxRecoveredBashCalls = 16

// Recover only the known compatible-provider shape: a sequence of complete,
// distinct command objects in one execute_bash call. Do this before recording
// history so every executed operation has valid arguments and its own result.
func recoverConcatenatedBashCalls(calls []ToolCall) ([]ToolCall, bool) {
	usedIDs := make(map[string]bool, len(calls))
	for _, call := range calls {
		usedIDs[call.ID] = true
	}
	var recovered []ToolCall
	changed := false
	for _, call := range calls {
		var objects []json.RawMessage
		if call.Function.Name == "execute_bash" && call.ID != "" && !json.Valid([]byte(call.Function.Arguments)) {
			objects = concatenatedBashCommands(call.Function.Arguments)
		}
		if len(objects) < 2 {
			recovered = append(recovered, call)
			continue
		}
		changed = true
		for index, object := range objects {
			part := call
			part.Function.Arguments = string(object)
			if index > 0 {
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

func concatenatedBashCommands(raw string) []json.RawMessage {
	decoder := json.NewDecoder(strings.NewReader(raw))
	var objects []json.RawMessage
	seenCommands := map[string]bool{}
	for {
		var object json.RawMessage
		err := decoder.Decode(&object)
		if err == io.EOF {
			return objects
		}
		if err != nil || len(objects) >= maxRecoveredBashCalls {
			return nil
		}
		// Token validation rejects duplicate command keys, extra fields,
		// arrays, null and non-string commands without dropping any data.
		fields := json.NewDecoder(strings.NewReader(string(object)))
		opening, err := fields.Token()
		if err != nil || opening != json.Delim('{') {
			return nil
		}
		key, err := fields.Token()
		if err != nil || key != "command" {
			return nil
		}
		var command string
		if err := fields.Decode(&command); err != nil || strings.TrimSpace(command) == "" || seenCommands[command] {
			return nil
		}
		closing, err := fields.Token()
		if err != nil || closing != json.Delim('}') {
			return nil
		}
		seenCommands[command] = true
		objects = append(objects, object)
	}
}
