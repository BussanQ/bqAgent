package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	defaultBashYieldTimeout    = 30 * time.Second
	defaultBashMaxConcurrent   = 3
	defaultBashWaitSeconds     = 30
	maxBashWaitSeconds         = 300
	bashNewOutputMaxBytes      = 16 << 10
	bashRunningOutputMaxBytes  = 16 << 10
	bashRetainedObservedJobs   = 32
	bashObservedOutputMaxBytes = 64 << 10

	BashJobStatusRunning   = "running"
	BashJobStatusCompleted = "completed"
	BashJobStatusFailed    = "failed"
	BashJobStatusKilled    = "killed"
)

// BashJobOptions configures the per-turn background job manager.
type BashJobOptions struct {
	// YieldTimeout is how long execute_bash waits before returning a running
	// job handle. Zero means 30s.
	YieldTimeout time.Duration
	// MaxTimeout is a hard cap on a job's lifetime. Zero disables it.
	MaxTimeout time.Duration
	// MaxConcurrent bounds simultaneously running jobs. Zero means 3.
	MaxConcurrent int
}

// BashJobStatus is the JSON-visible snapshot of a background job.
type BashJobStatus struct {
	JobID                string  `json:"job_id"`
	Status               string  `json:"status"`
	Command              string  `json:"command"`
	ExitCode             *int    `json:"exit_code,omitempty"`
	Error                string  `json:"error,omitempty"`
	Output               string  `json:"output"`
	NewOutput            string  `json:"new_output"`
	ElapsedSeconds       float64 `json:"elapsed_seconds"`
	LastOutputAgeSeconds float64 `json:"last_output_age_seconds"`
	OutputBytes          int64   `json:"output_bytes"`
	Truncated            bool    `json:"truncated"`
}

// BashResultCapture records the trusted structured result of the most recent
// execute_bash / bash_wait / bash_kill call made with its context.
type BashResultCapture struct {
	mu     sync.Mutex
	Result *BashJobStatus
}

var errBashMaxTimeout = errors.New("background job exceeded max timeout")

type bashJobsKey struct{}
type bashCaptureKey struct{}

// WithBashJobs attaches a fresh, isolated job manager to ctx. Cancelling ctx
// kills every job the manager owns.
func WithBashJobs(ctx context.Context, options BashJobOptions) (context.Context, *BashJobs) {
	if options.YieldTimeout <= 0 {
		options.YieldTimeout = defaultBashYieldTimeout
	}
	if options.MaxTimeout < 0 {
		options.MaxTimeout = 0
	}
	if options.MaxConcurrent <= 0 {
		options.MaxConcurrent = defaultBashMaxConcurrent
	}
	mctx, cancel := context.WithCancel(ctx)
	token := make([]byte, 4)
	_, _ = rand.Read(token)
	manager := &BashJobs{
		ctx:     mctx,
		cancel:  cancel,
		options: options,
		token:   hex.EncodeToString(token),
		jobs:    map[string]*bashJob{},
	}
	return context.WithValue(ctx, bashJobsKey{}, manager), manager
}

// BashJobsFromContext returns the manager attached by WithBashJobs, or nil.
func BashJobsFromContext(ctx context.Context) *BashJobs {
	if ctx == nil {
		return nil
	}
	manager, _ := ctx.Value(bashJobsKey{}).(*BashJobs)
	return manager
}

// WithBashResultCapture returns a ctx whose bash tool calls record their
// structured result into the returned capture.
func WithBashResultCapture(ctx context.Context) (context.Context, *BashResultCapture) {
	capture := &BashResultCapture{}
	return context.WithValue(ctx, bashCaptureKey{}, capture), capture
}

func recordBashResult(ctx context.Context, status BashJobStatus) {
	if capture, _ := ctx.Value(bashCaptureKey{}).(*BashResultCapture); capture != nil {
		capture.mu.Lock()
		copied := status
		capture.Result = &copied
		capture.mu.Unlock()
	}
}

// BashJobs owns the background jobs of a single turn.
type BashJobs struct {
	ctx     context.Context
	cancel  context.CancelFunc
	options BashJobOptions
	token   string

	mu     sync.Mutex
	jobs   map[string]*bashJob
	order  []string
	seq    int
	closed bool
	wg     sync.WaitGroup
}

type bashJob struct {
	id       string
	command  string
	started  time.Time
	cancel   context.CancelFunc
	ctx      context.Context
	proc     *bashProcess
	done     chan struct{}
	output   *headTailBuffer
	newOut   *headTailBuffer
	lastNano atomic.Int64

	mu            sync.RWMutex
	status        string
	exitCode      *int
	errMsg        string
	finished      time.Time
	killRequested bool
	observed      bool
}

// jobWriter fans process output into the total and "new since last look"
// buffers and records when output last arrived.
type jobWriter struct{ job *bashJob }

func (w jobWriter) Write(data []byte) (int, error) {
	w.job.output.Write(data)
	w.job.newOut.Write(data)
	w.job.lastNano.Store(time.Now().UnixNano())
	return len(data), nil
}

func (m *BashJobs) start(toolCtx context.Context, root, command string, maxOutputBytes int64) (*bashJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.ctx.Err() != nil {
		return nil, errors.New("background jobs are closed")
	}
	running := 0
	for _, job := range m.jobs {
		if job.isRunning() {
			running++
		}
	}
	if running >= m.options.MaxConcurrent {
		return nil, fmt.Errorf("too many active background jobs (limit %d); wait for or kill one with bash_wait/bash_kill", m.options.MaxConcurrent)
	}

	if maxOutputBytes <= 0 {
		maxOutputBytes = DefaultBashOutputMaxBytes
	}
	newLimit := maxOutputBytes
	if newLimit > bashNewOutputMaxBytes {
		newLimit = bashNewOutputMaxBytes
	}
	// The job inherits the calling tool's context (values and cancellation) and
	// is additionally cancelled when the manager's context ends.
	baseCtx, baseCancel := context.WithCancel(toolCtx)
	jobCtx, cancel := baseCtx, baseCancel
	if m.options.MaxTimeout > 0 {
		var timeoutCancel context.CancelFunc
		jobCtx, timeoutCancel = context.WithTimeoutCause(baseCtx, m.options.MaxTimeout, errBashMaxTimeout)
		cancel = func() { timeoutCancel(); baseCancel() }
	}
	stopLink := context.AfterFunc(m.ctx, cancel)
	cancelAll := cancel
	cancel = func() { stopLink(); cancelAll() }
	m.seq++
	job := &bashJob{
		id:      fmt.Sprintf("bg-%s-%d", m.token, m.seq),
		command: command,
		started: time.Now(),
		cancel:  cancel,
		ctx:     jobCtx,
		done:    make(chan struct{}),
		output:  newHeadTailBuffer(maxOutputBytes),
		newOut:  newHeadTailBuffer(newLimit),
		status:  BashJobStatusRunning,
	}
	job.lastNano.Store(job.started.UnixNano())
	proc, err := startBashProcess(jobCtx, root, command, jobWriter{job})
	if err != nil {
		cancel()
		return nil, err
	}
	job.proc = proc
	m.jobs[job.id] = job
	m.order = append(m.order, job.id)
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		job.finish(proc.wait())
	}()
	return job, nil
}

func (j *bashJob) finish(err error) {
	j.mu.Lock()
	switch {
	case err == nil:
		code := 0
		j.exitCode = &code
		j.status = BashJobStatusCompleted
	case j.killRequested:
		j.status = BashJobStatusKilled
	case errors.Is(context.Cause(j.ctx), errBashMaxTimeout):
		j.status = BashJobStatusFailed
		j.errMsg = "command exceeded the maximum background job timeout"
	case j.ctx.Err() != nil:
		j.status = BashJobStatusKilled
		j.errMsg = j.ctx.Err().Error() // "context deadline exceeded" / "context canceled"
	default:
		j.status = BashJobStatusFailed
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code := exitErr.ExitCode()
			j.exitCode = &code
		} else {
			j.errMsg = fmt.Sprintf("command execution failed: %v", err)
		}
	}
	j.finished = time.Now()
	j.mu.Unlock()
	j.cancel()
	close(j.done)
}

func (j *bashJob) isRunning() bool {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.status == BashJobStatusRunning
}

func (j *bashJob) markObserved() {
	j.mu.Lock()
	j.observed = true
	j.mu.Unlock()
}

func (j *bashJob) isObserved() bool {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.observed
}

// snapshot builds a status. With consume, the "new output" window is reset.
func (j *bashJob) snapshot(consume bool) BashJobStatus {
	j.mu.RLock()
	status, exitCode, finished, errMsg := j.status, j.exitCode, j.finished, j.errMsg
	j.mu.RUnlock()

	end := time.Now()
	if status != BashJobStatusRunning && !finished.IsZero() {
		end = finished
	}
	last := time.Unix(0, j.lastNano.Load())
	if last.After(end) {
		last = end
	}
	var newOutput string
	if consume {
		newOutput = j.newOut.take()
	} else {
		newOutput = j.newOut.String()
	}
	text, truncated := j.output.view()
	if status == BashJobStatusRunning && len(text) > bashRunningOutputMaxBytes {
		// Running snapshots carry only a recent tail; the full bounded result
		// is delivered once the job reaches a terminal state.
		text = text[len(text)-bashRunningOutputMaxBytes:]
		for len(text) > 0 && !utf8.RuneStart(text[0]) {
			text = text[1:]
		}
		truncated = true
	}
	command := j.command
	var code *int
	if exitCode != nil {
		c := *exitCode
		code = &c
	}
	return BashJobStatus{
		JobID:                j.id,
		Status:               status,
		Command:              command,
		ExitCode:             code,
		Error:                errMsg,
		Output:               text,
		NewOutput:            newOutput,
		ElapsedSeconds:       end.Sub(j.started).Seconds(),
		LastOutputAgeSeconds: end.Sub(last).Seconds(),
		OutputBytes:          j.output.Total(),
		Truncated:            truncated,
	}
}

func (j *bashJob) failureError() error {
	j.mu.RLock()
	defer j.mu.RUnlock()
	if j.exitCode != nil {
		return fmt.Errorf("command exited with status %d", *j.exitCode)
	}
	if j.errMsg != "" {
		return errors.New(j.errMsg)
	}
	return errors.New("command failed")
}

func (j *bashJob) kill() {
	j.mu.Lock()
	if j.status == BashJobStatusRunning {
		j.killRequested = true
	}
	j.mu.Unlock()
	j.cancel()
}

// waitFor blocks until the job finishes, d elapses or ctx is done.
func (j *bashJob) waitFor(ctx context.Context, d time.Duration) (finished bool, err error) {
	select {
	case <-j.done:
		return true, nil
	default:
	}
	if d <= 0 {
		return false, nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-j.done:
		return true, nil
	case <-timer.C:
		return false, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func (m *BashJobs) lookup(id string) (*bashJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return nil, fmt.Errorf("unknown background job %q (handles are only valid within the turn that created them)", id)
	}
	return job, nil
}

// Pending lists jobs whose final state has not been observed by a tool call,
// including finished jobs nobody has bash_wait-ed yet. It does not consume
// output windows.
func (m *BashJobs) Pending() []BashJobStatus {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	jobs := make([]*bashJob, 0, len(m.order))
	for _, id := range m.order {
		if job := m.jobs[id]; job != nil {
			jobs = append(jobs, job)
		}
	}
	m.mu.Unlock()
	var result []BashJobStatus
	for _, job := range jobs {
		if !job.isObserved() {
			result = append(result, job.snapshot(false))
		}
	}
	return result
}

// Close cancels all jobs, waits for them to be reaped and returns the final
// state of jobs that were never observed. It is safe to call repeatedly.
func (m *BashJobs) Close() []BashJobStatus {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.cancel()
	m.wg.Wait()
	pending := m.Pending()
	m.mu.Lock()
	for _, job := range m.jobs {
		job.markObserved()
	}
	m.mu.Unlock()
	return pending
}

func marshalBashStatus(status BashJobStatus) (string, error) {
	data, err := json.Marshal(status)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// executeWithJobs runs command as a job, waiting up to YieldTimeout.
func (m *BashJobs) execute(ctx context.Context, root, command string, maxOutputBytes int64) (string, error) {
	job, err := m.start(ctx, root, command, maxOutputBytes)
	if err != nil {
		return "", err
	}
	finished, waitErr := job.waitFor(ctx, m.options.YieldTimeout)
	if waitErr != nil {
		// The calling tool context ended: do not leave the job behind.
		job.kill()
		<-job.done
		status := job.snapshot(true)
		m.remove(job)
		recordBashResult(ctx, status)
		return status.Output, waitErr
	}
	status := job.snapshot(true)
	// The job may have finished between waitFor and snapshot; the snapshot is
	// authoritative.
	finished = status.Status != BashJobStatusRunning
	recordBashResult(ctx, status)
	if !finished {
		return marshalBashStatus(status)
	}
	// Short command: its full result is returned now, so nothing is retained.
	m.remove(job)
	switch status.Status {
	case BashJobStatusCompleted:
		return status.Output, nil
	case BashJobStatusKilled:
		if err := ctx.Err(); err != nil {
			return status.Output, err
		}
		return status.Output, context.Canceled
	default:
		return status.Output, job.failureError()
	}
}

// remove drops a job whose result has been fully delivered.
func (m *BashJobs) remove(job *bashJob) {
	job.markObserved()
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.jobs, job.id)
	for i, id := range m.order {
		if id == job.id {
			m.order = append(m.order[:i:i], m.order[i+1:]...)
			break
		}
	}
}

// observeFinal marks a finished long-running job as observed, shrinks its
// retained output and evicts the oldest observed jobs beyond a fixed count.
func (m *BashJobs) observeFinal(job *bashJob) {
	job.markObserved()
	job.output.shrink(bashObservedOutputMaxBytes)
	m.mu.Lock()
	defer m.mu.Unlock()
	var observed []string
	for _, id := range m.order {
		if j := m.jobs[id]; j != nil && j.isObserved() && !j.isRunning() {
			observed = append(observed, id)
		}
	}
	for len(observed) > bashRetainedObservedJobs {
		id := observed[0]
		observed = observed[1:]
		delete(m.jobs, id)
		for i, o := range m.order {
			if o == id {
				m.order = append(m.order[:i:i], m.order[i+1:]...)
				break
			}
		}
	}
}

func parseBashJobID(args map[string]any) (string, error) {
	id, err := requireString(args, "job_id")
	if err != nil {
		return "", err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return "", errors.New(`argument "job_id" must not be empty`)
	}
	return id, nil
}

func parseBashWaitSeconds(args map[string]any) (time.Duration, error) {
	raw, ok := args["wait_seconds"]
	if !ok {
		return defaultBashWaitSeconds * time.Second, nil
	}
	var value float64
	switch v := raw.(type) {
	case float64:
		value = v
	case int:
		value = float64(v)
	case int64:
		value = float64(v)
	case json.Number:
		parsed, err := v.Float64()
		if err != nil {
			return 0, errors.New(`argument "wait_seconds" must be an integer number of seconds`)
		}
		value = parsed
	default:
		return 0, errors.New(`argument "wait_seconds" must be an integer number of seconds`)
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) {
		return 0, errors.New(`argument "wait_seconds" must be a finite integer number of seconds`)
	}
	if value < 0 || value > maxBashWaitSeconds {
		return 0, fmt.Errorf(`argument "wait_seconds" must be between 0 and %d`, maxBashWaitSeconds)
	}
	return time.Duration(value) * time.Second, nil
}

// BashWait waits for a background job and reports its status.
func BashWait(ctx context.Context, args map[string]any) (string, error) {
	id, err := parseBashJobID(args)
	if err != nil {
		return "", err
	}
	wait, err := parseBashWaitSeconds(args)
	if err != nil {
		return "", err
	}
	manager := BashJobsFromContext(ctx)
	if manager == nil {
		return "", errors.New("background bash jobs are not available in this context")
	}
	job, err := manager.lookup(id)
	if err != nil {
		return "", err
	}
	if _, waitErr := job.waitFor(ctx, wait); waitErr != nil {
		return "", waitErr
	}
	status := job.snapshot(true)
	finished := status.Status != BashJobStatusRunning
	if finished {
		manager.observeFinal(job)
	}
	recordBashResult(ctx, status)
	text, err := marshalBashStatus(status)
	if err != nil {
		return "", err
	}
	if finished && status.Status == BashJobStatusFailed {
		return text, job.failureError()
	}
	return text, nil
}

// BashKill terminates a background job's whole process group.
func BashKill(ctx context.Context, args map[string]any) (string, error) {
	id, err := parseBashJobID(args)
	if err != nil {
		return "", err
	}
	manager := BashJobsFromContext(ctx)
	if manager == nil {
		return "", errors.New("background bash jobs are not available in this context")
	}
	job, err := manager.lookup(id)
	if err != nil {
		return "", err
	}
	job.kill()
	select {
	case <-job.done:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	manager.observeFinal(job)
	status := job.snapshot(true)
	recordBashResult(ctx, status)
	return marshalBashStatus(status)
}

// bashProcess is a started shell whose output is drained through an os pipe we
// own, so Wait returns when the shell exits and leftover children can be
// killed before we wait for the pipe to drain.
type bashProcess struct {
	cmd      *exec.Cmd
	reader   *os.File
	copyDone chan struct{}
}

func startBashProcess(ctx context.Context, root, command string, out io.Writer) (*bashProcess, error) {
	cmd := shellCommand(ctx, command)
	if root != "" {
		cmd.Dir = root
	}
	// A nil Stdin is wired to the null device, so commands that read stdin
	// get EOF instead of blocking on an interactive prompt.
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
		"PAGER=cat",
		"DEBIAN_FRONTEND=noninteractive",
	)
	cmd.WaitDelay = commandWaitDelay
	configureProcessGroup(cmd)

	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("command execution failed: %w", err)
	}
	// Same file for both streams preserves write ordering.
	cmd.Stdout = writer
	cmd.Stderr = writer
	if err := cmd.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, fmt.Errorf("command execution failed: %w", err)
	}
	_ = writer.Close()
	proc := &bashProcess{cmd: cmd, reader: reader, copyDone: make(chan struct{})}
	go func() {
		defer close(proc.copyDone)
		_, _ = io.Copy(out, reader)
	}()
	return proc, nil
}

// wait reaps the shell, kills any process left in its group and drains output.
func (p *bashProcess) wait() error {
	err := p.cmd.Wait()
	killProcessGroup(p.cmd)
	select {
	case <-p.copyDone:
	case <-time.After(commandWaitDelay):
		_ = p.reader.Close()
		<-p.copyDone
	}
	_ = p.reader.Close()
	return err
}

// headTailBuffer retains at most limit bytes: the first half and the last half
// of the stream (the tail is a fixed-size ring), with a marker in between when
// bytes were dropped.
type headTailBuffer struct {
	mu      sync.Mutex
	headCap int
	tailCap int
	head    []byte
	tail    []byte // grows to tailCap, then is used as a ring starting at tailPos
	tailPos int
	omitted int64
	total   int64
}

func newHeadTailBuffer(limit int64) *headTailBuffer {
	if limit < 2 {
		limit = 2
	}
	headCap := int(limit / 2)
	return &headTailBuffer{headCap: headCap, tailCap: int(limit) - headCap}
}

func (b *headTailBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(data)
	b.total += int64(n)
	if room := b.headCap - len(b.head); room > 0 {
		keep := min(room, len(data))
		b.head = append(b.head, data[:keep]...)
		data = data[keep:]
	}
	if len(data) == 0 {
		return n, nil
	}
	if len(data) >= b.tailCap {
		b.omitted += int64(len(b.tail) + len(data) - b.tailCap)
		b.tail = append(b.tail[:0], data[len(data)-b.tailCap:]...)
		b.tailPos = 0
		return n, nil
	}
	if room := b.tailCap - len(b.tail); room > 0 {
		keep := min(room, len(data))
		b.tail = append(b.tail, data[:keep]...)
		data = data[keep:]
	}
	for len(data) > 0 { // tail is full: overwrite oldest bytes
		chunk := copy(b.tail[b.tailPos:], data)
		data = data[chunk:]
		b.tailPos = (b.tailPos + chunk) % len(b.tail)
		b.omitted += int64(chunk)
	}
	return n, nil
}

func (b *headTailBuffer) tailBytesLocked() []byte {
	if b.tailPos == 0 {
		return b.tail
	}
	out := make([]byte, 0, len(b.tail))
	out = append(out, b.tail[b.tailPos:]...)
	return append(out, b.tail[:b.tailPos]...)
}

func (b *headTailBuffer) viewLocked() (string, bool) {
	tail := b.tailBytesLocked()
	if b.omitted == 0 {
		return string(b.head) + string(tail), false
	}
	return string(b.head) + fmt.Sprintf("\n[output truncated: %d bytes omitted]\n", b.omitted) + string(tail), true
}

// shrink lowers the retention limit, discarding the middle of what is held.
func (b *headTailBuffer) shrink(limit int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if int64(b.headCap+b.tailCap) <= limit {
		return
	}
	headCap := int(limit / 2)
	tailCap := int(limit) - headCap
	if len(b.head) > headCap {
		b.omitted += int64(len(b.head) - headCap)
		b.head = append([]byte(nil), b.head[:headCap]...)
	}
	tail := b.tailBytesLocked()
	if len(tail) > tailCap {
		b.omitted += int64(len(tail) - tailCap)
		tail = tail[len(tail)-tailCap:]
	}
	b.tail = append([]byte(nil), tail...)
	b.tailPos = 0
	b.headCap, b.tailCap = headCap, tailCap
}

func (b *headTailBuffer) view() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.viewLocked()
}

func (b *headTailBuffer) String() string {
	text, _ := b.view()
	return text
}

func (b *headTailBuffer) Total() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total
}

// take returns the retained content and resets the buffer.
func (b *headTailBuffer) take() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	text, _ := b.viewLocked()
	b.head, b.tail, b.tailPos, b.omitted, b.total = nil, nil, 0, 0, 0
	return text
}

// BashResultFromContext returns the result recorded so far in ctx's capture.
func BashResultFromContext(ctx context.Context) *BashJobStatus {
	capture, _ := ctx.Value(bashCaptureKey{}).(*BashResultCapture)
	if capture == nil {
		return nil
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.Result == nil {
		return nil
	}
	copied := *capture.Result
	return &copied
}

// Done returns a channel closed when the job has exited, or nil if unknown.
func (m *BashJobs) Done(jobID string) <-chan struct{} {
	if m == nil {
		return nil
	}
	job, err := m.lookup(jobID)
	if err != nil {
		return nil
	}
	return job.done
}

// Snapshot reports a job's current state without consuming new output or
// marking the job observed.
func (m *BashJobs) Snapshot(jobID string) (BashJobStatus, bool) {
	if m == nil {
		return BashJobStatus{}, false
	}
	job, err := m.lookup(jobID)
	if err != nil {
		return BashJobStatus{}, false
	}
	return job.snapshot(false), true
}

// Completion returns the job's done channel and a stable snapshot closure that
// keeps working even if the manager later evicts the job. Unknown jobs return
// nil, nil.
func (m *BashJobs) Completion(jobID string) (<-chan struct{}, func() BashJobStatus) {
	if m == nil {
		return nil, nil
	}
	job, err := m.lookup(jobID)
	if err != nil {
		return nil, nil
	}
	return job.done, func() BashJobStatus { return job.snapshot(false) }
}
