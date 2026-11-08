package extagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"bqagent/internal/privatefile"
	"bqagent/internal/session"
)

// ModelPreferences holds explicit composer choices. The active native session
// and its confirmed settings remain in the session store.
type ModelPreferences struct {
	Model      string            `json:"model,omitempty"`
	Custom     bool              `json:"custom,omitempty"`
	Efforts    map[string]string `json:"efforts,omitempty"`
	NewSession bool              `json:"new_session,omitempty"` // Derived for the reader, never set on a stored pick.
}

type ModelPick struct {
	Model  string `json:"model"`
	Custom bool   `json:"custom,omitempty"`
	// Nil is a model pick. An empty effort removes this model's remembered level.
	Effort *string `json:"effort,omitempty"`
}

func (b *Broker) preferencePath(agent AgentName) (string, error) {
	if b == nil || b.store == nil || !slices.Contains(SupportedAgents(), agent) {
		return "", fmt.Errorf("未知的外部 Agent %q", agent)
	}
	return filepath.Join(filepath.Dir(b.store.root), "model-preferences", string(agent)+".json"), nil
}

func readModelPreferences(path string) (ModelPreferences, error) {
	var prefs ModelPreferences
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return prefs, nil
	}
	if err == nil {
		err = json.Unmarshal(data, &prefs)
	}
	return prefs, err
}

func (b *Broker) WebModelPreferences(agent AgentName, id string) (ModelPreferences, error) {
	if b == nil || b.store == nil {
		return ModelPreferences{}, nil
	}
	path, err := b.preferencePath(agent)
	if err != nil {
		return ModelPreferences{}, err
	}
	prefs, err := readModelPreferences(path)
	if err != nil || prefs.Model == "" && len(prefs.Efforts) == 0 {
		return prefs, err
	}
	prefs.NewSession = true
	if id == "" {
		return prefs, nil
	}
	state, err := b.store.stateForAgent(id, agent)
	if err != nil {
		return ModelPreferences{}, err
	}
	// A resumed agent owns its model even if an older session never recorded
	// the model ID. Only a new native session may adopt a launch preference.
	if state.ExternalSessionID != "" || state.ClaudeNewID != "" || state.Model != "" || state.Effort != "" {
		prefs.Model, prefs.Custom, prefs.NewSession = "", false, false
	}
	return prefs, nil
}

func (b *Broker) RememberWebModelPick(ctx context.Context, agent AgentName, id string, pick ModelPick) error {
	path, err := b.preferencePath(agent)
	if err != nil {
		return err
	}
	if pick.Model != "" || pick.Effort != nil || pick.Custom {
		if err := validateModel(pick.Model); err != nil {
			return err
		}
	}
	if pick.Effort != nil && *pick.Effort != "" {
		if err := validateModel(*pick.Effort); err != nil {
			return err
		}
	}
	t, _, err := b.catalogTransport(ctx, agent, id)
	if err != nil {
		return err
	}
	if pick.Custom && t.Kind == TransportACP {
		return errors.New("此 ACP 会话只接受目录中的模型")
	}
	if err := privatefile.MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	lock, err := privatefile.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR)
	if err != nil {
		return err
	}
	defer lock.Close()
	for {
		err = session.TryLockFile(lock, true)
		if !errors.Is(err, session.ErrContextBusy) {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err != nil {
		return err
	}
	prefs, err := readModelPreferences(path)
	if err != nil {
		return err
	}
	if pick.Effort == nil || id == "" {
		prefs.Model, prefs.Custom = pick.Model, pick.Custom
	}
	if pick.Effort != nil {
		if *pick.Effort == "" {
			delete(prefs.Efforts, pick.Model)
		} else {
			if prefs.Efforts == nil {
				prefs.Efforts = make(map[string]string)
			}
			prefs.Efforts[pick.Model] = *pick.Effort
		}
	}
	data, err := json.Marshal(prefs)
	if err != nil {
		return err
	}
	return privatefile.WriteFile(path, data)
}
