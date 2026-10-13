package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Magpie emits complete server search blocks before asking the current model
// to summarize or search again. Consume the first verified result immediately;
// waiting for its entire assistant turn can discard useful results at timeout.
// The caller closes the body and cancels the request to stop the gateway loop.
func decodeMagpieSearchResponse(response *http.Response) (anthropicSearchResponse, error) {
	var result anthropicSearchResponse
	reader := bufio.NewReader(io.LimitReader(response.Body, 4*1024*1024))
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return result, fmt.Errorf("read Magpie search response: %w", err)
		}
		if strings.ContainsRune(" \r\n\t", rune(b)) {
			continue
		}
		if err := reader.UnreadByte(); err != nil {
			return result, err
		}
		if b == '{' {
			// Keep compatibility with gateways that ignore stream=true.
			err := json.NewDecoder(reader).Decode(&result)
			return result, err
		}
		break
	}

	type partialBlock struct {
		fields map[string]json.RawMessage
		input  strings.Builder
		closed bool
	}
	blocks := map[int]*partialBlock{}
	searches := map[string]json.RawMessage{}
	var data []string
	consume := func() (bool, error) {
		payload := strings.Join(data, "\n")
		data = nil
		if payload == "" || payload == "[DONE]" {
			return false, nil
		}
		var event struct {
			Type         string                     `json:"type"`
			Index        *int                       `json:"index"`
			Message      anthropicSearchResponse    `json:"message"`
			ContentBlock map[string]json.RawMessage `json:"content_block"`
			Delta        struct {
				Type        string          `json:"type"`
				Text        string          `json:"text"`
				Thinking    string          `json:"thinking"`
				Signature   string          `json:"signature"`
				PartialJSON string          `json:"partial_json"`
				Citation    json.RawMessage `json:"citation"`
				StopReason  string          `json:"stop_reason"`
			} `json:"delta"`
			Usage anthropicUsage `json:"usage"`
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return false, fmt.Errorf("decode Magpie search stream event: %w", err)
		}
		switch event.Type {
		case "message_start":
			result.Usage = event.Message.Usage
		case "message_delta":
			mergeNativeSearchUsage(&result.Usage, event.Usage)
			result.StopReason = event.Delta.StopReason
		case "content_block_start":
			if event.Index == nil || *event.Index < 0 || blocks[*event.Index] != nil || len(event.ContentBlock) == 0 {
				return false, fmt.Errorf("invalid Magpie search block start")
			}
			blocks[*event.Index] = &partialBlock{fields: event.ContentBlock}
		case "content_block_delta", "content_block_stop":
			if event.Index == nil || blocks[*event.Index] == nil || blocks[*event.Index].closed {
				return false, fmt.Errorf("Magpie search event has no open content block")
			}
			block := blocks[*event.Index]
			if event.Type == "content_block_delta" {
				switch event.Delta.Type {
				case "input_json_delta":
					block.input.WriteString(event.Delta.PartialJSON)
				case "text_delta", "thinking_delta", "signature_delta":
					field, value := "text", event.Delta.Text
					if event.Delta.Type == "thinking_delta" {
						field, value = "thinking", event.Delta.Thinking
					} else if event.Delta.Type == "signature_delta" {
						field, value = "signature", event.Delta.Signature
					}
					var previous string
					if raw := block.fields[field]; len(raw) > 0 {
						if err := json.Unmarshal(raw, &previous); err != nil {
							return false, err
						}
					}
					block.fields[field], _ = json.Marshal(previous + value)
				case "citations_delta":
					var citations []json.RawMessage
					if raw := block.fields["citations"]; len(raw) > 0 {
						if err := json.Unmarshal(raw, &citations); err != nil {
							return false, err
						}
					}
					block.fields["citations"], _ = json.Marshal(append(citations, event.Delta.Citation))
				}
				return false, nil
			}
			block.closed = true
			if block.input.Len() > 0 {
				input := json.RawMessage(block.input.String())
				if !json.Valid(input) {
					return false, fmt.Errorf("invalid Magpie search tool arguments")
				}
				block.fields["input"] = input
			}
			raw, err := json.Marshal(block.fields)
			if err != nil {
				return false, err
			}
			result.Content = append(result.Content, raw)
			var completed struct {
				Type      string          `json:"type"`
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				ToolUseID string          `json:"tool_use_id"`
				Content   json.RawMessage `json:"content"`
			}
			if err := json.Unmarshal(raw, &completed); err != nil {
				return false, err
			}
			if completed.Type == "server_tool_use" && completed.Name == "web_search" && completed.ID != "" {
				searches[completed.ID] = raw
			}
			if completed.Type == "web_search_tool_result" {
				search := searches[completed.ToolUseID]
				if len(search) == 0 {
					return false, fmt.Errorf("Magpie search result has no matching completed search call")
				}
				var toolError struct {
					Type string `json:"type"`
					Code string `json:"error_code"`
				}
				if json.Unmarshal(completed.Content, &toolError) == nil && toolError.Type == "web_search_tool_result_error" {
					return false, fmt.Errorf("Magpie web search failed: %s", toolError.Code)
				}
				var sources []nativeSearchSource
				if err := json.Unmarshal(completed.Content, &sources); err != nil {
					return false, fmt.Errorf("decode Magpie search sources: %w", err)
				}
				verified := nativeSearchResults{}
				for _, source := range sources {
					verified.addSource(source)
				}
				if len(verified.sources) > 0 {
					// Only return this complete search/result pair. Earlier text
					// may be a plan or uncited speculation, not a search summary.
					result.Content = []json.RawMessage{search, raw}
					result.searchComplete = true
					return true, nil
				}
			}
		case "message_stop":
			for _, block := range blocks {
				if !block.closed {
					return false, fmt.Errorf("Magpie search ended with an incomplete content block")
				}
			}
			return true, nil
		case "error":
			return false, providerStreamEventError(firstNonBlank(event.Error.Message, "Magpie search stream failed"), event.Error.Type)
		}
		return false, nil
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if done, err := consume(); done || err != nil {
				return result, err
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("read Magpie search stream: %w", err)
	}
	if done, err := consume(); done || err != nil {
		return result, err
	}
	return result, &IncompleteStreamError{Provider: "anthropic", Reason: "Magpie search stream ended before a complete search result or message_stop"}
}

func mergeNativeSearchUsage(dst *anthropicUsage, src anthropicUsage) {
	if src.InputTokens != nil {
		dst.InputTokens = src.InputTokens
	}
	if src.OutputTokens != nil {
		dst.OutputTokens = src.OutputTokens
	}
	if src.CacheCreationInputTokens != nil {
		dst.CacheCreationInputTokens = src.CacheCreationInputTokens
	}
	if src.CacheReadInputTokens != nil {
		dst.CacheReadInputTokens = src.CacheReadInputTokens
	}
}
