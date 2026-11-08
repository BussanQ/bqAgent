package extagent

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	errBrokerClosed         = errors.New("external agent broker is closed")
	errBrokerSessionCleared = errors.New("external agent session was cleared")
	ErrPermissionNotFound   = errors.New("ACP permission request was not found or is no longer active")
)

type pendingPermission struct {
	ctx      context.Context
	request  ACPPermissionRequest
	response chan acpPermissionOutcome
	taken    bool
	receipt  *permissionReceipt
}

// All claims, including cancellation during session cleanup, run under the
// broker lock. A submitted native decision still blocks until its wire receipt.
func (p *pendingPermission) resolve(outcome acpPermissionOutcome) {
	if !p.taken {
		p.taken = true
		p.response <- outcome
	}
}

func (p *pendingPermission) cancel() {
	if p.receipt != nil {
		p.receipt.revoked.Store(true)
	}
	p.resolve(acpPermissionOutcome{Outcome: "cancelled"})
}

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
	// rootCtx bounds the lifetime of spawned external-agent subprocesses. It
	// is the broker's lifecycle context, never a single turn's context, so a
	// shared client survives between turns and dies on shutdown or Close.
	rootCtx         context.Context
	detectionReady  chan struct{}
	detectedAt      time.Time
	detectionDone   sync.Once
	detectionCancel context.CancelFunc

	mu                 sync.Mutex
	acpClients         map[acpClientKey]ACPClient
	modelInventories   map[*AgentTransport]modelInventory
	acpInFlight        map[acpClientKey]*acpClientFuture
	sessionGenerations map[string]uint64
	sessionLocks       map[string]*sync.Mutex
	pendingPermissions map[string]*pendingPermission
	closed             bool
}

func NewBroker(store *StateStore, detections map[AgentName]DetectionResult, factory ACPClientFactory) *Broker {
	broker := newBroker(store, factory)
	broker.publishDetections(detections)
	return broker
}

// NewDetectingBroker returns immediately and probes external agents in the
// background. Detection-dependent operations wait for the probe, so callers do
// not observe a transient "unavailable" result merely because startup is still
// in progress.
func NewDetectingBroker(ctx context.Context, store *StateStore, config Config, factory ACPClientFactory, onDetected func(map[AgentName]DetectionResult)) *Broker {
	broker := newBroker(store, factory)
	if ctx == nil {
		ctx = context.Background()
	}
	broker.rootCtx = ctx
	probeCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	broker.detectionCancel = cancel
	go func() {
		results := Detect(probeCtx, config, factory)
		if broker.publishDetections(results) && onDetected != nil {
			onDetected(results)
		}
	}()
	return broker
}

func newBroker(store *StateStore, factory ACPClientFactory) *Broker {
	if factory == nil {
		factory = NewACPClient
	}
	return &Broker{
		store:              store,
		detections:         map[AgentName]DetectionResult{},
		acpFactory:         factory,
		rootCtx:            context.Background(),
		detectionReady:     make(chan struct{}),
		acpClients:         map[acpClientKey]ACPClient{},
		acpInFlight:        map[acpClientKey]*acpClientFuture{},
		sessionGenerations: map[string]uint64{},
		sessionLocks:       map[string]*sync.Mutex{},
		pendingPermissions: map[string]*pendingPermission{},
	}
}

func (broker *Broker) publishDetections(detections map[AgentName]DetectionResult) bool {
	if broker == nil {
		return false
	}
	broker.mu.Lock()
	published := !broker.closed
	if published {
		broker.detections = cloneDetections(detections)
		broker.modelInventories = make(map[*AgentTransport]modelInventory)
		broker.detectedAt = time.Now().UTC()
	}
	broker.mu.Unlock()
	broker.detectionDone.Do(func() { close(broker.detectionReady) })
	return published
}

func cloneDetections(source map[AgentName]DetectionResult) map[AgentName]DetectionResult {
	cloned := make(map[AgentName]DetectionResult, len(source))
	for agent, result := range source {
		cloned[agent] = result
	}
	return cloned
}

func (broker *Broker) waitForDetections(ctx context.Context) error {
	if broker == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-broker.detectionReady:
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if broker.closed {
		return errBrokerClosed
	}
	return nil
}

func (broker *Broker) Detection(agent AgentName) DetectionResult {
	if broker == nil {
		return DetectionResult{Agent: agent}
	}
	<-broker.detectionReady
	broker.mu.Lock()
	defer broker.mu.Unlock()
	result, ok := broker.detections[agent]
	if !ok {
		result.Agent = agent
	}
	return result
}

func (broker *Broker) AvailableAgents() []AgentName {
	if broker == nil {
		return nil
	}
	<-broker.detectionReady
	broker.mu.Lock()
	defer broker.mu.Unlock()
	available := make([]AgentName, 0)
	for _, agent := range SupportedAgents() {
		if broker.detections[agent].Preferred != nil {
			available = append(available, agent)
		}
	}
	return available
}

func (broker *Broker) Resolve(message, sessionID string, selected AgentName, attachments bool) (AgentName, string, bool, error) {
	agent, prompt, explicit, err := ParseRouteWithAttachments(message, attachments)
	if err != nil {
		return "", "", false, err
	}
	if explicit {
		return agent, prompt, true, nil
	}
	if selected != "" {
		return selected, prompt, false, nil
	}
	active, err := broker.ActiveAgent(sessionID)
	return active, prompt, false, err
}

func (broker *Broker) ActiveAgent(sessionID string) (AgentName, error) {
	state, err := broker.ActiveSession(sessionID)
	return state.Agent, err
}

func (broker *Broker) ActiveSession(sessionID string) (SessionState, error) {
	if broker == nil || broker.store == nil || sessionID == "" {
		return SessionState{}, nil
	}
	return broker.store.Load(sessionID)
}

// UseBuiltin deselects the external agent without deleting its resume ID.
// Explicit /default and session deletion still clear all external state.
func (broker *Broker) UseBuiltin(sessionID string) error {
	if broker == nil || broker.store == nil {
		return nil
	}
	lock := broker.sessionLock(sessionID)
	lock.Lock()
	defer lock.Unlock()
	active, err := broker.ActiveAgent(sessionID)
	if err != nil || active == "" {
		return err
	}
	return broker.store.Save(SessionState{BQSessionID: sessionID})
}

func (broker *Broker) Clear(sessionID string) error {
	return broker.clear(sessionID, false)
}

func (broker *Broker) ClearGroup(sessionID string) error {
	return broker.clear(sessionID, true)
}

func (broker *Broker) clear(sessionID string, group bool) error {
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
	var storeErr error
	if group {
		storeErr = broker.store.ClearGroup(sessionID)
	} else {
		storeErr = broker.store.Clear(sessionID)
	}
	if storeErr != nil && firstErr == nil {
		firstErr = storeErr
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
	for requestID, pending := range broker.pendingPermissions {
		if pending.request.BQSessionID != sessionID {
			continue
		}
		delete(broker.pendingPermissions, requestID)
		pending.cancel()
	}
	return clients
}

func (broker *Broker) SendTurn(ctx context.Context, request TurnRequest) (TurnResponse, error) {
	return broker.sendTurn(ctx, request, false)
}

func (broker *Broker) SendGroupTurn(ctx context.Context, request TurnRequest) (TurnResponse, error) {
	return broker.sendTurn(ctx, request, true)
}

func (broker *Broker) sendTurn(ctx context.Context, request TurnRequest, group bool) (TurnResponse, error) {
	if request.Activity != nil {
		ctx = context.WithValue(ctx, activityContextKey{}, request.Activity)
	}
	if broker == nil || broker.store == nil {
		return TurnResponse{}, fmt.Errorf("external agent broker is not configured")
	}
	request.BQSessionID = strings.TrimSpace(request.BQSessionID)
	if request.BQSessionID == "" {
		return TurnResponse{}, fmt.Errorf("session_id is required")
	}
	if err := broker.waitForDetections(ctx); err != nil {
		return TurnResponse{}, err
	}
	// Direct broker callers and the WebUI share the context boundary lock.
	scoped := false
	var contextDir string
	var contextEpoch int
	if !group && broker.store.sessions != nil {
		s, openErr := broker.store.sessions.Open(request.BQSessionID)
		if openErr != nil && !os.IsNotExist(openErr) {
			return TurnResponse{}, openErr
		}
		if openErr == nil {
			gate, err := s.OpenContextLock(false)
			if err != nil {
				return TurnResponse{}, err
			}
			defer gate.Close()
			if err := s.RefreshContexts(); err != nil {
				return TurnResponse{}, err
			}
			scoped = s.ContextNumber() > 0
			contextDir = s.ContextDir()
			contextEpoch = s.ContextNumber()
		}
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
	broker.mu.Lock()
	detection := broker.detections[request.Agent]
	broker.mu.Unlock()
	if detection.Preferred == nil {
		return TurnResponse{}, fmt.Errorf("agent %q is unavailable", request.Agent)
	}

	var state SessionState
	if group {
		state, err = broker.store.LoadGroup(request.BQSessionID, request.Agent)
	} else {
		state, err = broker.store.stateForAgent(request.BQSessionID, request.Agent)
	}
	if err != nil {
		return TurnResponse{}, err
	}
	if state.Agent != "" && state.Agent != request.Agent {
		state = SessionState{BQSessionID: request.BQSessionID}
	}
	state.BQSessionID = request.BQSessionID
	state.Agent = request.Agent
	transport, err := transportForSession(detection, state)
	if err != nil {
		return TurnResponse{}, err
	}
	state.Transport = transport.Kind
	if !supportsInteractionMode(state.Agent, transport.Kind) {
		state.InteractionMode = ""
	}
	if state.Transport != TransportAppServer {
		state.ServiceTier, state.PreferredServiceTier = "", ""
	}
	if request.ExpectedSession != "" && request.ExpectedSession != state.ExternalSessionID {
		return TurnResponse{}, fmt.Errorf("%s 的原生会话已经更换，排队命令未执行", state.Agent)
	}
	if request.Compact != nil && request.Compact.NativeOnly && (group || !compactSupported(transport)) {
		return TurnResponse{}, fmt.Errorf("%s 的当前会话无法执行排队的 /compact", state.Agent)
	}
	if len(request.Images) > 0 && !transport.SupportsImages() {
		return TurnResponse{}, fmt.Errorf("%s 当前使用的 %s 接口不支持图片", request.Agent, transport.Kind)
	}
	previousSettings := state
	if !group && request.ContextUsage != nil {
		var finish func()
		ctx, finish = trackContextUsage(ctx, contextDir, contextEpoch, state, firstNonEmpty(request.Model, state.Model), request.ContextUsage)
		defer finish()
	}
	var response TurnResponse
	if !group && request.Compact != nil && transport.Kind != TransportACP && (state.Agent == AgentPi || state.Agent == AgentCodex || state.Agent == AgentClaude) {
		response, err = broker.compact(ctx, transport, state, request, generation)
	} else {
		// Only the single-chat transports above own this host command. Other
		// adapters receive their advertised slash command as an ordinary prompt.
		request.Compact = nil
		options, optionErr := broker.optionsForTurn(ctx, request, state, transport.Kind)
		if optionErr != nil {
			return TurnResponse{}, optionErr
		}
		state.Model, state.Effort = options.Model, options.Effort

		switch transport.Kind {
		case TransportACP, TransportAppServer:
			response, err = broker.sendACPAttempt(ctx, transport.Command, state, request, options, generation, false, true)
		case TransportCLI:
			ask := func(dialogCtx context.Context, params acpPermissionParams) acpPermissionOutcome {
				return broker.awaitPermission(dialogCtx, state.BQSessionID, state.Agent, params, request.PermissionSink, generation)
			}
			if group && state.Agent == AgentClaude {
				ask = nil
			}
			spec := transport.Command
			if scoped {
				spec.Args = scopedCLIArgs(state.Agent, spec.Args)
			}
			response, err = broker.cli.sendTurn(ctx, spec, state, request, ask)
		default:
			err = fmt.Errorf("unsupported transport %q", detection.Preferred.Kind)
		}
	}
	if err != nil {
		if response.Command == "" && response.State.ExternalSessionID != "" && broker.generationCurrent(request.BQSessionID, generation) {
			// Keep a newly created thread resumable even when its first turn fails.
			if !response.settingsAcknowledged {
				response.State.Model = previousSettings.Model
				response.State.Effort = previousSettings.Effort
				response.State.ServiceTier = previousSettings.ServiceTier
				response.State.PreferredServiceTier = previousSettings.PreferredServiceTier
			}
			var saveErr error
			if group {
				saveErr = broker.store.SaveGroup(response.State)
			} else {
				saveErr = broker.store.Save(response.State)
			}
			if saveErr != nil {
				return TurnResponse{}, errors.Join(err, saveErr)
			}
		}
		return TurnResponse{}, err
	}
	if !broker.generationCurrent(request.BQSessionID, generation) {
		return TurnResponse{}, errBrokerSessionCleared
	}
	var saveErr error
	if group {
		saveErr = broker.store.SaveGroup(response.State)
	} else {
		saveErr = broker.store.Save(response.State)
	}
	if saveErr != nil {
		return TurnResponse{}, saveErr
	}
	return response, nil
}

func (broker *Broker) sendACPAttempt(ctx context.Context, spec CommandSpec, state SessionState, req TurnRequest, opts PromptOptions, generation uint64, forceNewSession, allowReconnect bool) (TurnResponse, error) {
	cwd, prompt, permissionSink := req.CWD, req.Prompt, req.PermissionSink
	client, err := broker.sessionClient(ctx, state.BQSessionID, state.Agent, spec, cwd, generation, state.Transport)
	if err != nil {
		return TurnResponse{}, err
	}
	sessionID := state.ExternalSessionID
	if state.ForkBinding && (sessionID == "" || !client.LoadSessionSupported()) {
		return TurnResponse{}, errors.New("无法恢复分支的原生会话")
	}
	switch {
	case sessionID == "" || (forceNewSession && !client.LoadSessionSupported()):
		sessionID, err = client.NewSession(ctx, cwd)
	case client.LoadSessionSupported():
		sessionID, err = client.LoadSession(ctx, sessionID, cwd)
	default:
		// Keep using the active in-memory process for the session if load isn't supported.
	}
	if err != nil {
		if allowReconnect && ctx.Err() == nil && broker.generationCurrent(state.BQSessionID, generation) && isACPConnectionError(err) {
			broker.discardACPClient(state.BQSessionID, state.Agent, client)
			return broker.sendACPAttempt(ctx, spec, state, req, opts, generation, true, false)
		}
		return TurnResponse{}, err
	}
	if !broker.generationCurrent(state.BQSessionID, generation) {
		return TurnResponse{}, errBrokerSessionCleared
	}
	if state.ForkBinding && state.ExternalSessionID != sessionID {
		return TurnResponse{}, errors.New("Agent 恢复了其他会话，已取消发送")
	}
	if sink := contextUsageSink(ctx); sink != nil && state.ExternalSessionID != sessionID {
		sink(sessionID, ContextUsage{})
	}
	state.ExternalSessionID = sessionID
	if aware, ok := client.(interface {
		setContextPermissionHandler(func(context.Context, acpPermissionParams) acpPermissionOutcome)
	}); ok {
		aware.setContextPermissionHandler(func(permissionCtx context.Context, params acpPermissionParams) acpPermissionOutcome {
			return broker.awaitPermission(permissionCtx, state.BQSessionID, state.Agent, params, permissionSink, generation)
		})
		defer aware.setContextPermissionHandler(nil)
	} else if aware, ok := client.(acpPermissionAware); ok {
		aware.setPermissionHandler(func(params acpPermissionParams) acpPermissionOutcome {
			return broker.awaitPermission(ctx, state.BQSessionID, state.Agent, params, permissionSink, generation)
		})
		defer aware.setPermissionHandler(nil)
	}
	var reply string
	var began atomic.Bool
	promptCtx := ctx
	if state.Transport == TransportACP && allowReconnect {
		sink := ActivitySinkFromContext(ctx)
		promptCtx = context.WithValue(ctx, activityContextKey{}, ActivitySink(func(event Activity) {
			if event.ContextUsage == nil {
				began.Store(true)
			}
			if sink != nil {
				sink(event)
			}
		}))
	}
	configurable, supportsOptions := client.(interface {
		PromptWithOptions(context.Context, string, string, PromptOptions) (string, error)
	})
	var steering *TurnSteering
	if state.Transport == TransportAppServer && supportsOptions {
		steering = req.Steering
	}
	opts.Images, opts.Steering = req.Images, steering
	if observer, ok := permissionSink.(interface{ QuestionsSettled() }); ok && state.Transport == TransportAppServer && supportsOptions {
		opts.QuestionsSettled = observer.QuestionsSettled
	}
	hasMode := opts.InteractionMode != "" || opts.savedInteractionMode != ""
	if state.Model == "" && state.Effort == "" && opts.ServiceTier == "" && !hasMode && len(req.Images) == 0 && steering == nil && opts.QuestionsSettled == nil {
		reply, err = client.Prompt(promptCtx, sessionID, prompt)
	} else if supportsOptions {
		reply, err = configurable.PromptWithOptions(promptCtx, sessionID, prompt, opts)
	} else if modelClient, ok := client.(interface {
		PromptWithModel(context.Context, string, string, string) (string, error)
	}); ok && state.Effort == "" && opts.ServiceTier == "" && !hasMode && len(req.Images) == 0 {
		reply, err = modelClient.PromptWithModel(promptCtx, sessionID, prompt, state.Model)
	} else {
		return TurnResponse{}, fmt.Errorf("%s 适配器不支持当前请求的模型、强度或图片输入", state.Agent)
	}
	if err != nil {
		if state.Transport == TransportAppServer {
			// A started turn may already have changed files. Never replay it on a
			// new connection or fall back to CLI after a prompt failure.
			response := TurnResponse{State: state}
			if native, ok := client.(*codexAppServerClient); ok {
				// Resume readback is authoritative even if turn/start is refused.
				// Apply user overrides only after turn/start returns a turn ID.
				native.mu.Lock()
				applied, started := native.threadOptions, native.threadTurnStarted
				native.mu.Unlock()
				if applied.ExternalSessionID == sessionID && applied.InteractionMode != "" {
					response.State.InteractionMode = applied.InteractionMode
				}
				if started && applied.ExternalSessionID == sessionID && (hasMode || applied.PreferredServiceTier != "") {
					response.settingsAcknowledged = true
					if applied.PreferredServiceTier != "" {
						response.State.Model, response.State.Effort = applied.Model, applied.Effort
					}
					response.State.ServiceTier, response.State.PreferredServiceTier = applied.ServiceTier, applied.PreferredServiceTier
				}
			}
			broker.discardACPClient(state.BQSessionID, state.Agent, client)
			return response, err
		}
		if allowReconnect && ctx.Err() == nil && broker.generationCurrent(state.BQSessionID, generation) && isACPConnectionError(err) {
			broker.discardACPClient(state.BQSessionID, state.Agent, client)
			if began.Load() {
				// The agent may already have executed the tool. Reconnecting must
				// not implicitly run the user's prompt a second time.
				if client.LoadSessionSupported() {
					return TurnResponse{State: state}, err
				}
				return TurnResponse{}, err
			}
			return broker.sendACPAttempt(ctx, spec, state, req, opts, generation, true, false)
		}
		return TurnResponse{}, err
	}
	if !broker.generationCurrent(state.BQSessionID, generation) {
		return TurnResponse{}, errBrokerSessionCleared
	}
	state.ExternalSessionID = sessionID
	if client, ok := client.(*codexAppServerClient); ok {
		applied := client.threadSettings(sessionID)
		state.ServiceTier, state.PreferredServiceTier = applied.ServiceTier, applied.PreferredServiceTier
		state.InteractionMode = applied.InteractionMode
	}
	fork, ok := client.(interface{ SupportsConversationFork() bool })
	return TurnResponse{Reply: reply, State: state, ForkTip: state.Agent == AgentOpenCode && state.Transport == TransportACP && ok && fork.SupportsConversationFork()}, nil
}

func (broker *Broker) discardACPClient(sessionID string, agent AgentName, expected ACPClient) {
	key := acpClientKey{sessionID: sessionID, agent: agent}
	broker.mu.Lock()
	client := broker.acpClients[key]
	if client == expected {
		delete(broker.acpClients, key)
	} else {
		client = nil
	}
	for requestID, pending := range broker.pendingPermissions {
		if pending.request.BQSessionID == sessionID && pending.request.Agent == agent {
			delete(broker.pendingPermissions, requestID)
			pending.cancel()
		}
	}
	broker.mu.Unlock()
	if client != nil {
		_ = client.Close()
	}
}

func isACPConnectionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, os.ErrClosed) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "broken pipe") || strings.Contains(message, "closed pipe") || strings.Contains(message, "acp process exited")
}

func (broker *Broker) awaitPermission(ctx context.Context, bqSessionID string, agent AgentName, params acpPermissionParams, sink ACPPermissionSink, generation uint64) acpPermissionOutcome {
	if sink == nil || len(params.Options) == 0 {
		return acpPermissionOutcome{Outcome: "cancelled"}
	}
	broker.mu.Lock()
	if ctx.Err() != nil || broker.closed || broker.sessionGenerations[bqSessionID] != generation {
		broker.mu.Unlock()
		return acpPermissionOutcome{Outcome: "cancelled"}
	}
	requestID := "acp-permission-" + rand.Text()
	request := ACPPermissionRequest{
		RequestID: requestID, BQSessionID: bqSessionID, Agent: agent,
		ExternalSessionID: params.SessionID, ToolCall: append(json.RawMessage(nil), params.ToolCall...),
		Options: append([]ACPPermissionOption(nil), params.Options...), Dialog: params.Dialog,
	}
	pending := &pendingPermission{ctx: ctx, request: request, response: make(chan acpPermissionOutcome, 1), receipt: params.receipt}
	broker.pendingPermissions[requestID] = pending
	broker.mu.Unlock()

	sink.EmitACPPermissionRequest(request)
	if params.published != nil {
		params.published()
	}
	remove := func() bool {
		broker.mu.Lock()
		defer broker.mu.Unlock()
		if broker.pendingPermissions[requestID] != pending {
			return false
		}
		delete(broker.pendingPermissions, requestID)
		return true
	}
	select {
	case <-ctx.Done():
		remove()
		request.Expired = true
		emitPermissionResolution(sink, request)
		return acpPermissionOutcome{Outcome: "cancelled"}
	case outcome := <-pending.response:
		request.Selected, request.Expired = outcome.OptionID, outcome.Outcome != "selected"
		if request.Dialog != nil && request.Dialog.Method == "question" {
			if outcome.OptionID == "cancel" {
				request.Selected, request.Expired = "", true
			} else if request.Dialog.Secret && !request.Expired {
				// Only the originating RPC receives the user's answer.
				request.Selected = "answered"
			}
		}
		if params.receipt == nil {
			remove()
			emitPermissionResolution(sink, request)
		} else {
			params.receipt.publish = func(delivered bool) {
				if !remove() || !delivered {
					request.Selected, request.Expired = "", true
				} else if request.Selected == "submit" && len(outcome.Answers) != 0 {
					request.Dialog = answeredDialog(request.Dialog, outcome.Answers)
				}
				emitPermissionResolution(sink, request)
			}
		}
		return outcome
	}
}

// Interactive clients subscribe to the outcome as well as the original
// prompt so a reconnect never resurrects a resolved permission request.
func emitPermissionResolution(sink ACPPermissionSink, request ACPPermissionRequest) {
	if observer, ok := sink.(interface{ EmitACPPermissionResolution(ACPPermissionRequest) }); ok {
		observer.EmitACPPermissionResolution(request)
	} else if request.Dialog != nil && request.Expired {
		sink.EmitACPPermissionRequest(request)
	}
}

func (broker *Broker) RespondPermission(requestID, optionID string) error {
	return broker.RespondPrompt(requestID, optionID, nil)
}

func (broker *Broker) RespondPrompt(requestID, optionID string, value *string) error {
	return broker.RespondPromptWithAnswers(requestID, optionID, value, nil)
}

func (broker *Broker) RespondPromptWithAnswers(requestID, optionID string, value *string, answers []QuestionAnswer) error {
	if broker == nil {
		return ErrPermissionNotFound
	}
	requestID = strings.TrimSpace(requestID)
	optionID = strings.TrimSpace(optionID)
	broker.mu.Lock()
	defer broker.mu.Unlock()
	pending := broker.pendingPermissions[requestID]
	if pending == nil || pending.taken || pending.ctx.Err() != nil {
		return ErrPermissionNotFound
	}
	valid := false
	for _, option := range pending.request.Options {
		if option.OptionID == optionID {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("permission option %q was not offered", optionID)
	}
	dialog := pending.request.Dialog
	if dialog != nil && len(dialog.Questions) != 0 && optionID == "submit" {
		var err error
		answers, err = orderedQuestionAnswers(dialog.Questions, answers)
		if err != nil {
			return err
		}
	} else if answers != nil {
		return fmt.Errorf("此选项不接受分组回答")
	}
	textAnswer := dialog != nil && (dialog.Method == "input" || dialog.Method == "editor" || dialog.Method == "question" && dialog.FreeText) && optionID == "submit"
	if textAnswer != (value != nil) {
		return fmt.Errorf("invalid text answer for option %q", optionID)
	}
	if dialog != nil && dialog.Method == "question" && value != nil && (strings.TrimSpace(*value) == "" || len(*value) > 64<<10) {
		return fmt.Errorf("请填写有效的回答")
	}
	if pending.receipt == nil {
		delete(broker.pendingPermissions, requestID)
	}
	pending.resolve(acpPermissionOutcome{Outcome: "selected", OptionID: optionID, Value: value, Answers: answers})
	return nil
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
	return broker.sessionClient(ctx, sessionID, agent, spec, cwd, generation, TransportACP)
}

func (broker *Broker) sessionClient(ctx context.Context, sessionID string, agent AgentName, spec CommandSpec, cwd string, generation uint64, transport TransportKind) (SessionClient, error) {
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

	var client ACPClient
	var err error
	if transport == TransportAppServer {
		client, err = NewCodexAppServerClient(broker.rootCtx, spec, cwd)
	} else {
		client, err = broker.acpFactory(broker.rootCtx, spec, cwd)
	}
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
	detectionCancel := broker.detectionCancel
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
	for requestID, pending := range broker.pendingPermissions {
		delete(broker.pendingPermissions, requestID)
		pending.cancel()
	}
	broker.mu.Unlock()
	if detectionCancel != nil {
		detectionCancel()
	}
	broker.detectionDone.Do(func() { close(broker.detectionReady) })

	var firstErr error
	for _, client := range clients {
		if err := client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// HasPendingPrompt reports a live prompt that blocks native input in this chat.
func (broker *Broker) HasPendingPrompt(id string) bool {
	if broker == nil {
		return false
	}
	broker.mu.Lock()
	for _, p := range broker.pendingPermissions {
		if p.request.BQSessionID == id && p.ctx.Err() == nil && (p.request.Dialog == nil || !p.request.Dialog.NonBlocking) {
			broker.mu.Unlock()
			return true
		}
	}
	client, _ := broker.acpClients[acpClientKey{sessionID: id, agent: AgentCodex}].(*codexAppServerClient)
	broker.mu.Unlock()
	return client != nil && client.blockingPrompt()
}
