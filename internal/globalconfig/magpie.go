package globalconfig

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"bqagent/internal/modelcatalog"
)

// MagpieConfig is owned and restored by the Magpie Agent plugin. Keep it in a
// separate file so provider edits cannot overwrite it or expose its gateway key.
type MagpieConfig struct {
	Model             string             `json:"model"`
	BaseURL           string             `json:"base_url"`
	APIKey            string             `json:"api_key"`
	Models            []string           `json:"models"`
	ModelDisplayNames modelcatalog.Names `json:"model_display_names"`
}

func LoadMagpie(agentDir string) (*MagpieConfig, error) {
	data, err := os.ReadFile(filepath.Join(agentDir, "magpie.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var config MagpieConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	config.Model = strings.TrimSpace(config.Model)
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	// Magpie restores/removes individual keys when disconnected. Only a
	// complete connection overrides the user's existing provider or environment.
	if config.Model == "" || config.BaseURL == "" || strings.TrimSpace(config.APIKey) == "" {
		return nil, nil
	}
	endpoint, err := url.Parse(config.BaseURL)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return nil, errors.New("invalid Magpie gateway URL")
	}
	models := []string{config.Model}
	seen := map[string]bool{config.Model: true}
	for _, model := range config.Models {
		model = strings.TrimSpace(model)
		if model != "" && !seen[model] {
			models = append(models, model)
			seen[model] = true
		}
	}
	config.Models = models
	config.ModelDisplayNames = config.ModelDisplayNames.ForModels(models)
	return &config, nil
}
