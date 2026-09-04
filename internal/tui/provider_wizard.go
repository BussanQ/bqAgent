package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type providerStep uint8

const (
	providerStepLoading providerStep = iota
	providerStepChoose
	providerStepID
	providerStepAPIType
	providerStepBaseURL
	providerStepAPIKey
	providerStepModelSource
	providerStepModels
	providerStepDiscovering
	providerStepConfirm
	providerStepSaving
)

type providerModelSource uint8

const (
	providerModelsAutomatic providerModelSource = iota
	providerModelsCustom
)

type providerWizard struct {
	step        providerStep
	settings    ProviderSettings
	draft       ProviderInput
	input       textinput.Model
	selected    int
	modelSource providerModelSource
	err         string
	width       int
}

type providerSettingsMsg struct {
	settings ProviderSettings
	err      error
}

type providerModelsMsg struct {
	models []string
	err    error
}

type providerSavedMsg struct {
	runtime RuntimeInfo
	err     error
}

func newProviderWizard(noColor bool, width int) *providerWizard {
	input := textinput.New()
	input.Prompt = "› "
	input.CharLimit = 0
	input.SetVirtualCursor(false)
	if noColor {
		styles := input.Styles()
		styles.Focused = textinput.StyleState{}
		styles.Blurred = textinput.StyleState{}
		styles.Cursor = textinput.CursorStyle{}
		input.SetStyles(styles)
	}
	wizard := &providerWizard{step: providerStepLoading, input: input}
	wizard.resize(width)
	return wizard
}

func (wizard *providerWizard) resize(width int) {
	wizard.width = max(20, width)
	wizard.input.SetWidth(max(10, wizard.width-2))
}

func (wizard *providerWizard) applySettings(settings ProviderSettings) tea.Cmd {
	wizard.settings = settings
	wizard.err = ""
	if len(settings.Providers) == 0 {
		wizard.draft = ProviderInput{APIType: "openai"}
		return wizard.setTextStep(providerStepID)
	}
	wizard.step = providerStepChoose
	wizard.selected = 0
	for index, provider := range settings.Providers {
		if provider.ID == settings.ActiveProvider {
			wizard.selected = index + 1
			break
		}
	}
	wizard.input.Blur()
	return nil
}

func (wizard *providerWizard) beginSelectedProvider() tea.Cmd {
	wizard.err = ""
	wizard.modelSource = providerModelsAutomatic
	if wizard.selected <= 0 {
		wizard.draft = ProviderInput{APIType: "openai"}
	} else {
		provider := wizard.settings.Providers[wizard.selected-1]
		wizard.draft = ProviderInput{
			OriginalID: provider.ID, ID: provider.ID, Name: provider.Name,
			APIType: provider.APIType, BaseURL: provider.BaseURL,
			Models: append([]string(nil), provider.Models...), DefaultModel: provider.DefaultModel,
			APIKeyConfigured: provider.APIKeyConfigured,
		}
	}
	return wizard.setTextStep(providerStepID)
}

func (wizard *providerWizard) setTextStep(step providerStep) tea.Cmd {
	wizard.step = step
	wizard.err = ""
	wizard.input.EchoMode = textinput.EchoNormal
	wizard.input.Placeholder = ""
	value := ""
	switch step {
	case providerStepID:
		value = wizard.draft.ID
		wizard.input.Placeholder = "openai"
	case providerStepBaseURL:
		value = wizard.draft.BaseURL
		wizard.input.Placeholder = defaultProviderBaseURL(wizard.draft.APIType)
	case providerStepAPIKey:
		value = wizard.draft.APIKey
		wizard.input.EchoMode = textinput.EchoPassword
		if wizard.draft.APIKeyConfigured {
			wizard.input.Placeholder = "已保存；留空保持不变"
		} else {
			wizard.input.Placeholder = "输入 API Key"
		}
	case providerStepModels:
		value = strings.Join(wizard.draft.Models, ", ")
		wizard.input.Placeholder = "model-a, model-b"
	}
	wizard.input.SetValue(value)
	wizard.input.CursorEnd()
	wizard.resize(wizard.width)
	return wizard.input.Focus()
}

func (wizard *providerWizard) captureInput() {
	value := strings.TrimSpace(wizard.input.Value())
	switch wizard.step {
	case providerStepID:
		wizard.draft.ID = value
	case providerStepBaseURL:
		wizard.draft.BaseURL = value
	case providerStepAPIKey:
		wizard.draft.APIKey = value
	case providerStepModels:
		wizard.draft.Models = parseProviderModels(value)
		if len(wizard.draft.Models) > 0 {
			wizard.draft.DefaultModel = wizard.draft.Models[0]
		}
	}
}

func (wizard *providerWizard) advanceTextStep() tea.Cmd {
	wizard.captureInput()
	wizard.err = ""
	switch wizard.step {
	case providerStepID:
		if wizard.draft.ID == "" || strings.ContainsAny(wizard.draft.ID, " /\\") {
			wizard.err = "Provider ID 不能为空，且不能包含空格、/ 或 \\."
			return nil
		}
		wizard.draft.Name = wizard.draft.ID
		wizard.step = providerStepAPIType
		wizard.selected = providerAPITypeIndex(wizard.draft.APIType)
		wizard.input.Blur()
		return nil
	case providerStepBaseURL:
		return wizard.setTextStep(providerStepAPIKey)
	case providerStepAPIKey:
		wizard.step = providerStepModelSource
		wizard.selected = int(wizard.modelSource)
		wizard.input.Blur()
		return nil
	case providerStepModels:
		if len(wizard.draft.Models) == 0 {
			wizard.err = "请至少输入一个模型；多个模型使用英文逗号分隔。"
			return nil
		}
		wizard.draft.DefaultModel = wizard.draft.Models[0]
		wizard.step = providerStepConfirm
		wizard.input.Blur()
	}
	return nil
}

func (wizard *providerWizard) previousStep() tea.Cmd {
	wizard.captureInput()
	switch wizard.step {
	case providerStepAPIType:
		return wizard.setTextStep(providerStepID)
	case providerStepBaseURL:
		wizard.step = providerStepAPIType
		wizard.selected = providerAPITypeIndex(wizard.draft.APIType)
		wizard.input.Blur()
	case providerStepAPIKey:
		return wizard.setTextStep(providerStepBaseURL)
	case providerStepModelSource:
		return wizard.setTextStep(providerStepAPIKey)
	case providerStepModels:
		wizard.step = providerStepModelSource
		wizard.selected = int(wizard.modelSource)
		wizard.input.Blur()
	case providerStepConfirm:
		if wizard.modelSource == providerModelsCustom {
			return wizard.setTextStep(providerStepModels)
		}
		wizard.step = providerStepModelSource
		wizard.selected = int(wizard.modelSource)
	}
	wizard.err = ""
	return nil
}

func (wizard *providerWizard) updateInput(message tea.Msg) tea.Cmd {
	var command tea.Cmd
	wizard.input, command = wizard.input.Update(message)
	return command
}

func (model *Model) handleProviderKey(key tea.KeyPressMsg) []tea.Cmd {
	wizard := model.provider
	if wizard == nil {
		return nil
	}
	switch key.String() {
	case "ctrl+q":
		model.provider = nil
		return []tea.Cmd{tea.Quit}
	case "esc":
		model.provider = nil
		model.statusNotice = "已取消 Provider 配置"
		return []tea.Cmd{model.input.area.Focus()}
	case "shift+tab":
		if wizard.step != providerStepLoading && wizard.step != providerStepChoose && wizard.step != providerStepDiscovering && wizard.step != providerStepSaving {
			return commandSlice(wizard.previousStep())
		}
		return nil
	}

	switch wizard.step {
	case providerStepLoading:
		if key.String() == "enter" && wizard.err != "" {
			wizard.err = ""
			return []tea.Cmd{loadProviderSettings(model.backend, model.config.Context)}
		}
	case providerStepChoose:
		switch key.String() {
		case "up":
			wizard.selected = (wizard.selected - 1 + len(wizard.settings.Providers) + 1) % (len(wizard.settings.Providers) + 1)
		case "down":
			wizard.selected = (wizard.selected + 1) % (len(wizard.settings.Providers) + 1)
		case "enter":
			return commandSlice(wizard.beginSelectedProvider())
		}
	case providerStepAPIType:
		switch key.String() {
		case "up":
			wizard.selected = (wizard.selected - 1 + len(providerAPITypeValues())) % len(providerAPITypeValues())
		case "down":
			wizard.selected = (wizard.selected + 1) % len(providerAPITypeValues())
		case "enter":
			wizard.draft.APIType = providerAPITypeValues()[wizard.selected]
			return commandSlice(wizard.setTextStep(providerStepBaseURL))
		}
	case providerStepModelSource:
		switch key.String() {
		case "up", "down":
			wizard.selected = 1 - wizard.selected
		case "enter":
			wizard.modelSource = providerModelSource(wizard.selected)
			wizard.err = ""
			if wizard.modelSource == providerModelsCustom {
				return commandSlice(wizard.setTextStep(providerStepModels))
			}
			wizard.step = providerStepDiscovering
			return []tea.Cmd{discoverProviderModels(model.backend, model.config.Context, wizard.draft)}
		}
	case providerStepConfirm:
		if key.String() == "enter" {
			wizard.step = providerStepSaving
			wizard.err = ""
			return []tea.Cmd{saveProvider(model.backend, model.config.Context, model.sessionID, wizard.draft)}
		}
	case providerStepDiscovering, providerStepSaving:
		return nil
	default:
		if key.String() == "enter" {
			return commandSlice(wizard.advanceTextStep())
		}
		return commandSlice(wizard.updateInput(key))
	}
	return nil
}

func (model *Model) handleProviderSettings(message providerSettingsMsg) []tea.Cmd {
	if model.provider == nil {
		return nil
	}
	if message.err != nil {
		model.provider.step = providerStepLoading
		model.provider.err = "读取 Provider 配置失败：" + message.err.Error()
		return nil
	}
	return commandSlice(model.provider.applySettings(message.settings))
}

func (model *Model) handleProviderModels(message providerModelsMsg) []tea.Cmd {
	if model.provider == nil || model.provider.step != providerStepDiscovering {
		return nil
	}
	if message.err != nil {
		model.provider.step = providerStepModelSource
		model.provider.selected = int(providerModelsAutomatic)
		model.provider.err = "自动获取模型失败：" + message.err.Error() + "；可重试或选择自定义模型。"
		return nil
	}
	models := parseProviderModels(strings.Join(message.models, ","))
	if len(models) == 0 {
		model.provider.step = providerStepModelSource
		model.provider.err = "Provider 未返回可用模型；请选择自定义模型。"
		return nil
	}
	model.provider.draft.Models = models
	model.provider.draft.DefaultModel = models[0]
	model.provider.step = providerStepConfirm
	model.provider.err = ""
	return nil
}

func (model *Model) handleProviderSaved(message providerSavedMsg) []tea.Cmd {
	if model.provider == nil || model.provider.step != providerStepSaving {
		return nil
	}
	if message.err != nil {
		model.provider.step = providerStepConfirm
		model.provider.err = "保存 Provider 失败：" + message.err.Error()
		return nil
	}
	providerID := model.provider.draft.ID
	defaultModel := model.provider.draft.DefaultModel
	model.provider = nil
	model.runtime = message.runtime
	if model.runtime.Model == "" {
		model.runtime = model.backend.RuntimeInfo(model.sessionID)
	}
	model.commands = mergeCommands(model.backend.Commands())
	model.statusNotice = "Provider 已保存并启用"
	model.enqueuePrint(model.styles.ok.Render(fmt.Sprintf("Provider %s 已启用，默认模型：%s", providerID, defaultModel)))
	return []tea.Cmd{model.input.area.Focus()}
}

func commandSlice(command tea.Cmd) []tea.Cmd {
	if command == nil {
		return nil
	}
	return []tea.Cmd{command}
}

func (wizard *providerWizard) render(styles theme) (string, *tea.Cursor) {
	lines := []string{wizard.renderHeader(styles), ""}
	inputLine := -1
	appendInput := func(label, description string) {
		lines = append(lines, styles.accent.Render(label))
		if description != "" {
			lines = append(lines, styles.dim.Render(description))
		}
		lines = append(lines, "")
		inputLine = len(lines)
		lines = append(lines, wizard.input.View(), "", styles.dim.Render("Enter 下一步  ·  Shift+Tab 返回  ·  Esc 取消"))
	}
	switch wizard.step {
	case providerStepLoading:
		lines = append(lines, "正在读取 Provider 配置…")
	case providerStepChoose:
		lines = append(lines, styles.accent.Render("选择要配置的 Provider"), styles.dim.Render("可编辑已有配置，或添加新的 Provider。"), "")
		labels := []string{"添加 Provider"}
		for _, provider := range wizard.settings.Providers {
			label := provider.Name + "  " + styles.dim.Render(provider.ID)
			if provider.ID == wizard.settings.ActiveProvider {
				label += "  " + styles.ok.Render("当前")
			}
			labels = append(labels, label)
		}
		lines = append(lines, renderProviderOptions(labels, wizard.selected)...)
		lines = append(lines, "", styles.dim.Render("↑/↓ 选择  ·  Enter 继续  ·  Esc 取消"))
	case providerStepID:
		appendInput("Provider ID", "配置的唯一标识，例如 openai、deepseek 或 local。")
	case providerStepAPIType:
		lines = append(lines, styles.accent.Render("API 协议"), styles.dim.Render("选择 Provider 使用的接口协议。"), "")
		lines = append(lines, renderProviderOptions(providerAPITypeLabels(), wizard.selected)...)
		lines = append(lines, "", styles.dim.Render("↑/↓ 选择  ·  Enter 下一步  ·  Shift+Tab 返回  ·  Esc 取消"))
	case providerStepBaseURL:
		appendInput("API 地址", "留空时使用所选协议的官方默认地址。")
	case providerStepAPIKey:
		description := "API Key 会加密保存在全局 ~/.agent/config.json。"
		if wizard.draft.APIKeyConfigured {
			description += " 留空可保留已保存的密钥。"
		}
		appendInput("API Key", description)
	case providerStepModelSource:
		lines = append(lines, styles.accent.Render("模型配置方式"), styles.dim.Render("自动调用 Provider 的 /models 接口，或手工输入模型。"), "")
		lines = append(lines, renderProviderOptions([]string{"自动获取可用模型", "自定义模型"}, wizard.selected)...)
		lines = append(lines, "", styles.dim.Render("↑/↓ 选择  ·  Enter 下一步  ·  Shift+Tab 返回  ·  Esc 取消"))
	case providerStepModels:
		appendInput("自定义模型", "多个模型使用英文逗号分隔；第一个模型将作为默认模型。")
	case providerStepDiscovering:
		lines = append(lines, "正在从 Provider 获取模型…", "", styles.dim.Render("Esc 取消"))
	case providerStepConfirm:
		lines = append(lines, styles.accent.Render("确认配置"), "")
		lines = append(lines,
			providerSummaryLine("Provider", wizard.draft.Name+" ("+wizard.draft.ID+")", styles, wizard.width),
			providerSummaryLine("协议", providerAPITypeLabels()[providerAPITypeIndex(wizard.draft.APIType)], styles, wizard.width),
			providerSummaryLine("API 地址", displayProviderBaseURL(wizard.draft.APIType, wizard.draft.BaseURL), styles, wizard.width),
			providerSummaryLine("API Key", providerKeySummary(wizard.draft), styles, wizard.width),
			providerSummaryLine("模型", fmt.Sprintf("%d 个；默认 %s", len(wizard.draft.Models), wizard.draft.DefaultModel), styles, wizard.width),
			"", styles.dim.Render("Enter 保存并启用  ·  Shift+Tab 返回  ·  Esc 取消"),
		)
	case providerStepSaving:
		lines = append(lines, "正在保存并启用 Provider…")
	}
	if wizard.err != "" {
		lines = append(lines, "", styles.error.Render(wizard.err))
		if wizard.step == providerStepLoading {
			lines = append(lines, styles.dim.Render("Enter 重试  ·  Esc 取消"))
		}
	}
	content := strings.Join(lines, "\n")
	if inputLine < 0 {
		return content, nil
	}
	cursor := wizard.input.Cursor()
	if cursor != nil {
		cursor.Position.Y += inputLine
	}
	return content, cursor
}

func (wizard providerWizard) renderHeader(styles theme) string {
	left := styles.accent.Render("Provider 配置")
	if step := wizard.stepNumber(); step > 0 {
		left += styles.dim.Render(fmt.Sprintf("  %d/7", step))
	}
	right := styles.dim.Render("esc")
	space := wizard.width - lipgloss.Width(left) - lipgloss.Width(right)
	if space < 1 {
		return left
	}
	return left + strings.Repeat(" ", space) + right
}

func (wizard providerWizard) stepNumber() int {
	switch wizard.step {
	case providerStepID:
		return 1
	case providerStepAPIType:
		return 2
	case providerStepBaseURL:
		return 3
	case providerStepAPIKey:
		return 4
	case providerStepModelSource:
		return 5
	case providerStepModels, providerStepDiscovering:
		return 6
	case providerStepConfirm, providerStepSaving:
		return 7
	default:
		return 0
	}
}

func renderProviderOptions(options []string, selected int) []string {
	lines := make([]string, 0, len(options))
	for index, option := range options {
		prefix := "  "
		if index == selected {
			prefix = "› "
		}
		lines = append(lines, prefix+option)
	}
	return lines
}

func providerSummaryLine(label, value string, styles theme, width int) string {
	value = ansi.Truncate(value, max(8, width-14), "…")
	return fmt.Sprintf("%-10s %s", label, styles.dim.Render(value))
}

func providerKeySummary(input ProviderInput) string {
	if strings.TrimSpace(input.APIKey) != "" {
		return "将保存新密钥"
	}
	if input.APIKeyConfigured {
		return "保留已保存密钥"
	}
	return "未配置"
}

func providerAPITypeLabels() []string {
	return []string{"OpenAI Chat Completions", "OpenAI Responses", "Anthropic Messages"}
}

func providerAPITypeValues() []string { return []string{"openai", "openai-response", "anthropic"} }

func providerAPITypeIndex(value string) int {
	for index, candidate := range providerAPITypeValues() {
		if candidate == value {
			return index
		}
	}
	return 0
}

func defaultProviderBaseURL(apiType string) string {
	if apiType == "anthropic" {
		return "https://api.anthropic.com/v1"
	}
	return "https://api.openai.com/v1"
}

func displayProviderBaseURL(apiType, baseURL string) string {
	if value := strings.TrimSpace(baseURL); value != "" {
		return value
	}
	return defaultProviderBaseURL(apiType) + "（默认）"
}

func parseProviderModels(value string) []string {
	seen := make(map[string]bool)
	models := make([]string, 0)
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		models = append(models, item)
	}
	return models
}

func loadProviderSettings(backend Backend, ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		settings, err := backend.ProviderSettings(ctx)
		return providerSettingsMsg{settings: settings, err: err}
	}
}

func discoverProviderModels(backend Backend, ctx context.Context, input ProviderInput) tea.Cmd {
	return func() tea.Msg {
		models, err := backend.DiscoverProviderModels(ctx, input)
		return providerModelsMsg{models: models, err: err}
	}
}

func saveProvider(backend Backend, ctx context.Context, sessionID string, input ProviderInput) tea.Cmd {
	return func() tea.Msg {
		runtime, err := backend.SaveProvider(ctx, sessionID, input)
		return providerSavedMsg{runtime: runtime, err: err}
	}
}
