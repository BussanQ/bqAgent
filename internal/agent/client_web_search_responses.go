package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type nativeSearchResponseItem struct {
	Type   string `json:"type"`
	Status string `json:"status"`
	Action struct {
		Type    string               `json:"type"`
		Sources []nativeSearchSource `json:"sources"`
	} `json:"action"`
	Content []struct {
		Type        string               `json:"type"`
		Text        string               `json:"text"`
		Annotations []nativeSearchSource `json:"annotations"`
	} `json:"content"`
}

type nativeSearchResponse struct {
	Status string                     `json:"status"`
	Output []nativeSearchResponseItem `json:"output"`
	Usage  openAIResponseUsage        `json:"usage"`
	Error  struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
}

func decodeNativeSearchResponses(response *http.Response) (nativeSearchResponse, error) {
	var result nativeSearchResponse
	reader := bufio.NewReader(io.LimitReader(response.Body, 4*1024*1024))
	// Some compatible servers ignore stream=true or omit Content-Type. Sniff
	// the body so both JSON and SSE work without depending on gateway headers.
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return result, fmt.Errorf("read native search response: %w", err)
		}
		if strings.ContainsRune(" \r\n\t", rune(b)) {
			continue
		}
		if err := reader.UnreadByte(); err != nil {
			return result, err
		}
		if b == '{' {
			err := json.NewDecoder(reader).Decode(&result)
			return result, err
		}
		break
	}

	// Keep completed items even when response.completed has an empty output
	// array (as on Codex). Merge terminal output as well: gateways may retain
	// search metadata in only one of the two representations. Rendering dedups
	// text and source URLs, while all search records still undergo validation.
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var data []string
	consume := func() (bool, error) {
		payload := strings.Join(data, "\n")
		data = nil
		if payload == "" || payload == "[DONE]" {
			return false, nil
		}
		var event struct {
			Type     string                   `json:"type"`
			Message  string                   `json:"message"`
			Item     nativeSearchResponseItem `json:"item"`
			Response nativeSearchResponse     `json:"response"`
			Error    struct {
				Message string `json:"message"`
				Code    string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return false, fmt.Errorf("decode native search stream event: %w", err)
		}
		switch event.Type {
		case "response.output_item.done":
			result.Output = append(result.Output, event.Item)
		case "response.completed", "response.incomplete", "response.failed":
			result.Status = event.Response.Status
			result.Usage = event.Response.Usage
			result.Output = append(result.Output, event.Response.Output...)
			if event.Type != "response.completed" {
				return false, providerStreamEventError(firstNonBlank(event.Response.Error.Message, event.Error.Message, event.Type), firstNonBlank(event.Response.Error.Code, event.Error.Code, event.Type))
			}
			if result.Status == "" {
				result.Status = "completed"
			}
			return true, nil
		case "error":
			return false, providerStreamEventError(firstNonBlank(event.Error.Message, event.Message, "native search stream failed"), event.Error.Code)
		}
		return false, nil
	}
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
		return result, fmt.Errorf("read native search stream: %w", err)
	}
	if done, err := consume(); done || err != nil {
		return result, err
	}
	return result, &IncompleteStreamError{Provider: "responses", Reason: "native search stream missing response.completed event"}
}
