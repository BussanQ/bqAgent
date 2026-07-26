package extagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	errBrokerClosed         = errors.New("external agent broker is closed")
	errBrokerSessionCleared = errors.New("external agent session was cleared")
)

type acpClientKey struct {
	sessionID string
	agent     AgentName
}

// acpClientFuture represents exactly one in-progress factory/initialize cycle
// for a client key. Waiters share it but may independently abandon the wait via
// their context.
type acpClientFuture struct {
	done          chan struct{}
	generation    uint64
	client        ACPClient // set only while initialization is in progress
	clientClaimed bool      // Clear/Close or the initializer owns its final Close
	err           error
	abandoned     bool
}

type Broker struct {
	store      *StateStore
	detections map[AgentName]DetectionResult
	acpFactory ACPClientFactory
	cli        CLIAdapter

	mu                 sync.Mutex
	acpClients         map[acpClientKey]ACPClient
	acpInFlight        map[acpClientKey]*acpClientFuture
	sessionGenerations map[string]uint64
	sessionLocks       map[string]*sync.Mutex
	closed             bool
}

func NewBroker(store *StateStore, detections map[AgentName]DetectionResult, factory ACPClientFactory) *Broker {
	if factory == nil {
		factory = NewACPClient
	}
	return &Broker{
		store:              store,
		detections:         detections,
		acpFactory:         factory,
		acpClients:         map[acpClientKey]ACPClient{},
		acpInFlight:        map[acpClientKey]*acpClientFuture{},
		sessionGenerations: map[string]uint64{},
		sessionLocks:       map[string]*sync.Mutex{},
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

	// Serialize invalidation and deletion with the complete turn transaction so
	// a SendTurn that started earlier cannot save its state after Clear returns.
	sessionLock := broker.sessionLock(sessionID)
	sessionLock.Lock()
	defer sessionLock.Unlock()

	// Invalidate cached and initializing clients while holding the map lock, but
	// never wait for a process there. An initializer that finishes after Clear
	// observes its abandoned generation and closes its own late client instead of
	// putting it back in the cache.
	clients := broker.invalidateSession(sessionID)
	var firstErr error
	for _, client := range clients {
		if err := client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if err := broker.store.Clear(sessionID); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// sessionLock serializes stateful work for one BQ session while allowing
// unrelated sessions to proceed independently. ACP process initialization still
// releases broker.mu in acpClient.
func (broker *Broker) sessionLock(sessionID string) *sync.Mutex {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	lock := broker.sessionLocks[sessionID]
	if lock == nil {
		lock = &sync.Mutex{}
		broker.sessionLocks[sessionID] = lock
	}
	return lock
}

func (broker *Broker) invalidateSession(sessionID string) []ACPClient {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	broker.sessionGenerations[sessionID]++
	clients := make([]ACPClient, 0)
	for key, client := range broker.acpClients {
		if key.sessionID != sessionID {
			continue
		}
		clients = append(clients, client)
		delete(broker.acpClients, key)
	}
	for key, future := range broker.acpInFlight {
		if key.sessionID != sessionID {
			continue
		}
		future.abandoned = true
		if future.client != nil {
			clients = append(clients, future.client)
			future.client = nil
			future.clientClaimed = true
		}
		delete(broker.acpInFlight, key)
	}
	return clients
}

func (broker *Broker) SendTurn(ctx context.Context, request TurnRequest) (TurnResponse, error) {
	if broker == nil || broker.store == nil {
		return TurnResponse{}, fmt.Errorf("external agent broker is not configured")
	}
	request.BQSessionID = strings.TrimSpace(request.BQSessionID)
	if request.BQSessionID == "" {
		return TurnResponse{}, fmt.Errorf("session_id is required")
	}
	// Load, external work, generation validation, and Save comprise one session
	// transaction. This prevents concurrent turns from losing a newly issued
	// external session ID and makes Clear a durable fencing operation.
	sessionLock := broker.sessionLock(request.BQSessionID)
	sessionLock.Lock()
	defer sessionLock.Unlock()

	generation, err := broker.sessionGeneration(request.BQSessionID)
	if err != nil {
		return TurnResponse{}, err
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
		response, err = broker.sendACP(ctx, detection.Preferred.Command, state, request.CWD, request.Prompt, generation)
	case TransportCLI:
		response, err = broker.cli.SendTurn(ctx, detection.Preferred.Command, state, request.CWD, request.Prompt)
	default:
		err = fmt.Errorf("unsupported transport %q", detection.Preferred.Kind)
	}
	if err != nil {
		return TurnResponse{}, err
	}
	if !broker.generationCurrent(request.BQSessionID, generation) {
		return TurnResponse{}, errBrokerSessionCleared
	}
	if saveErr := broker.store.Save(response.State); saveErr != nil {
		return TurnResponse{}, saveErr
	}
	return response, nil
}

func (broker *Broker) sendACP(ctx context.Context, spec CommandSpec, state SessionState, cwd, prompt string, generation uint64) (TurnResponse, error) {
	client, err := broker.acpClient(ctx, state.BQSessionID, state.Agent, spec, cwd, generation)
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
	if !broker.generationCurrent(state.BQSessionID, generation) {
		return TurnResponse{}, errBrokerSessionCleared
	}
	reply, err := client.Prompt(ctx, sessionID, prompt)
	if err != nil {
		return TurnResponse{}, err
	}
	if !broker.generationCurrent(state.BQSessionID, generation) {
		return TurnResponse{}, errBrokerSessionCleared
	}
	state.ExternalSessionID = sessionID
	return TurnResponse{Reply: reply, State: state}, nil
}

func (broker *Broker) sessionGeneration(sessionID string) (uint64, error) {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if broker.closed {
		return 0, errBrokerClosed
	}
	return broker.sessionGenerations[sessionID], nil
}

func (broker *Broker) generationCurrent(sessionID string, generation uint64) bool {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	return !broker.closed && broker.sessionGenerations[sessionID] == generation
}

// acpClient coalesces startup by key. Both ACPClientFactory and Initialize can
// spawn processes or block, so they deliberately run outside broker.mu.
func (broker *Broker) acpClient(ctx context.Context, sessionID string, agent AgentName, spec CommandSpec, cwd string, generation uint64) (ACPClient, error) {
	key := acpClientKey{sessionID: sessionID, agent: agent}

	broker.mu.Lock()
	if broker.closed {
		broker.mu.Unlock()
		return nil, errBrokerClosed
	}
	if broker.sessionGenerations[sessionID] != generation {
		broker.mu.Unlock()
		return nil, errBrokerSessionCleared
	}
	if client := broker.acpClients[key]; client != nil {
		broker.mu.Unlock()
		return client, nil
	}
	if future := broker.acpInFlight[key]; future != nil {
		broker.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-future.done:
			if future.err != nil {
				return nil, future.err
			}
			return future.client, nil
		}
	}
	future := &acpClientFuture{done: make(chan struct{}), generation: generation}
	broker.acpInFlight[key] = future
	broker.mu.Unlock()

	client, err := broker.acpFactory(spec, cwd)
	if err == nil {
		broker.mu.Lock()
		if broker.acpInFlight[key] == future && !future.abandoned {
			future.client = client
		}
		broker.mu.Unlock()
		initCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err = client.Initialize(initCtx)
		cancel()
	}

	closeClient := broker.finishACPClient(key, future, client, err)
	if closeClient != nil {
		_ = closeClient.Close()
	}
	if future.err != nil {
		return nil, future.err
	}
	return client, nil
}

// finishACPClient publishes a successfully initialized client only when the
// same generation is still active. It returns a late client to close outside
// the broker lock.
func (broker *Broker) finishACPClient(key acpClientKey, future *acpClientFuture, client ACPClient, initErr error) ACPClient {
	broker.mu.Lock()
	defer broker.mu.Unlock()

	active := !broker.closed && !future.abandoned && broker.sessionGenerations[key.sessionID] == future.generation && broker.acpInFlight[key] == future
	if active && initErr == nil {
		broker.acpClients[key] = client
		future.client = client
	} else if initErr == nil {
		if broker.closed {
			initErr = errBrokerClosed
		} else {
			initErr = errBrokerSessionCleared
		}
	}
	if broker.acpInFlight[key] == future {
		delete(broker.acpInFlight, key)
	}
	future.err = initErr
	close(future.done)
	if client != nil && (initErr != nil || !active) && !future.clientClaimed {
		future.client = nil
		future.clientClaimed = true
		return client
	}
	return nil
}

func (broker *Broker) Close() error {
	if broker == nil {
		return nil
	}

	// Detach all clients first. Closing processes may wait for them to exit and
	// must not hold the map lock while a future or waiter needs to make progress.
	broker.mu.Lock()
	broker.closed = true
	clients := make([]ACPClient, 0, len(broker.acpClients)+len(broker.acpInFlight))
	for key, client := range broker.acpClients {
		clients = append(clients, client)
		delete(broker.acpClients, key)
	}
	for key, future := range broker.acpInFlight {
		future.abandoned = true
		if future.client != nil {
			clients = append(clients, future.client)
			future.client = nil
			future.clientClaimed = true
		}
		delete(broker.acpInFlight, key)
	}
	broker.mu.Unlock()

	var firstErr error
	for _, client := range clients {
		if err := client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
