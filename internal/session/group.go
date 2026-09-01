package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bqagent/internal/atomicfile"
)

const GroupConfigVersion = 1

type GroupConfig struct {
	Version      int       `json:"version"`
	Scheduler    string    `json:"scheduler"`
	Participants []string  `json:"participants"`
	CreatedAt    time.Time `json:"created_at"`
}

func (session *Session) GroupConfigPath() string {
	return filepath.Join(session.Dir(), "group.json")
}

func (session *Session) SaveGroupConfig(config GroupConfig) error {
	if config.Version <= 0 {
		config.Version = GroupConfigVersion
	}
	config.Scheduler = strings.TrimSpace(config.Scheduler)
	if config.CreatedAt.IsZero() {
		config.CreatedAt = time.Now().UTC()
	}
	content, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(session.Dir(), 0o755); err != nil {
		return err
	}
	return atomicfile.Write(session.GroupConfigPath(), append(content, '\n'), 0o644)
}

func (session *Session) LoadGroupConfig() (GroupConfig, error) {
	content, err := os.ReadFile(session.GroupConfigPath())
	if err != nil {
		return GroupConfig{}, err
	}
	var config GroupConfig
	if err := json.Unmarshal(content, &config); err != nil {
		return GroupConfig{}, err
	}
	return config, nil
}
