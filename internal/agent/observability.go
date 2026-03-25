package agent

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"bqagent/internal/tools"
)

type instrumentedClient struct {
	inner     ChatCompletionClient
	logWriter io.Writer
}

func instrumentChatCompletionClient(client ChatCompletionClient, logWriter io.Writer) ChatCompletionClient {
	if client == nil || logWriter == nil {
		return client
	}
	return &instrumentedClient{inner: client, logWriter: logWriter}
}

func (c *instrumentedClient) CreateChatCompletion(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition) (message AssistantMessage, err error) {
	startedAt := time.Now()
	defer func() {
		c.logModelRequest("chat", model, false, startedAt, err)
	}()

	message, err = c.inner.CreateChatCompletion(ctx, model, messages, definitions)
	return message, err
}

func (c *instrumentedClient) CreateChatCompletionWithOptions(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions) (message AssistantMessage, err error) {
	startedAt := time.Now()
	defer func() {
		c.logModelRequest("chat", model, false, startedAt, err)
	}()

	if client, ok := c.inner.(chatCompletionOptionsClient); ok {
		message, err = client.CreateChatCompletionWithOptions(ctx, model, messages, definitions, options)
		return message, err
	}

	message, err = c.inner.CreateChatCompletion(ctx, model, messages, definitions)
	return message, err
}

func (c *instrumentedClient) CreateChatCompletionStream(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, onChunk func(string)) (message AssistantMessage, err error) {
	startedAt := time.Now()
	defer func() {
		c.logModelRequest("chat", model, true, startedAt, err)
	}()

	message, err = c.inner.CreateChatCompletionStream(ctx, model, messages, definitions, onChunk)
	return message, err
}

func (c *instrumentedClient) logModelRequest(requestType, model string, stream bool, startedAt time.Time, err error) {
	if c.logWriter == nil {
		return
	}

	status := "success"
	extra := ""
	if err != nil {
		status = "error"
		extra = fmt.Sprintf(" error=%q", sanitizeLogValue(err.Error()))
	}

	fmt.Fprintf(c.logWriter, "[Model] request=%s model=%s stream=%t duration=%s status=%s%s\n", requestType, model, stream, formatDuration(time.Since(startedAt)), status, extra)
}

func logToolTiming(logWriter io.Writer, toolName string, duration time.Duration, err error) {
	if logWriter == nil {
		return
	}

	status := "success"
	extra := ""
	if err != nil {
		status = "error"
		extra = fmt.Sprintf(" error=%q", sanitizeLogValue(err.Error()))
	}

	fmt.Fprintf(logWriter, "[Tool] name=%s duration=%s status=%s%s\n", toolName, formatDuration(duration), status, extra)
}

func logTurnTiming(logWriter io.Writer, actualIterations int, allowPlan bool, duration time.Duration, err error) {
	if logWriter == nil {
		return
	}

	status := "success"
	extra := ""
	if err != nil {
		status = "error"
		extra = fmt.Sprintf(" error=%q", sanitizeLogValue(err.Error()))
	}

	fmt.Fprintf(logWriter, "[Turn] iterations=%d allow_plan=%t duration=%s status=%s%s\n", actualIterations, allowPlan, formatDuration(duration), status, extra)
}

func formatDuration(duration time.Duration) string {
	if duration < time.Microsecond {
		return duration.String()
	}
	if duration < time.Millisecond {
		return duration.Round(time.Microsecond).String()
	}
	return duration.Round(time.Millisecond).String()
}

func sanitizeLogValue(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	return strings.TrimSpace(value)
}
