package agent

import "encoding/json"

func responseItemsFromMessage(message map[string]any) []json.RawMessage {
	value, ok := message["response_items"]
	if !ok {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var items []json.RawMessage
	if json.Unmarshal(encoded, &items) != nil {
		return nil
	}
	return items
}

// Tool argument recovery can modify calls after decoding. Replay the recovered
// calls rather than stale raw arguments, while retaining every other item.
func (m AssistantMessage) replayResponseItems() []json.RawMessage {
	originalIDs := []string{}
	for _, raw := range m.ResponseItems {
		var item openAIResponseOutput
		if json.Unmarshal(raw, &item) == nil && item.Type == "function_call" {
			originalIDs = append(originalIDs, firstNonBlank(item.CallID, item.ID))
		}
	}
	changed := len(originalIDs) != len(m.ToolCalls)
	for index, id := range originalIDs {
		if index >= len(m.ToolCalls) || m.ToolCalls[index].ID != id {
			changed = true
			break
		}
	}
	items := make([]json.RawMessage, 0, len(m.ResponseItems))
	callIndex := 0
	appendCalls := func() {
		for _, call := range m.ToolCalls {
			raw, _ := json.Marshal(map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments})
			items = append(items, raw)
		}
	}
	inserted := false
	for _, raw := range m.ResponseItems {
		var item map[string]any
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		if item["type"] == "function_call" {
			if changed {
				if !inserted {
					appendCalls()
					inserted = true
				}
				continue
			}
			call := m.ToolCalls[callIndex]
			item["call_id"], item["name"], item["arguments"] = call.ID, call.Function.Name, call.Function.Arguments
			raw, _ = json.Marshal(item)
			callIndex++
		}
		items = append(items, raw)
	}
	if changed && !inserted {
		appendCalls()
	}
	return items
}

// These fields are local history metadata, not Chat Completions parameters.
func chatMessagesWithoutResponseMetadata(messages []map[string]any) []map[string]any {
	result := duplicateMessages(messages)
	for _, message := range result {
		delete(message, "response_items")
		delete(message, "phase")
		delete(message, ToolImagesMessageKey)
	}
	return result
}
