package extagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

type acpModels struct {
	ModelCatalog
	configID string // Empty for the legacy session/set_model protocol.
	effortID string // Native config ID, independent of the displayed name.
}

func parseACPModels(data json.RawMessage) (acpModels, bool) {
	type choice struct {
		Value, Name string
		Options     []struct{ Value, Name string }
	}
	var caps struct {
		ConfigOptions []struct {
			ID, Category, Type string
			CurrentValue       json.RawMessage
			Options            []choice
		}
		Models *struct {
			CurrentModelID  string
			AvailableModels []struct{ ModelID, Name string }
		}
	}
	var m acpModels
	if json.Unmarshal(data, &caps) != nil {
		return m, false
	}
	var efforts EffortMenu
	for _, opt := range caps.ConfigOptions {
		if opt.Type != "select" || opt.ID == "" {
			continue
		}
		var add func(string, string)
		switch {
		case m.configID == "" && (opt.Category == "model" || opt.ID == "model"):
			m.configID = opt.ID
			_ = json.Unmarshal(opt.CurrentValue, &m.Selected)
			add = m.add
		case m.effortID == "" && (opt.Category == "thought_level" || opt.ID == "thought_level" || opt.ID == "reasoning_effort" || opt.ID == "thinking_level" || opt.ID == "effort"):
			m.effortID = opt.ID
			_ = json.Unmarshal(opt.CurrentValue, &m.SelectedEffort)
			add = efforts.add
		default:
			continue
		}
		for _, entry := range opt.Options {
			add(entry.Value, entry.Name)
			for _, v := range entry.Options {
				add(v.Value, v.Name)
			}
		}
	}
	if caps.Models != nil && (m.configID == "" || len(m.Models) == 0 && len(caps.Models.AvailableModels) > 0) {
		// Some adapters publish an empty config selector and advertise their
		// selectable IDs only through the legacy session model API.
		for _, row := range caps.Models.AvailableModels {
			m.add(row.ModelID, row.Name)
		}
		if m.configID == "" || len(m.Models) > 0 {
			m.configID = ""
			m.Selected = firstNonEmpty(caps.Models.CurrentModelID, m.Selected)
		}
	}
	m.Efforts = map[string]EffortMenu{m.Selected: efforts}
	return m, m.configID != "" || caps.Models != nil || m.effortID != ""
}

func (c *stdioACPClient) rememberModels(id string, data json.RawMessage) {
	if m, ok := parseACPModels(data); ok {
		c.mu.Lock()
		if m.effortID != "" && m.Selected == "" && len(m.Models) == 0 && c.modelSessionID == id {
			// Some legacy sessions report models once on open, then only
			// thought-level config options in update notifications.
			m.Selected, m.Models, m.configID = c.modelOptions.Selected, c.modelOptions.Models, c.modelOptions.configID
			m.Efforts = map[string]EffortMenu{m.Selected: m.Efforts[""]}
		}
		if c.modelSessionID == id && c.modelOptions.Selected != "" && m.Selected != "" && c.modelOptions.Selected != m.Selected && c.usageSessionID == id {
			c.usageSessionID, c.contextReading = "", nil
		}
		c.modelSessionID, c.modelOptions = id, m
		if c.modelsChanged != nil {
			close(c.modelsChanged)
			c.modelsChanged = nil
		}
		c.mu.Unlock()
	}
}

func (c *stdioACPClient) modelsFor(id string) (acpModels, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.modelOptions, id != "" && id == c.modelSessionID
}

// Some adapters publish config_option_update just after resolving session/new
// or session/load. Keep the probe alive for that notification.
func (c *stdioACPClient) availableModels(ctx context.Context, id string) (acpModels, bool) {
	timer := time.NewTimer(750 * time.Millisecond)
	defer timer.Stop()
	for {
		c.mu.Lock()
		m := c.modelOptions
		if id != "" && c.modelSessionID == id && len(m.Models) > 0 {
			c.mu.Unlock()
			return m, true
		}
		if c.modelsChanged == nil {
			c.modelsChanged = make(chan struct{})
		}
		changed := c.modelsChanged
		c.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return acpModels{}, false
		case <-timer.C:
			return c.modelsFor(id)
		}
	}
}

func (c *stdioACPClient) PromptWithModel(ctx context.Context, id, prompt, model string) (string, error) {
	return c.PromptWithOptions(ctx, id, prompt, PromptOptions{Model: model})
}

func (c *stdioACPClient) PromptWithOptions(ctx context.Context, id, prompt string, options PromptOptions) (string, error) {
	if len(options.Images) > 0 && !c.SupportsImages() {
		return "", fmt.Errorf("ACP Agent 未声明图片输入能力")
	}
	var before acpModels
	if options.Model != "" && options.Effort != "" {
		before, _ = c.availableModels(ctx, id)
	}
	if options.Model != "" {
		if err := c.selectModel(ctx, id, options.Model); err != nil {
			return "", err
		}
	}
	if options.Effort != "" {
		if err := c.selectEffort(ctx, id, options.Effort); err != nil {
			if before.Selected != "" && options.Model != "" && options.Model != before.Selected {
				recoverCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
				defer cancel()
				restoreErr := c.selectModel(recoverCtx, id, before.Selected)
				if restoreErr == nil && before.SelectedEffort != "" {
					restoreErr = c.selectEffort(recoverCtx, id, before.SelectedEffort)
				}
				if restoreErr != nil {
					err = errors.Join(err, fmt.Errorf("还原 ACP 会话设置：%w", restoreErr))
				}
			}
			return "", err
		}
	}
	return c.promptWithImages(ctx, id, prompt, options.Images)
}

func (c *stdioACPClient) selectEffort(ctx context.Context, id, effort string) error {
	menu, ok := c.modelsFor(id)
	if !ok || menu.effortID == "" || !menu.Efforts[menu.Selected].supports(effort) {
		return fmt.Errorf("ACP 会话未提供推理强度 %q", effort)
	}
	if menu.SelectedEffort == effort {
		return nil
	}
	reply, err := c.request(ctx, "session/set_config_option", map[string]string{"sessionId": id, "configId": menu.effortID, "value": effort})
	if err != nil {
		return fmt.Errorf("切换 ACP 推理强度：%w", err)
	}
	c.rememberModels(id, reply.Result)
	if current, ok := c.modelsFor(id); !ok || current.SelectedEffort != effort {
		return fmt.Errorf("ACP 未确认推理强度 %q", effort)
	}
	return nil
}

func (c *stdioACPClient) selectModel(ctx context.Context, id, model string) error {
	menu, ok := c.availableModels(ctx, id)
	if err := ctx.Err(); err != nil {
		return err
	}
	if !ok || !slices.ContainsFunc(menu.Models, func(v ModelOption) bool { return v.ID == model }) {
		return fmt.Errorf("ACP 会话未提供模型 %q", model)
	}
	if menu.Selected != model {
		c.mu.Lock()
		if c.usageSessionID == id {
			c.usageSessionID, c.contextReading = "", nil
		}
		c.mu.Unlock()
		method := "session/set_model"
		params := map[string]any{"sessionId": id}
		if menu.configID != "" {
			method = "session/set_config_option"
			params["configId"], params["value"] = menu.configID, model
		} else {
			params["modelId"] = model
		}
		reply, err := c.request(ctx, method, params)
		if err != nil {
			return fmt.Errorf("切换 ACP 模型：%w", err)
		}
		if updated, ok := parseACPModels(reply.Result); ok {
			if updated.Selected != "" && updated.Selected != model {
				return fmt.Errorf("ACP 未应用模型 %q", model)
			}
			if updated.Selected == "" {
				updated.configID, updated.Models = menu.configID, menu.Models
				updated.Efforts = map[string]EffortMenu{model: updated.Efforts[""]}
			}
			menu = updated
		} else if reported, ok := c.modelsFor(id); ok && reported.Selected == model {
			menu = reported
		} else {
			// A bare acknowledgement confirms the model, not its previous
			// model's effort choices. Wait for new config options.
			menu.Efforts, menu.effortID, menu.SelectedEffort = nil, "", ""
		}
		menu.Selected = model
		c.mu.Lock()
		c.modelSessionID, c.modelOptions = id, menu
		c.mu.Unlock()
	}
	return nil
}

type acpModelReader interface {
	availableModels(context.Context, string) (acpModels, bool)
}

func (broker *Broker) acpModelCatalog(ctx context.Context, t *AgentTransport, bqID, nativeID, cwd, previewModel string) (ModelCatalog, error) {
	broker.mu.Lock()
	active := broker.acpClients[acpClientKey{sessionID: bqID, agent: t.Agent}]
	broker.mu.Unlock()
	if c, ok := active.(acpModelReader); ok && nativeID != "" {
		if m, ok := c.availableModels(ctx, nativeID); ok && (previewModel == "" || m.Selected == previewModel) {
			return m.ModelCatalog, nil
		}
	}
	var menu ModelCatalog
	c, err := broker.acpFactory(ctx, t.Command, cwd)
	if err != nil {
		return menu, err
	}
	defer c.Close()
	reader, ok := c.(acpModelReader)
	if !ok {
		return menu, fmt.Errorf("ACP 适配器未提供模型目录")
	}
	if err := c.Initialize(ctx); err != nil {
		return menu, err
	}
	id, release, err := openACPCatalogSession(ctx, c, nativeID, cwd, previewModel != "")
	if err != nil {
		return menu, err
	}
	defer release()
	if previewModel == "" && nativeID != "" && id != nativeID {
		return menu, fmt.Errorf("ACP 返回了其他会话的模型目录")
	}
	if previewModel != "" {
		probe, ok := c.(interface {
			selectModel(context.Context, string, string) error
		})
		if !ok || (nativeID != "" && id == nativeID) {
			return menu, fmt.Errorf("ACP 适配器未提供独立的模型预览会话")
		}
		if err := probe.selectModel(ctx, id, previewModel); err != nil {
			return menu, err
		}
	}
	m, ok := reader.availableModels(ctx, id)
	if !ok {
		return menu, fmt.Errorf("ACP 会话没有公布可切换的模型")
	}
	return m.ModelCatalog, nil
}

// ACP advertises capabilities after opening a session. Only sessions created
// for a probe may be removed. An existing conversation's binding stays intact.
func openACPCatalogSession(ctx context.Context, c ACPClient, nativeID, cwd string, fresh bool) (string, func(), error) {
	release := func() {}
	if !fresh && nativeID != "" {
		if !c.LoadSessionSupported() {
			return "", release, fmt.Errorf("ACP 适配器无法恢复此会话的目录")
		}
		id, err := c.LoadSession(ctx, nativeID, cwd)
		return id, release, err
	}
	id, err := c.NewSession(ctx, cwd)
	if probe, ok := c.(*stdioACPClient); err == nil && ok && id != nativeID {
		method := ""
		if probe.deleteSessionSupported {
			method = "session/delete"
		} else if probe.closeSessionSupported {
			method = "session/close"
		}
		if method != "" {
			release = func() {
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
				defer cancel()
				_, _ = probe.request(cleanup, method, map[string]string{"sessionId": id})
			}
		}
	}
	return id, release, err
}
