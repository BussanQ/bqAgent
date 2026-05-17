package agent

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"strings"
	"sync"
	"time"

	"bqagent/internal/tools"
)

type instrumentedClient struct {
	inner          ChatCompletionClient
	logWriter      io.Writer
	progressWriter io.Writer
}

type synchronizedWriter struct {
	mu    sync.Mutex
	inner io.Writer
}

func (w *synchronizedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.inner.Write(p)
}

func synchronizeLogWriter(writer io.Writer) io.Writer {
	if writer == nil {
		return nil
	}
	if _, ok := writer.(*synchronizedWriter); ok {
		return writer
	}
	return &synchronizedWriter{inner: writer}
}

func instrumentChatCompletionClient(client ChatCompletionClient, logWriter io.Writer, progressWriter io.Writer) ChatCompletionClient {
	if client == nil {
		return client
	}
	logWriter = synchronizeLogWriter(logWriter)
	progressWriter = synchronizeLogWriter(progressWriter)
	if logWriter == nil && progressWriter == nil {
		return client
	}
	return &instrumentedClient{inner: client, logWriter: logWriter, progressWriter: progressWriter}
}

func (c *instrumentedClient) CreateChatCompletion(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition) (message AssistantMessage, err error) {
	startedAt := time.Now()
	reporter := startModelProgressReporter(ctx, c.progressWriter, false)
	defer func() {
		reporter.stop()
		c.logModelRequest("chat", model, false, startedAt, err)
	}()

	message, err = c.inner.CreateChatCompletion(ctx, model, messages, definitions)
	return message, err
}

func (c *instrumentedClient) CreateChatCompletionWithOptions(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions) (message AssistantMessage, err error) {
	startedAt := time.Now()
	reporter := startModelProgressReporter(ctx, c.progressWriter, false)
	defer func() {
		reporter.stop()
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
	reporter := startModelProgressReporter(ctx, c.progressWriter, true)
	defer func() {
		reporter.stop()
		c.logModelRequest("chat", model, true, startedAt, err)
	}()

	wrappedOnChunk := func(chunk string) {
		reporter.markActivity()
		if onChunk != nil {
			onChunk(chunk)
		}
	}

	message, err = c.inner.CreateChatCompletionStream(ctx, model, messages, definitions, wrappedOnChunk)
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

type modelProgressTicker interface {
	C() <-chan time.Time
	Stop()
}

type realModelProgressTicker struct {
	ticker *time.Ticker
}

func (t *realModelProgressTicker) C() <-chan time.Time {
	return t.ticker.C
}

func (t *realModelProgressTicker) Stop() {
	t.ticker.Stop()
}

type modelProgressReporter struct {
	cancel       context.CancelFunc
	done         chan struct{}
	logWriter    io.Writer
	stream       bool
	interval     time.Duration
	lastActivity time.Time
	mu           sync.Mutex
}

var modelProgressInterval = 10 * time.Second

var modelProgressMessages = []string{
	"仍在推理中：好答案需要一点时间。",
	"仍在推理中：我还在处理这个请求。",
	"仍在推理中：正在等待模型返回。",
	"仍在推理中：复杂问题需要多想一会儿。",
	"仍在推理中：后台任务仍在运行。",
}

var newModelProgressTicker = func(interval time.Duration) modelProgressTicker {
	return &realModelProgressTicker{ticker: time.NewTicker(interval)}
}

var modelProgressNow = time.Now
var selectModelProgressMessage = randomModelProgressMessage

func startModelProgressReporter(ctx context.Context, logWriter io.Writer, stream bool) *modelProgressReporter {
	if logWriter == nil || modelProgressInterval <= 0 {
		return nil
	}

	progressCtx, cancel := context.WithCancel(ctx)
	reporter := &modelProgressReporter{
		cancel:       cancel,
		done:         make(chan struct{}),
		logWriter:    logWriter,
		stream:       stream,
		interval:     modelProgressInterval,
		lastActivity: modelProgressNow(),
	}
	ticker := newModelProgressTicker(reporter.interval)
	go reporter.run(progressCtx, ticker)
	return reporter
}

func (r *modelProgressReporter) run(ctx context.Context, ticker modelProgressTicker) {
	defer close(r.done)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			if r.shouldEmit() {
				fmt.Fprintf(r.logWriter, "%s\n", selectModelProgressMessage(modelProgressMessages))
			}
		}
	}
}

func (r *modelProgressReporter) shouldEmit() bool {
	if !r.stream {
		return true
	}

	r.mu.Lock()
	lastActivity := r.lastActivity
	r.mu.Unlock()
	return modelProgressNow().Sub(lastActivity) >= r.interval
}

func (r *modelProgressReporter) markActivity() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.lastActivity = modelProgressNow()
	r.mu.Unlock()
}

func (r *modelProgressReporter) stop() {
	if r == nil {
		return
	}
	r.cancel()
	<-r.done
}

func randomModelProgressMessage(messages []string) string {
	if len(messages) == 0 {
		return "仍在推理中：后台任务仍在运行。"
	}
	index, err := rand.Int(rand.Reader, big.NewInt(int64(len(messages))))
	if err != nil {
		return messages[0]
	}
	return messages[int(index.Int64())]
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
