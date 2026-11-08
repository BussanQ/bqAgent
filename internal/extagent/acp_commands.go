package extagent

import (
	"context"
	"fmt"
	"slices"
	"time"
)

type acpCommandReader interface {
	AvailableCommands(context.Context, string) ([]NativeCommand, error)
}

func (c *stdioACPClient) rememberCommands(id string, rows []commandInfo) {
	if id == "" || rows == nil {
		return
	}
	commands := slashCommandRows(rows)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.commandReports == nil {
		c.commandReports = make(map[string][]NativeCommand)
	}
	c.commandReports[id] = commands
	if c.commandsChanged != nil {
		close(c.commandsChanged)
		c.commandsChanged = nil
	}
}

// The notification can arrive on either side of the new/load response. An
// agent which never advertises commands must still allow the user to type.
func (c *stdioACPClient) AvailableCommands(ctx context.Context, id string) ([]NativeCommand, error) {
	timer := time.NewTimer(750 * time.Millisecond)
	defer timer.Stop()
	for {
		c.mu.Lock()
		if rows, ok := c.commandReports[id]; ok {
			c.mu.Unlock()
			return slices.Clone(rows), nil
		}
		if c.commandsChanged == nil {
			c.commandsChanged = make(chan struct{})
		}
		changed := c.commandsChanged
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, nil
		case <-changed:
		}
	}
}

func (broker *Broker) acpCommands(ctx context.Context, t *AgentTransport, active ACPClient, nativeID, cwd string) ([]NativeCommand, error) {
	if c, ok := active.(acpCommandReader); ok && nativeID != "" {
		return c.AvailableCommands(ctx, nativeID)
	}
	client, err := broker.acpFactory(ctx, t.Command, cwd)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	reader, ok := client.(acpCommandReader)
	if !ok {
		return nil, fmt.Errorf("ACP 适配器未提供命令目录")
	}
	if err := client.Initialize(ctx); err != nil {
		return nil, err
	}
	if nativeID != "" && !client.LoadSessionSupported() {
		return nil, fmt.Errorf("ACP 适配器无法恢复此会话的命令目录")
	}
	id, release, err := openACPCatalogSession(ctx, client, nativeID, cwd, false)
	if err != nil {
		return nil, err
	}
	defer release()
	if nativeID != "" && id != nativeID {
		return nil, fmt.Errorf("ACP 返回了其他会话的命令目录")
	}
	return reader.AvailableCommands(ctx, id)
}
