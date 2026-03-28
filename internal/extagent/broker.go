package extagent

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

type Broker struct {
	store      *StateStore
	detections map[AgentName]DetectionResult
	acpFactory ACPClientFactory
	cli        CLIAdapter

	mu         sync.Mutex
	acpClients map[string]ACPClient
}

func NewBroker(store *StateStore, detections map[AgentName]DetectionResult, factory ACPClientFactory) *Broker {
	if factory == nil {
		factory = NewACPClient
	}
	return &Broker{
		store:      store,
		detections: detections,
		acpFactory: factory,
		acpClients: map[string]ACPClient{},
	}
}

func (broker *Broker) Detection(agent AgentName) DetectionResult {
	if broker == nil {
		return DetectionResult{Agent: agent}
	}
	return broker.detections[agent]
}

func (broker *Broker) Resolve(message, sessionID string) (AgentName, string, bool, error) {
	agent, prompt, explicit, err := ParseRoute(message)
	if err != nil {
		return "", "", false, err
	}
	if explicit {
		return agent, prompt, true, nil
	}
	if broker == nil || strings.TrimSpace(sessionID) == "" || broker.store == nil {
		return "", strings.TrimSpace(message), false, nil
	}
	state, err := broker.store.Load(sessionID)
	if err != nil || state.Agent == "" {
		return "", strings.TrimSpace(message), false, nil
	}
	return state.Agent, strings.TrimSpace(message), false, nil
}

func (broker *Broker) Clear(sessionID string) error {
	if broker == nil || broker.store == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("session_id is required")
	}

	broker.mu.Lock()
	if client := broker.acpClients[sessionID]; client != nil {
		_ = client.Close()
		delete(broker.acpClients, sessionID)
	}
	broker.mu.Unlock()

	return broker.store.Clear(sessionID)
}

func (broker *Broker) SendTurn(ctx context.Context, request TurnRequest) (TurnResponse, error) {
	if broker == nil || broker.store == nil {
		return TurnResponse{}, fmt.Errorf("external agent broker is not configured")
	}
	detection := broker.detections[request.Agent]
	if detection.Preferred == nil {
		return TurnResponse{}, fmt.Errorf("agent %q is unavailable", request.Agent)
	}

	state, err := broker.store.Load(request.BQSessionID)
	if err != nil {
		return TurnResponse{}, err
	}
	if state.Agent != "" && state.Agent != request.Agent {
		state = SessionState{BQSessionID: request.BQSessionID}
	}
	state.BQSessionID = request.BQSessionID
	state.Agent = request.Agent
	state.Transport = detection.Preferred.Kind

	var response TurnResponse
	switch detection.Preferred.Kind {
	case TransportACP:
		response, err = broker.sendACP(ctx, detection.Preferred.Command, state, request.CWD, request.Prompt)
	case TransportCLI:
		response, err = broker.cli.SendTurn(ctx, detection.Preferred.Command, state, request.CWD, request.Prompt)
	default:
		err = fmt.Errorf("unsupported transport %q", detection.Preferred.Kind)
	}
	if err != nil {
		return TurnResponse{}, err
	}
	if saveErr := broker.store.Save(response.State); saveErr != nil {
		return TurnResponse{}, saveErr
	}
	return response, nil
}

func (broker *Broker) sendACP(ctx context.Context, spec CommandSpec, state SessionState, cwd, prompt string) (TurnResponse, error) {
	client, err := broker.acpClient(state.BQSessionID, spec, cwd)
	if err != nil {
		return TurnResponse{}, err
	}
	sessionID := state.ExternalSessionID
	switch {
	case sessionID == "":
		sessionID, err = client.NewSession(ctx, cwd)
	case client.LoadSessionSupported():
		sessionID, err = client.LoadSession(ctx, sessionID, cwd)
	default:
		// Keep using the active in-memory process for the session if load isn't supported.
	}
	if err != nil {
		return TurnResponse{}, err
	}
	reply, err := client.Prompt(ctx, sessionID, prompt)
	if err != nil {
		return TurnResponse{}, err
	}
	state.ExternalSessionID = sessionID
	return TurnResponse{Reply: reply, State: state}, nil
}

func (broker *Broker) acpClient(sessionID string, spec CommandSpec, cwd string) (ACPClient, error) {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if client := broker.acpClients[sessionID]; client != nil {
		return client, nil
	}
	client, err := broker.acpFactory(spec, cwd)
	if err != nil {
		return nil, err
	}
	if err := client.Initialize(context.Background()); err != nil {
		_ = client.Close()
		return nil, err
	}
	broker.acpClients[sessionID] = client
	return client, nil
}

func (broker *Broker) Close() error {
	if broker == nil {
		return nil
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	var firstErr error
	for sessionID, client := range broker.acpClients {
		if err := client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(broker.acpClients, sessionID)
	}
	return firstErr
}
