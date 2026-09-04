package server

import (
	"fmt"
	"strings"

	"bqagent/internal/session"
)

type configuredModel struct {
	Alias string
	ID    string
}

// ModelOption is a read-only model choice exposed to interactive clients.
type ModelOption struct {
	Alias string
	ID    string
}

// ModelOptions returns a copy of the configured switchable model list.
func (service *Service) ModelOptions() []ModelOption {
	options := make([]ModelOption, 0, len(service.models)+1)
	options = append(options, ModelOption{Alias: "default", ID: service.model})
	for _, model := range service.models {
		options = append(options, ModelOption{Alias: model.Alias, ID: model.ID})
	}
	return options
}

func parseConfiguredModels(values []string) []configuredModel {
	models := make([]configuredModel, 0, len(values))
	seenAliases := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		alias := value
		modelID := value
		if left, right, ok := strings.Cut(value, "="); ok {
			alias = strings.TrimSpace(left)
			modelID = strings.TrimSpace(right)
		}
		if alias == "" || modelID == "" || strings.EqualFold(alias, "default") {
			continue
		}
		key := strings.ToLower(alias)
		if _, exists := seenAliases[key]; exists {
			continue
		}
		seenAliases[key] = struct{}{}
		models = append(models, configuredModel{Alias: alias, ID: modelID})
	}
	return models
}

func (service *Service) resolveConfiguredModel(input string) (string, bool) {
	input = strings.TrimSpace(input)
	for _, model := range service.models {
		if strings.EqualFold(input, model.Alias) || input == model.ID {
			return model.ID, true
		}
	}
	return "", false
}

func (service *Service) effectiveModel(savedSession *session.Session) string {
	if savedSession != nil {
		if current := strings.TrimSpace(savedSession.Meta().CurrentModel); current != "" {
			return current
		}
	}
	return service.model
}

func (service *Service) modelIsConfigured(modelID string) bool {
	for _, model := range service.models {
		if model.ID == modelID {
			return true
		}
	}
	return false
}

func (service *Service) setSessionModel(sessionID, model string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	canonicalID, err := session.CanonicalID(sessionID)
	if err != nil {
		return err
	}
	unlock := service.locker.Lock(canonicalID)
	defer unlock()
	savedSession, err := service.store.Open(canonicalID)
	if err != nil {
		return err
	}
	modelID, ok := service.resolveConfiguredModel(model)
	if !ok {
		return fmt.Errorf("unknown model %q", model)
	}
	return savedSession.SetCurrentModel(modelID)
}

// SelectSessionModel applies a configured model to an existing session. It is
// used by interactive clients after changing the active Provider.
func (service *Service) SelectSessionModel(sessionID, model string) error {
	return service.setSessionModel(sessionID, model)
}

func (service *Service) handleModelCommand(message string, savedSession *session.Session) (string, bool, error) {
	fields := strings.Fields(strings.TrimSpace(message))
	if len(fields) == 0 || !strings.EqualFold(fields[0], "/model") {
		return "", false, nil
	}
	if len(fields) > 2 {
		return "", true, fmt.Errorf("用法：/model、/model <名称或别名>、/model default")
	}
	if len(fields) == 1 {
		if len(service.models) == 0 {
			return "", true, fmt.Errorf("未配置可切换模型，请通过 LLM_MODELS 预先配置模型列表")
		}
		current := service.effectiveModel(savedSession)
		var reply strings.Builder
		fmt.Fprintf(&reply, "当前模型：%s\n默认模型：%s\n可用模型：\n", current, service.model)
		for _, model := range service.models {
			if model.Alias == model.ID {
				fmt.Fprintf(&reply, "- %s\n", model.ID)
			} else {
				fmt.Fprintf(&reply, "- %s = %s\n", model.Alias, model.ID)
			}
		}
		if current != service.model && !service.modelIsConfigured(current) {
			reply.WriteString("\n当前会话保存的模型不在现有 LLM_MODELS 中，但仍将继续使用。\n")
		}
		reply.WriteString("\n使用 /model <名称或别名> 切换，/model default 恢复默认模型。")
		return reply.String(), true, nil
	}
	if savedSession == nil {
		return "", true, fmt.Errorf("session is required")
	}
	selection := fields[1]
	if strings.EqualFold(selection, "default") {
		if err := savedSession.SetCurrentModel(""); err != nil {
			return "", true, err
		}
		return fmt.Sprintf("已恢复默认模型：%s", service.model), true, nil
	}
	if len(service.models) == 0 {
		return "", true, fmt.Errorf("未配置可切换模型，请通过 LLM_MODELS 预先配置模型列表")
	}
	modelID, ok := service.resolveConfiguredModel(selection)
	if !ok {
		return "", true, fmt.Errorf("未知模型 %q，请使用 /model 查看可用模型", selection)
	}
	if err := savedSession.SetCurrentModel(modelID); err != nil {
		return "", true, err
	}
	return fmt.Sprintf("已切换当前会话模型：%s", modelID), true, nil
}
