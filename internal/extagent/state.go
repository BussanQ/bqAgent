package extagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"bqagent/internal/privatefile"
	"bqagent/internal/session"
)

type StateStore struct {
	sessions   *session.Store
	root       string
	groupRoot  string
	legacyRoot string
}

func NewStateStore(workspaceRoot string, options ...session.Options) *StateStore {
	return &StateStore{
		sessions:   session.NewStore(workspaceRoot, options...),
		root:       filepath.Join(workspaceRoot, ".agent", "external-agents", "sessions"),
		groupRoot:  filepath.Join(workspaceRoot, ".agent", "external-agents", "groups"),
		legacyRoot: filepath.Join(workspaceRoot, ".agent", "server", "external-agents", "sessions"),
	}
}

func (store *StateStore) LoadGroup(sessionID string, agent AgentName) (SessionState, error) {
	sessionID, agentName, err := groupStateComponents(sessionID, agent)
	if err != nil {
		return SessionState{}, err
	}
	content, err := os.ReadFile(filepath.Join(store.groupRoot, sessionID, agentName+".json"))
	if os.IsNotExist(err) {
		return SessionState{BQSessionID: sessionID, Agent: agent}, nil
	}
	if err != nil {
		return SessionState{}, err
	}
	var state SessionState
	if err := json.Unmarshal(content, &state); err != nil {
		return SessionState{}, err
	}
	state.BQSessionID = sessionID
	state.Agent = agent
	return state, nil
}

func (store *StateStore) SaveGroup(state SessionState) error {
	sessionID, agentName, err := groupStateComponents(state.BQSessionID, state.Agent)
	if err != nil {
		return err
	}
	state.BQSessionID = sessionID
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	directory := filepath.Join(store.groupRoot, sessionID)
	if err := privatefile.MkdirAll(directory); err != nil {
		return err
	}
	return privatefile.WriteFile(filepath.Join(directory, agentName+".json"), append(content, '\n'))
}

func (store *StateStore) ClearGroup(sessionID string) error {
	sessionID = filepath.Base(sessionID)
	if sessionID == "." || sessionID == "" {
		return fmt.Errorf("session_id is required")
	}
	return os.RemoveAll(filepath.Join(store.groupRoot, sessionID))
}

func groupStateComponents(sessionID string, agent AgentName) (string, string, error) {
	sessionID = filepath.Base(sessionID)
	agentName := filepath.Base(string(agent))
	if sessionID == "." || sessionID == "" {
		return "", "", fmt.Errorf("session_id is required")
	}
	if agentName == "." || agentName == "" {
		return "", "", fmt.Errorf("agent is required")
	}
	return sessionID, agentName, nil
}

// Keep the active route at the top level for compatibility with older session
// files. Each inactive agent owns its own resume ID and transport.
type singleSessionRecord struct {
	SessionState
	Inactive map[AgentName]SessionState `json:"inactive,omitempty"`
}

func (store *StateStore) loadSingle(sessionID string) (singleSessionRecord, error) {
	sessionID = filepath.Base(sessionID)
	if sessionID == "." || sessionID == "" {
		return singleSessionRecord{}, fmt.Errorf("session_id is required")
	}
	path, original, err := store.singlePath(sessionID)
	if err != nil {
		return singleSessionRecord{}, err
	}
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) && original && store.legacyRoot != "" {
		content, err = os.ReadFile(store.legacyPath(sessionID))
	}
	if os.IsNotExist(err) {
		return singleSessionRecord{SessionState: SessionState{BQSessionID: sessionID}}, nil
	}
	if err != nil {
		return singleSessionRecord{}, err
	}
	var state singleSessionRecord
	if err := json.Unmarshal(content, &state); err != nil {
		return singleSessionRecord{}, err
	}
	if state.BQSessionID == "" {
		state.BQSessionID = sessionID
	}
	return state, nil
}

func (store *StateStore) Load(sessionID string) (SessionState, error) {
	record, err := store.loadSingle(sessionID)
	return record.SessionState, err
}

func (store *StateStore) stateForAgent(sessionID string, name AgentName) (SessionState, error) {
	record, err := store.loadSingle(sessionID)
	if err != nil || record.Agent == name {
		return record.SessionState, err
	}
	return record.Inactive[name], nil
}

func (store *StateStore) Save(state SessionState) error {
	state.BQSessionID = filepath.Base(state.BQSessionID)
	if state.BQSessionID == "." || state.BQSessionID == "" {
		return fmt.Errorf("session_id is required")
	}
	record, err := store.loadSingle(state.BQSessionID)
	if err != nil {
		return err
	}
	if record.Agent != "" && record.Agent != state.Agent {
		if record.Inactive == nil {
			record.Inactive = make(map[AgentName]SessionState)
		}
		record.Inactive[record.Agent] = record.SessionState
	}
	delete(record.Inactive, state.Agent)
	record.SessionState = state
	path, _, err := store.singlePath(state.BQSessionID)
	if err != nil {
		return err
	}
	return writeSingleRecord(path, record)
}

func writeSingleRecord(path string, record singleSessionRecord) error {
	content, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	return privatefile.WriteFile(path, content)
}

func (store *StateStore) Clear(sessionID string) error {
	sessionID = filepath.Base(sessionID)
	if sessionID == "." || sessionID == "" {
		return fmt.Errorf("session_id is required")
	}
	path, original, err := store.singlePath(sessionID)
	if err != nil {
		return err
	}
	if !original {
		if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	err = os.Remove(store.path(sessionID))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if store.legacyRoot != "" {
		err = os.Remove(store.legacyPath(sessionID))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (store *StateStore) path(sessionID string) string {
	return filepath.Join(store.root, filepath.Base(sessionID)+".json")
}

func (store *StateStore) legacyPath(sessionID string) string {
	return filepath.Join(store.legacyRoot, filepath.Base(sessionID)+".json")
}

func (store *StateStore) singlePath(id string) (string, bool, error) {
	if store.sessions != nil {
		s, err := store.sessions.Open(id)
		if err != nil && !os.IsNotExist(err) {
			return "", false, err
		}
		if err == nil && (s.ContextNumber() > 0 || s.Meta().ForkedFrom != nil) {
			return filepath.Join(s.ContextDir(), "external-agent.json"), false, nil
		}
	}
	return store.path(id), true, nil
}

// ClearContext publishes provider choices and the staged queue together. Each
// agent's next turn starts a new native session with its saved model and effort.
func (broker *Broker) ClearContext(saved *session.Session, operation string, target AgentName, prepare func(*session.Session) error) error {
	if broker == nil || broker.store == nil {
		if target != "" && target != "bqagent" {
			return fmt.Errorf("外部 Agent 未配置")
		}
		return saved.StartContext(operation, string(target), prepare)
	}
	lock := broker.sessionLock(saved.ID())
	if !lock.TryLock() {
		return session.ErrContextBusy
	}
	defer lock.Unlock()
	record, err := broker.store.loadSingle(saved.ID())
	if err != nil {
		return err
	}
	if record.Inactive == nil {
		record.Inactive = make(map[AgentName]SessionState)
	}
	if record.Agent != "" {
		record.Inactive[record.Agent] = record.SessionState
	}
	active := target
	if active == "" {
		active = record.Agent
	} else if active == "bqagent" {
		active = ""
	}
	for name, state := range record.Inactive {
		state.ExternalSessionID, state.Transport, state.ClaudeNewID = "", "", ""
		state.ForkBinding = false
		record.Inactive[name] = state
	}
	requested := record.Inactive[active]
	requested.BQSessionID, requested.Agent = saved.ID(), active
	delete(record.Inactive, active)
	record.SessionState = requested
	err = saved.StartContext(operation, string(target), func(next *session.Session) error {
		if err := writeSingleRecord(filepath.Join(next.ContextDir(), "external-agent.json"), record); err != nil {
			return err
		}
		return prepare(next)
	})
	if err == nil {
		for _, client := range broker.invalidateSession(saved.ID()) {
			_ = client.Close()
		}
	}
	return err
}
