package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"bqagent/internal/agent"
	"bqagent/internal/providerconfig"
)

const maxProviderModelsResponseBytes = 2 << 20

type providerView struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	APIType          string   `json:"api_type"`
	BaseURL          string   `json:"base_url,omitempty"`
	Models           []string `json:"models"`
	DefaultModel     string   `json:"default_model"`
	APIKeyConfigured bool     `json:"api_key_configured"`
}

type providerSettingsResponse struct {
	ActiveProvider string         `json:"active_provider"`
	Providers      []providerView `json:"providers"`
}

type providerInput struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	APIType      string   `json:"api_type"`
	BaseURL      string   `json:"base_url"`
	Models       []string `json:"models"`
	DefaultModel string   `json:"default_model"`
	APIKey       string   `json:"api_key"`
}

type providerSettingsInput struct {
	ActiveProvider string          `json:"active_provider"`
	Providers      []providerInput `json:"providers"`
}

func (handler *handler) handleProviders(writer http.ResponseWriter, request *http.Request) {
	if handler.providers == nil {
		writeError(writer, http.StatusServiceUnavailable, chatResponse{Error: "provider settings unavailable"})
		return
	}
	handler.providerMu.Lock()
	defer handler.providerMu.Unlock()
	switch request.Method {
	case http.MethodGet:
		config, err := handler.providers.Load()
		if err != nil {
			writeError(writer, http.StatusInternalServerError, chatResponse{Error: err.Error()})
			return
		}
		writeJSON(writer, http.StatusOK, providerResponse(config))
	case http.MethodPut:
		var input providerSettingsInput
		if err := decodeProviderJSON(writer, request, &input); err != nil {
			writeError(writer, http.StatusBadRequest, chatResponse{Error: err.Error()})
			return
		}
		existing, err := handler.providers.Load()
		if err != nil {
			writeError(writer, http.StatusInternalServerError, chatResponse{Error: err.Error()})
			return
		}
		config, err := handler.buildProviderConfig(input, existing)
		if err != nil {
			writeError(writer, http.StatusBadRequest, chatResponse{Error: err.Error()})
			return
		}
		if err := handler.providers.Save(config); err != nil {
			writeError(writer, http.StatusBadRequest, chatResponse{Error: err.Error()})
			return
		}
		service, err := handler.serviceForWorkspace(request.URL.Query().Get("workspace_id"))
		if err == nil {
			_ = handler.applyProvider(service, config)
		}
		writeJSON(writer, http.StatusOK, providerResponse(config))
	default:
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
	}
}

func (handler *handler) handleProviderSelection(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
		return
	}
	if handler.providers == nil {
		writeError(writer, http.StatusServiceUnavailable, chatResponse{Error: "provider settings unavailable"})
		return
	}
	var selection struct {
		ProviderID string `json:"provider_id"`
		Model      string `json:"model"`
		SessionID  string `json:"session_id"`
	}
	if err := decodeProviderJSON(writer, request, &selection); err != nil {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: err.Error()})
		return
	}
	handler.providerMu.Lock()
	defer handler.providerMu.Unlock()
	config, err := handler.providers.Load()
	if err != nil {
		writeError(writer, http.StatusInternalServerError, chatResponse{Error: err.Error()})
		return
	}
	found := false
	for index := range config.Providers {
		provider := &config.Providers[index]
		if provider.ID != selection.ProviderID {
			continue
		}
		for _, model := range provider.Models {
			if model == selection.Model {
				provider.DefaultModel = model
				found = true
				break
			}
		}
	}
	if !found {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: "unknown provider or model"})
		return
	}
	config.ActiveProvider = selection.ProviderID
	if err := handler.providers.Save(config); err != nil {
		writeError(writer, http.StatusInternalServerError, chatResponse{Error: err.Error()})
		return
	}
	service, err := handler.serviceForWorkspace(request.URL.Query().Get("workspace_id"))
	if err != nil || handler.applyProvider(service, config) != nil {
		writeError(writer, http.StatusInternalServerError, chatResponse{Error: "apply provider failed"})
		return
	}
	if err := service.setSessionModel(selection.SessionID, selection.Model); err != nil {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: fmt.Sprintf("apply session model: %v", err)})
		return
	}
	writeJSON(writer, http.StatusOK, service.RuntimeLLMInfoForSession(selection.SessionID))
}

func (handler *handler) handleProviderModels(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
		return
	}
	if handler.providers == nil {
		writeError(writer, http.StatusServiceUnavailable, chatResponse{Error: "provider settings unavailable"})
		return
	}
	var input struct {
		ProviderID string `json:"provider_id"`
		APIType    string `json:"api_type"`
		BaseURL    string `json:"base_url"`
		APIKey     string `json:"api_key"`
	}
	if err := decodeProviderJSON(writer, request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: err.Error()})
		return
	}
	apiKey := strings.TrimSpace(input.APIKey)
	apiType := string(agent.NormalizeAPIType(input.APIType))
	baseURL := strings.TrimSpace(input.BaseURL)
	if apiKey == "" && strings.TrimSpace(input.ProviderID) != "" {
		handler.providerMu.Lock()
		config, err := handler.providers.Load()
		if err == nil {
			for _, provider := range config.Providers {
				if provider.ID == input.ProviderID {
					apiKey, err = handler.providers.DecryptAPIKey(provider.APIKey)
					// Never combine a persisted secret with a caller-controlled URL:
					// otherwise this endpoint could be used to exfiltrate the key.
					baseURL = provider.BaseURL
					apiType = provider.APIType
					break
				}
			}
		}
		handler.providerMu.Unlock()
		if err != nil {
			writeError(writer, http.StatusBadRequest, chatResponse{Error: err.Error()})
			return
		}
	}
	models, err := FetchProviderModels(request.Context(), apiType, baseURL, apiKey)
	if err != nil {
		writeError(writer, http.StatusBadGateway, chatResponse{Error: err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"models": models})
}

// FetchProviderModels returns the model IDs exposed by an OpenAI-compatible or
// Anthropic-compatible Provider. Interactive clients share this implementation
// so model discovery behaves the same in the WebUI and TUI.
func FetchProviderModels(ctx context.Context, apiType, baseURL, apiKey string) ([]string, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("API Key 未配置")
	}
	if strings.TrimSpace(baseURL) == "" {
		if agent.NormalizeAPIType(apiType) == agent.APITypeAnthropic {
			baseURL = "https://api.anthropic.com/v1"
		} else {
			baseURL = "https://api.openai.com/v1"
		}
	}
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/") + "/models")
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("无效的 Provider API 地址")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if agent.NormalizeAPIType(apiType) == agent.APITypeAnthropic {
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("获取模型失败: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxProviderModelsResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxProviderModelsResponseBytes {
		return nil, fmt.Errorf("模型列表响应过大")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Provider 返回 HTTP %d", response.StatusCode)
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("解析模型列表失败: %w", err)
	}
	seen := map[string]bool{}
	models := make([]string, 0, len(payload.Data)+len(payload.Models))
	for _, item := range append(payload.Data, payload.Models...) {
		id := strings.TrimSpace(item.ID)
		if id != "" && !seen[id] {
			seen[id] = true
			models = append(models, id)
		}
	}
	sort.Strings(models)
	if len(models) == 0 {
		return nil, fmt.Errorf("Provider 未返回可用模型")
	}
	return models, nil
}

func (handler *handler) buildProviderConfig(input providerSettingsInput, existing providerconfig.Config) (providerconfig.Config, error) {
	secrets := map[string]providerconfig.Secret{}
	for _, provider := range existing.Providers {
		secrets[provider.ID] = provider.APIKey
	}
	config := providerconfig.Config{ActiveProvider: strings.TrimSpace(input.ActiveProvider), Providers: make([]providerconfig.Provider, 0, len(input.Providers)), WebUI: existing.WebUI}
	for _, value := range input.Providers {
		secret := secrets[strings.TrimSpace(value.ID)]
		if strings.TrimSpace(value.APIKey) != "" {
			var err error
			secret, err = handler.providers.EncryptAPIKey(value.APIKey)
			if err != nil {
				return providerconfig.Config{}, err
			}
		}
		models := cleanModels(value.Models)
		config.Providers = append(config.Providers, providerconfig.Provider{ID: strings.TrimSpace(value.ID), Name: strings.TrimSpace(value.Name), APIType: string(agent.NormalizeAPIType(value.APIType)), BaseURL: strings.TrimSpace(value.BaseURL), Models: models, DefaultModel: strings.TrimSpace(value.DefaultModel), APIKey: secret})
	}
	return config, nil
}

func (handler *handler) applySavedProvider(service *Service) {
	config, err := handler.providers.Load()
	if err == nil {
		_ = handler.applyProvider(service, config)
	}
}

func (handler *handler) applyProvider(service *Service, config providerconfig.Config) error {
	for _, provider := range config.Providers {
		if provider.ID != config.ActiveProvider {
			continue
		}
		apiKey, err := handler.providers.DecryptAPIKey(provider.APIKey)
		if err != nil {
			return err
		}
		service.ConfigureLLM(provider.ID, agent.NormalizeAPIType(provider.APIType), apiKey, provider.BaseURL, provider.DefaultModel, provider.Models)
		return nil
	}
	return nil
}

func providerResponse(config providerconfig.Config) providerSettingsResponse {
	response := providerSettingsResponse{ActiveProvider: config.ActiveProvider, Providers: make([]providerView, 0, len(config.Providers))}
	for _, provider := range config.Providers {
		response.Providers = append(response.Providers, providerView{ID: provider.ID, Name: provider.Name, APIType: provider.APIType, BaseURL: provider.BaseURL, Models: append([]string(nil), provider.Models...), DefaultModel: provider.DefaultModel, APIKeyConfigured: provider.APIKey.Ciphertext != ""})
	}
	return response
}

func cleanModels(values []string) []string {
	models := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			models = append(models, value)
		}
	}
	return models
}

func decodeProviderJSON(writer http.ResponseWriter, request *http.Request, target any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBodySize)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid provider settings: %w", err)
	}
	return nil
}
