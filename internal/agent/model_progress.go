package agent

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const DefaultStreamProgressTimeout = 2 * time.Minute

type modelProgressObserverKey struct{}
type streamActivityObserverKey struct{}
type providerRetryObserverKey struct{}

// Activity is diagnostic only: bytes and keepalives do not renew model progress.
func reportStreamActivity(ctx context.Context, kind string) {
	if observer, _ := ctx.Value(streamActivityObserverKey{}).(func(string)); observer != nil {
		observer(kind)
	}
}

func reportStreamLine(ctx context.Context, line string) {
	if strings.HasPrefix(line, ":") {
		reportStreamActivity(ctx, "heartbeat")
	}
}

func reportStreamEvent(ctx context.Context, event string) {
	if event == "ping" {
		reportStreamActivity(ctx, "heartbeat")
	}
	reportStreamActivity(ctx, "event:"+event)
}

func withModelProgressObserver(ctx context.Context, observer func()) context.Context {
	previous, _ := ctx.Value(modelProgressObserverKey{}).(func())
	return context.WithValue(ctx, modelProgressObserverKey{}, func() {
		if previous != nil {
			previous()
		}
		observer()
	})
}

func reportModelProgress(ctx context.Context) {
	if observer, _ := ctx.Value(modelProgressObserverKey{}).(func()); observer != nil {
		observer()
	}
}

// Each attempt has its own progress budget. Expiration cancels only that
// attempt, allowing the shared transient retry budget to recover the request.
// Transport heartbeats and local progress messages cannot renew it.
func (c *Client) withStreamProgressTimeout(ctx context.Context, run func(context.Context) (AssistantMessage, error)) (AssistantMessage, error) {
	if c.streamProgressTimeout <= 0 {
		return run(ctx)
	}
	watched, watchdog := newStreamWatchdog(ctx, c.streamProgressTimeout)
	defer watchdog.stop()
	message, err := run(withModelProgressObserver(watched, watchdog.touch))
	if ctx.Err() != nil {
		return message, &providerRequestError{cause: ctx.Err(), request: ProviderRequestMetadataFromError(err)}
	}
	if err != nil && watchdog.timedOut.Load() {
		return message, &ProviderError{
			Category:  ProviderErrorTimeout,
			Transient: true,
			Message:   fmt.Sprintf("模型已连续 %s 无有效进展（未收到正文、推理内容或工具参数），已停止等待；可重试或恢复会话后继续", c.streamProgressTimeout),
			Request:   ProviderRequestMetadataFromError(err),
		}
	}
	return message, err
}
