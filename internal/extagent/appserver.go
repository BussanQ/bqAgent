package extagent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bqagent/internal/turn"
)

// codexAppServerClient implements Codex's own JSON-RPC protocol, while exposing
// the same session lifecycle and approval bridge as the ACP adapter.
type codexAppServerClient struct {
	cmd                 *exec.Cmd
	stdin               io.WriteCloser
	mu                  sync.Mutex
	writeMu             sync.Mutex
	nextID              atomic.Int64
	responses           map[string]chan rpcEnvelope
	done                chan struct{}
	waitDone            chan struct{}
	stopOnce            sync.Once
	closeOnce           sync.Once
	readErr             error
	permissionHandler   func(context.Context, acpPermissionParams) acpPermissionOutcome
	prompts             map[string]*pendingCodexPrompt
	compaction          *codexCompactRun
	threadOptions       SessionState
	threadCollaboration *codexCollaborationMode
	threadTurnStarted   bool
	threadHistoryMode   string
	active              *appServerRun
	lastUsage           codexUsageTarget
	usageClock          int64
}

type codexUsageTarget struct {
	thread, turn, model string
	report              usageSink
}

func (c *codexAppServerClient) stampUsage(u *ContextUsage) *ContextUsage {
	if u != nil {
		c.usageClock = max(c.usageClock+1, time.Now().UnixMicro())
		u.Captured = c.usageClock
	}
	return u
}

type appServerItem struct {
	Summary                                         json.RawMessage `json:"summary"`
	ID                                              string          `json:"id"`
	Type                                            string          `json:"type"`
	Text                                            string          `json:"text"`
	Phase                                           string          `json:"phase"`
	Status, Command, Cwd, Server, Tool, Query, Path string
	AggregatedOutput                                string
	Arguments, Changes, Result, Error, ContentItems json.RawMessage
	ExitCode                                        *int
	Success                                         *bool
}

type appServerTurn struct {
	ID     string          `json:"id"`
	Status string          `json:"status"`
	Items  []appServerItem `json:"items"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type appServerProgress struct {
	compactionSeen bool
	steps          *PlanUpdate
	proposalDeltas map[string]*strings.Builder
	activity       []Activity
	items          map[string]appServerItem
	order          []string
	completed      *appServerTurn
}

type appServerRun struct {
	questionsSettled func()
	identified       chan struct{}
	promptWorkers    sync.WaitGroup
	drained          bool
	threadID         string
	turnID           string
	ctx              context.Context
	cancel           context.CancelFunc
	turns            map[string]*appServerProgress
	changed          chan struct{}
}

func NewCodexAppServerClient(ctx context.Context, spec CommandSpec, cwd string) (SessionClient, error) {
	if strings.TrimSpace(spec.Command) == "" {
		return nil, fmt.Errorf("codex app-server command is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, spec.Command, spec.Args...)
	cmd.Dir = cwd
	configureExternalProcess(cmd)
	// Stderr is diagnostic, never protocol data. Avoid unbounded buffering.
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = stdout.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	c := &codexAppServerClient{cmd: cmd, stdin: stdin, responses: map[string]chan rpcEnvelope{}, done: make(chan struct{}), waitDone: make(chan struct{})}
	go func() {
		c.readLoop(stdout)
		_ = cmd.Wait()
		close(c.waitDone)
	}()
	return c, nil
}

func (c *codexAppServerClient) Initialize(ctx context.Context) error {
	_, err := c.request(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "bqagent", "title": "bqAgent", "version": "1.0.0"}, "capabilities": map[string]bool{"experimentalApi": true}})
	if err != nil {
		return err
	}
	return c.write(map[string]any{"method": "initialized", "params": map[string]any{}})
}

func (c *codexAppServerClient) LoadSessionSupported() bool { return true }

func (c *codexAppServerClient) NewSession(ctx context.Context, cwd string) (string, error) {
	return c.openThread(ctx, "thread/start", map[string]any{"cwd": cwd, "historyMode": "paginated"}, false)
}

func (c *codexAppServerClient) LoadSession(ctx context.Context, id, cwd string) (string, error) {
	return c.openThread(ctx, "thread/resume", map[string]any{"threadId": id, "cwd": cwd}, false)
}

func (c *codexAppServerClient) openThread(ctx context.Context, method string, params map[string]any, requireIdle bool) (string, error) {
	response, err := c.request(ctx, method, params)
	if method == "thread/start" && response.Error != nil && response.Error.Code == -32602 && params["historyMode"] != nil {
		delete(params, "historyMode")
		response, err = c.request(ctx, method, params)
	}
	if err != nil {
		return "", err
	}
	var result struct {
		CollaborationMode      *codexCollaborationMode
		Model, ReasoningEffort string
		ServiceTier            json.RawMessage
		Thread                 struct {
			HistoryMode string
			ID          string `json:"id"`
			Status      json.RawMessage
		} `json:"thread"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		return "", err
	}
	if strings.TrimSpace(result.Thread.ID) == "" {
		return "", fmt.Errorf("codex %s returned an empty thread ID", method)
	}
	if requireIdle {
		var status struct{ Type string }
		if json.Unmarshal(result.Thread.Status, &status) != nil {
			_ = json.Unmarshal(result.Thread.Status, &status.Type)
		}
		if status.Type == "active" {
			return "", fmt.Errorf("Codex 线程仍有任务在运行")
		}
	}
	c.mu.Lock()
	c.threadOptions = SessionState{Agent: AgentCodex, Model: result.Model, Effort: result.ReasoningEffort, ServiceTier: openedServiceTier(result.ServiceTier), ExternalSessionID: result.Thread.ID}
	c.threadCollaboration = result.CollaborationMode
	if m := result.CollaborationMode; m != nil && (m.Mode == "plan" || m.Mode == "default") {
		c.threadOptions.InteractionMode = m.Mode
		c.threadOptions.Model = firstNonEmpty(result.Model, m.Settings.Model)
		if c.threadOptions.Effort == "" && m.Settings.Effort != nil && c.threadOptions.Model == m.Settings.Model {
			c.threadOptions.Effort = *m.Settings.Effort
		}
	}
	c.threadHistoryMode = result.Thread.HistoryMode
	c.threadTurnStarted = false
	c.mu.Unlock()
	return result.Thread.ID, nil
}

func (c *codexAppServerClient) Prompt(ctx context.Context, id, prompt string) (string, error) {
	return c.PromptWithModel(ctx, id, prompt, "")
}

func (c *codexAppServerClient) PromptWithModel(ctx context.Context, id, prompt, model string) (string, error) {
	return c.PromptWithOptions(ctx, id, prompt, PromptOptions{Model: model})
}

func (c *codexAppServerClient) PromptWithOptions(ctx context.Context, id, prompt string, options PromptOptions) (string, error) {
	runCtx, cancel := context.WithCancel(ctx)
	run := &appServerRun{threadID: id, ctx: runCtx, cancel: cancel, turns: map[string]*appServerProgress{}, changed: make(chan struct{}, 1), identified: make(chan struct{}), questionsSettled: options.QuestionsSettled}
	c.mu.Lock()
	if c.active != nil || c.compaction != nil {
		c.mu.Unlock()
		cancel()
		return "", fmt.Errorf("codex app-server turn already active")
	}
	c.active = run
	c.mu.Unlock()
	defer func() {
		cancel()
		c.mu.Lock()
		if c.active == run {
			c.active = nil
		}
		c.mu.Unlock()
		run.promptWorkers.Wait()
	}()
	input := codexUserInput(prompt, options.Images)
	if skill := c.pickedSkill(runCtx, prompt); skill != nil {
		input = append(input, skill)
	}
	params := map[string]any{"threadId": id, "input": input}
	if options.Model != "" {
		params["model"] = options.Model
	}
	if options.Effort != "" {
		params["effort"] = options.Effort
	}
	target := c.threadSettings(id)
	mode, err := c.requestedMode(ctx, target, options)
	if err != nil {
		return "", err
	}
	if mode != nil {
		params["collaborationMode"] = mode
	}
	tier, preferred, err := c.turnServiceTier(ctx, target, options)
	if err != nil {
		return "", err
	}
	if options.PreservedTier != nil {
		preferred = *options.PreservedTier
		// The queued card runs at its captured speed. Preserve a different
		// native thread setting for messages delivered after that card.
		if preferred == "" && tier != target.ServiceTier {
			preferred = firstNonEmpty(target.ServiceTier, standardServiceTier)
		}
	}
	if tier != "" {
		params["serviceTier"] = tier
	}
	response, err := c.request(ctx, "turn/start", params)
	if err != nil {
		// The request may already have reached the server. Closing is the only
		// reliable cancellation if no turn ID was acknowledged.
		_ = c.Close()
		return "", err
	}
	var result struct {
		Turn appServerTurn `json:"turn"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		_ = c.Close()
		return "", err
	}
	if result.Turn.ID == "" {
		_ = c.Close()
		return "", fmt.Errorf("codex turn/start returned an empty turn ID")
	}
	c.mu.Lock()
	run.turnID = result.Turn.ID
	close(run.identified)
	idleSink, _ := ctx.Value(idleUsageKey{}).(usageSink)
	paginated := c.threadHistoryMode == "paginated" && c.threadOptions.ExternalSessionID == id
	if options.Model != "" && options.Model != target.Model {
		target.Effort = ""
	}
	target.Model = firstNonEmpty(options.Model, target.Model)
	if mode != nil {
		target.InteractionMode = mode.Mode
		c.threadCollaboration = mode
	}
	c.lastUsage = codexUsageTarget{thread: id, turn: run.turnID, model: target.Model, report: idleSink}
	target.Effort = firstNonEmpty(options.Effort, target.Effort)
	target.ServiceTier = firstNonEmpty(tier, target.ServiceTier)
	target.PreferredServiceTier = preferred
	c.threadOptions = target
	c.threadTurnStarted = true
	c.mu.Unlock()
	if paginated && options.Steering != nil && options.Steering.Started != nil {
		options.Steering.Started(target, result.Turn.ID)
	}
	if options.Steering != nil && options.Steering.Target != nil {
		options.Steering.Target(target)
	}
	sink := ActivitySinkFromContext(ctx)
	requests := nativeInputRequests(runCtx, options.Steering, turn.Steering)
	for {
		c.mu.Lock()
		progress := run.turns[result.Turn.ID]
		var completed *appServerTurn
		var reply string
		var events []Activity
		if progress != nil {
			events, progress.activity = progress.activity, nil
			completed = progress.completed
			if completed != nil {
				run.drained = true
				reply = progress.reply()
			}
		}
		c.mu.Unlock()
		if sink != nil {
			for _, event := range events {
				if event.ContextUsage != nil && event.ContextUsage.Window > 0 {
					event.ContextUsage.Model = target.Model
				}
				sink(event)
			}
		}
		if completed != nil {
			if completed.Status != "completed" {
				message := completed.Status
				if completed.Error != nil {
					message += ": " + completed.Error.Message
				}
				return "", fmt.Errorf("codex turn %s", message)
			}
			if strings.TrimSpace(reply) == "" {
				return "", fmt.Errorf("codex returned an empty final response")
			}
			return reply, nil
		}
		select {
		case correction := <-requests:
			err := c.steer(run, correction.input, sink)
			if err == nil {
				options.Steering.Applied(correction.input)
			}
			correction.result <- err
		case <-run.changed:
		case <-ctx.Done():
			interruptCtx, interruptCancel := context.WithTimeout(context.Background(), time.Second)
			_, _ = c.request(interruptCtx, "turn/interrupt", map[string]string{"threadId": id, "turnId": result.Turn.ID})
			interruptCancel()
			_ = c.Close()
			return "", ctx.Err()
		case <-c.done:
			// Check a queued completion before reporting a process exit.
			c.mu.Lock()
			p := run.turns[result.Turn.ID]
			finished := p != nil && p.completed != nil
			c.mu.Unlock()
			if !finished {
				return "", c.failure()
			}
		}
	}
}

func (p *appServerProgress) record(item appServerItem) {
	if (item.Type != "agentMessage" && item.Type != "plan") || item.ID == "" {
		return
	}
	if _, exists := p.items[item.ID]; !exists {
		p.order = append(p.order, item.ID)
	}
	p.items[item.ID] = item
	delete(p.proposalDeltas, item.ID)
}

func (p *appServerProgress) reply() string {
	var final, legacy, proposal string
	for _, id := range p.order {
		item := p.items[id]
		if item.Type == "plan" {
			text := item.Text
			if delta := p.proposalDeltas[id]; delta != nil {
				text = delta.String()
			}
			if strings.TrimSpace(text) != "" {
				proposal = text
			}
			continue
		}
		if item.Phase == "final_answer" {
			final = item.Text
		}
		if item.Phase == "" {
			legacy = item.Text
		}
	}
	if reply := firstNonEmpty(final, legacy, proposal); reply != "" {
		return reply
	}
	if p.steps != nil {
		return p.steps.Plan.ReplyText(p.steps.Text)
	}
	return ""
}

func (p *appServerProgress) addPlanDelta(id, delta string) {
	item, ok := p.items[id]
	if !ok {
		item = appServerItem{ID: id, Type: "plan"}
		p.record(item)
	}
	if p.proposalDeltas == nil {
		p.proposalDeltas = make(map[string]*strings.Builder)
	}
	buf := p.proposalDeltas[id]
	if buf == nil {
		buf = &strings.Builder{}
		buf.WriteString(item.Text)
		p.proposalDeltas[id] = buf
	}
	buf.WriteString(delta)
}

type appServerNotification struct {
	SummaryIndex *int `json:"summaryIndex"`
	Changes      json.RawMessage
	TokenUsage   *struct {
		Last               struct{ TotalTokens int64 }
		ModelContextWindow int64
	}
	Plan        []codexPlanStep `json:"plan"`
	Explanation string          `json:"explanation"`
	ThreadID    string          `json:"threadId"`
	TurnID      string          `json:"turnId"`
	ItemID      string          `json:"itemId"`
	Delta       string          `json:"delta"`
	Item        appServerItem   `json:"item"`
	Turn        appServerTurn   `json:"turn"`
	WillRetry   bool
	Error       struct{ Message string }
}

func (c *codexAppServerClient) notification(envelope rpcEnvelope) {
	if envelope.Method == "serverRequest/resolved" {
		c.resolvePrompt(envelope.Params)
		return
	}
	var params appServerNotification
	if json.Unmarshal(envelope.Params, &params) != nil {
		return
	}
	if envelope.Method == "turn/completed" || envelope.Method == "turn/started" {
		params.TurnID = params.Turn.ID
	}
	var publishIdle func()
	c.mu.Lock()
	defer func() {
		c.mu.Unlock()
		if publishIdle != nil {
			publishIdle()
		}
	}()
	if command := c.compaction; command != nil {
		if command.finished && command.marked && envelope.Method == "thread/tokenUsage/updated" && params.ThreadID == command.threadID && (params.TurnID == "" || params.TurnID == command.turnID) {
			if u := c.stampUsage(params.contextUsage()); u != nil {
				command.usage = u
			}
			return
		}
		command.observe(envelope.Method, params)
		return
	}
	run := c.active
	if (run == nil || run.drained) && envelope.Method == "thread/tokenUsage/updated" {
		owner := c.lastUsage
		if owner.report != nil && owner.thread == params.ThreadID && (params.TurnID == "" || params.TurnID == owner.turn) {
			if u := c.stampUsage(params.contextUsage()); u != nil {
				u.Model = owner.model
				publishIdle = func() { owner.report(owner.thread, *u) }
			}
		}
		return
	}
	if run != nil && envelope.Method == "thread/tokenUsage/updated" && params.TurnID == "" {
		params.TurnID = run.turnID
	}
	if run == nil || params.ThreadID != run.threadID || params.TurnID == "" || (run.turnID != "" && run.turnID != params.TurnID) {
		return
	}
	progress := run.turns[params.TurnID]
	if progress == nil {
		progress = &appServerProgress{items: map[string]appServerItem{}}
		run.turns[params.TurnID] = progress
	}
	switch envelope.Method {
	case "thread/tokenUsage/updated":
		if u := c.stampUsage(params.contextUsage()); u != nil {
			progress.activity = append(progress.activity, Activity{NativeSession: params.ThreadID, ContextUsage: u})
		}
	case "thread/compacted":
		progress.compactionSeen = true
		progress.activity = append(progress.activity, Activity{NativeSession: params.ThreadID, ContextUsage: c.stampUsage(&ContextUsage{})})
	case "turn/plan/updated":
		if params.Plan == nil {
			return
		}
		progress.steps = codexSteps(params.Explanation, params.Plan)
		if ActivitySinkFromContext(run.ctx) != nil {
			progress.activity = append(progress.activity, Activity{Plan: progress.steps})
		}
	case "item/plan/delta":
		if params.ItemID == "" || params.Delta == "" {
			return
		}
		progress.addPlanDelta(params.ItemID, params.Delta)
		if ActivitySinkFromContext(run.ctx) != nil {
			progress.activity = append(progress.activity, Activity{Plan: proposedPlan(params.ItemID, params.Delta, true)})
		}
	case "item/started", "item/completed":
		if params.Item.Type == "contextCompaction" && envelope.Method == "item/completed" {
			progress.compactionSeen = true
			progress.activity = append(progress.activity, Activity{NativeSession: params.ThreadID, ContextUsage: c.stampUsage(&ContextUsage{})})
		}
		progress.record(params.Item)
		if ActivitySinkFromContext(run.ctx) != nil {
			if event, ok := codexItemActivity(params.Item, envelope.Method == "item/completed"); ok {
				progress.activity = append(progress.activity, event)
			}
		}
	case "item/agentMessage/delta":
		item := progress.items[params.ItemID]
		item.ID, item.Type = params.ItemID, "agentMessage"
		item.Text += params.Delta
		progress.record(item)
		if ActivitySinkFromContext(run.ctx) != nil && params.Delta != "" {
			progress.activity = append(progress.activity, Activity{TextID: params.ItemID, Text: params.Delta})
		}
	case "item/reasoning/summaryTextDelta":
		if ActivitySinkFromContext(run.ctx) != nil && params.ItemID != "" && params.SummaryIndex != nil && *params.SummaryIndex >= 0 && params.Delta != "" {
			progress.activity = append(progress.activity, Activity{Summary: &SummaryUpdate{ID: params.ItemID, Index: *params.SummaryIndex, Delta: params.Delta}})
		}
	case "item/commandExecution/outputDelta":
		if ActivitySinkFromContext(run.ctx) != nil && params.ItemID != "" {
			progress.activity = append(progress.activity, Activity{CallID: params.ItemID, Name: "commandExecution", Status: "running", Output: params.Delta, AppendOutput: true})
		}
	case "item/fileChange/patchUpdated":
		if ActivitySinkFromContext(run.ctx) != nil && params.ItemID != "" {
			if edits := codexFileEdits(params.Changes); edits != nil {
				progress.activity = append(progress.activity, Activity{CallID: params.ItemID, Name: "fileChange", Status: "running", Edits: edits})
			}
		}
	case "item/fileChange/outputDelta":
		if ActivitySinkFromContext(run.ctx) != nil && params.ItemID != "" {
			progress.activity = append(progress.activity, Activity{CallID: params.ItemID, Name: "fileChange", Status: "running", Output: params.Delta, AppendOutput: true})
		}
	case "turn/completed":
		for _, item := range params.Turn.Items {
			progress.record(item)
			if !progress.compactionSeen && item.Type == "contextCompaction" {
				progress.compactionSeen = true
				progress.activity = append(progress.activity, Activity{NativeSession: params.ThreadID, ContextUsage: c.stampUsage(&ContextUsage{})})
			}
			if ActivitySinkFromContext(run.ctx) != nil {
				if event, ok := codexItemActivity(item, true); ok {
					progress.activity = append(progress.activity, event)
				}
			}
		}
		progress.completed = &params.Turn
	default:
		return
	}
	select {
	case run.changed <- struct{}{}:
	default:
	}
}

func (c *codexAppServerClient) setContextPermissionHandler(handler func(context.Context, acpPermissionParams) acpPermissionOutcome) {
	c.mu.Lock()
	c.permissionHandler = handler
	c.mu.Unlock()
}

// A native request can arrive before the turn/start reply identifies our
// turn. Only publish it after that binding is known and still active.
func (c *codexAppServerClient) ownsPrompt(ctx context.Context, run *appServerRun, thread, turn string) bool {
	if run == nil || turn == "" || thread != run.threadID {
		return false
	}
	select {
	case <-ctx.Done():
		return false
	case <-run.identified:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	progress := run.turns[turn]
	return c.active == run && run.turnID == turn && !run.drained && (progress == nil || progress.completed == nil) && ctx.Err() == nil
}

func (c *codexAppServerClient) Close() error {
	c.closeOnce.Do(func() {
		c.stop(io.ErrClosedPipe)
		_ = c.stdin.Close()
		if c.cmd.Cancel != nil {
			_ = c.cmd.Cancel()
		} else {
			_ = c.cmd.Process.Kill()
		}
		<-c.waitDone
	})
	return nil
}

func (c *codexAppServerClient) stop(err error) {
	c.stopOnce.Do(func() {
		c.mu.Lock()
		c.readErr = fmt.Errorf("codex app-server process exited: %w", err)
		if c.active != nil {
			c.active.cancel()
		}
		c.mu.Unlock()
		close(c.done)
	})
}

func (c *codexAppServerClient) failure() error { c.mu.Lock(); defer c.mu.Unlock(); return c.readErr }

func (c *codexAppServerClient) request(ctx context.Context, method string, params any) (rpcEnvelope, error) {
	id := c.nextID.Add(1)
	key := fmt.Sprint(id)
	ch := make(chan rpcEnvelope, 1)
	c.mu.Lock()
	c.responses[key] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.responses, key); c.mu.Unlock() }()
	select {
	case <-c.done:
		return rpcEnvelope{}, c.failure()
	case <-ctx.Done():
		return rpcEnvelope{}, ctx.Err()
	default:
	}
	if err := c.write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return rpcEnvelope{}, err
	}
	var response rpcEnvelope
	select {
	case response = <-ch:
	case <-ctx.Done():
		return rpcEnvelope{}, ctx.Err()
	case <-c.done:
		select {
		case response = <-ch:
		default:
			return rpcEnvelope{}, c.failure()
		}
	}
	if response.Error != nil {
		return response, errors.New(response.Error.Message)
	}
	return response, nil
}

func (c *codexAppServerClient) write(payload any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.writeLocked(payload)
}

func (c *codexAppServerClient) writeLocked(payload any) error {
	return json.NewEncoder(c.stdin).Encode(payload)
}

func (c *codexAppServerClient) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, acpScannerInitialBufferSize), maxACPProtocolMessageBytes)
	for scanner.Scan() {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var envelope rpcEnvelope
		if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
			c.stop(fmt.Errorf("invalid JSON-RPC: %w", err))
			return
		}
		if len(envelope.ID) > 0 && envelope.Method != "" {
			// Claim the native ID before reading the next frame, which may
			// withdraw the request. Only the interaction waits on the user.
			c.startServerRequest(envelope)
			continue
		}
		if len(envelope.ID) > 0 {
			c.mu.Lock()
			ch := c.responses[string(bytes.TrimSpace(envelope.ID))]
			c.mu.Unlock()
			if ch != nil {
				select {
				case ch <- envelope:
				default:
				}
			}
		} else {
			c.notification(envelope)
		}
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	c.stop(err)
}
