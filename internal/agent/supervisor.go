// Process supervisor. Used on both leader (to launch the coordinator
// llama-server for sharded models) and workers (to launch rpc-server per
// shard). Wraps os/exec with start/stop/list/logs + a TCP-port readiness
// probe so callers can wait until a launched process is actually serving.
//
// Supports optional restart-on-crash: set ProcessSpec.Restart and the
// supervisor will re-launch the process (with exponential backoff, capped
// at MaxRestarts) when it exits abnormally. Used by the sharding
// orchestrator so a single rpc-server dying mid-stream doesn't take the
// model offline until an admin re-runs `flock shard create`.
package agent

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// ProcessSpec describes a child process to launch.
type ProcessSpec struct {
	ID         string // caller-assigned unique id
	Command    string // absolute path or PATH-resolvable binary
	Args       []string
	Env        map[string]string // appended to os.Environ()
	WorkDir    string            // optional; defaults to CWD
	HealthPort int               // optional; if >0, waitReady probes TCP this port
	HealthHost string            // default "127.0.0.1"
	LogLines   int               // ring-buffer capacity (default 200)

	// Restart, when true, makes the supervisor re-launch the process on
	// abnormal exit (anything other than an explicit Stop). Sharding uses
	// this so a single rpc-server dying mid-stream doesn't take a whole
	// model offline.
	Restart bool
	// MaxRestarts caps how many times we'll retry before giving up and
	// marking the process "crashloop". 0 → unlimited (not recommended).
	// Default applied by Start: 5.
	MaxRestarts int
	// RestartBackoff is the initial backoff between restarts; doubles on
	// each consecutive failure up to a 30s cap. Default 1s.
	RestartBackoff time.Duration
}

// ProcessInfo is the observable state of a managed process.
type ProcessInfo struct {
	ID        string    `json:"id"`
	Command   string    `json:"command"`
	Args      []string  `json:"args"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	// Status: starting | running | stopped | failed | crashloop.
	// "crashloop" means Restart was enabled but MaxRestarts was exceeded.
	Status   string `json:"status"`
	ExitErr  string `json:"exit_err,omitempty"`
	Address  string `json:"address,omitempty"`
	Restarts int    `json:"restarts,omitempty"` // count of automatic restarts so far
}

type Process struct {
	Info   ProcessInfo
	spec   ProcessSpec
	cmd    *exec.Cmd
	cancel context.CancelFunc
	logBuf *ringBuffer
	mu     sync.RWMutex
}

// Supervisor manages a set of child processes by id.
type Supervisor struct {
	mu    sync.RWMutex
	procs map[string]*Process
	log   *slog.Logger
}

func NewSupervisor(log *slog.Logger) *Supervisor {
	if log == nil {
		log = slog.Default()
	}
	return &Supervisor{
		procs: make(map[string]*Process),
		log:   log,
	}
}

// Start launches a new managed process. Returns once the process has either
// reached "running" (PID + optional health probe pass) or "failed".
func (s *Supervisor) Start(ctx context.Context, spec ProcessSpec) (*ProcessInfo, error) {
	if spec.ID == "" {
		return nil, fmt.Errorf("ProcessSpec.ID required")
	}
	if spec.Command == "" {
		return nil, fmt.Errorf("ProcessSpec.Command required")
	}
	if spec.LogLines <= 0 {
		spec.LogLines = 200
	}
	if spec.HealthHost == "" {
		spec.HealthHost = "127.0.0.1"
	}
	if spec.Restart && spec.MaxRestarts == 0 {
		spec.MaxRestarts = 5
	}
	if spec.Restart && spec.RestartBackoff == 0 {
		spec.RestartBackoff = time.Second
	}

	s.mu.Lock()
	if _, ok := s.procs[spec.ID]; ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("process %q already exists", spec.ID)
	}
	s.mu.Unlock()

	addr := ""
	if spec.HealthPort > 0 {
		addr = net.JoinHostPort(spec.HealthHost, strconv.Itoa(spec.HealthPort))
	}

	logBuf := newRingBuffer(spec.LogLines)

	p := &Process{
		Info: ProcessInfo{
			ID:        spec.ID,
			Command:   spec.Command,
			Args:      spec.Args,
			StartedAt: time.Now(),
			Status:    "starting",
			Address:   addr,
		},
		spec:   spec,
		logBuf: logBuf,
	}

	s.mu.Lock()
	s.procs[spec.ID] = p
	s.mu.Unlock()

	if err := s.launchProc(ctx, p); err != nil {
		s.mu.Lock()
		delete(s.procs, spec.ID)
		s.mu.Unlock()
		return &p.Info, err
	}

	return s.snapshot(p), nil
}

// launchProc starts the process once; called by Start and (when Restart is
// enabled) by the restart loop in waitExit. Assumes the *Process is already
// registered in s.procs and holds the ring buffer.
func (s *Supervisor) launchProc(ctx context.Context, p *Process) error {
	spec := p.spec
	procCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, spec.Command, spec.Args...)
	// Put the child in its own process group so Stop() can signal the
	// whole group, killing any grandchildren the process forked.
	applyProcessGroup(cmd)
	// On Linux: also ask the kernel to SIGTERM the child if we (the
	// supervisor) die abnormally. No-op on macOS.
	applyParentDeathSignal(cmd)
	if spec.WorkDir != "" {
		cmd.Dir = spec.WorkDir
	}
	env := os.Environ()
	for k, v := range spec.Env {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stderr pipe: %w", err)
	}

	go readLines(stdout, p.logBuf)
	go readLines(stderr, p.logBuf)

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start %s: %w", filepath.Base(spec.Command), err)
	}

	p.mu.Lock()
	p.cmd = cmd
	p.cancel = cancel
	p.Info.PID = cmd.Process.Pid
	p.Info.StartedAt = time.Now()
	p.Info.ExitErr = ""
	p.Info.Status = "starting"
	p.mu.Unlock()

	if spec.HealthPort > 0 {
		if err := waitReady(ctx, spec.HealthHost, spec.HealthPort, 30*time.Second); err != nil {
			s.markFailed(p, fmt.Errorf("health probe: %w", err))
			return fmt.Errorf("process did not become ready: %w", err)
		}
	}

	p.mu.Lock()
	p.Info.Status = "running"
	p.mu.Unlock()

	go s.waitExit(p)

	s.log.Info("process started", "id", spec.ID, "pid", p.Info.PID, "command", spec.Command, "addr", p.Info.Address, "restarts", p.Info.Restarts)
	return nil
}

// Stop sends SIGTERM, waits up to 10s, then SIGKILLs.
func (s *Supervisor) Stop(id string) error {
	s.mu.RLock()
	p, ok := s.procs[id]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("process %q not found", id)
	}
	if p.cmd.Process == nil {
		return nil
	}
	// Signal the process group rather than just the leader so any
	// grandchildren the engine forked (download helpers, worker threads
	// the engine wraps in subprocesses) terminate too. Fall back to a
	// per-pid signal if the group signal fails (e.g. group already gone).
	pid := p.cmd.Process.Pid
	if err := signalGroup(pid, syscall.SIGTERM); err != nil {
		_ = p.cmd.Process.Signal(os.Interrupt)
	}
	done := make(chan struct{})
	go func() {
		_ = p.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		if err := signalGroup(pid, syscall.SIGKILL); err != nil {
			_ = p.cmd.Process.Kill()
		}
	}
	p.mu.Lock()
	p.Info.Status = "stopped"
	p.mu.Unlock()
	p.cancel()

	s.mu.Lock()
	delete(s.procs, id)
	s.mu.Unlock()

	s.log.Info("process stopped", "id", id, "pid", p.Info.PID)
	return nil
}

// StopAll terminates every managed process; used on agent shutdown.
func (s *Supervisor) StopAll() {
	s.mu.RLock()
	ids := make([]string, 0, len(s.procs))
	for id := range s.procs {
		ids = append(ids, id)
	}
	s.mu.RUnlock()
	for _, id := range ids {
		_ = s.Stop(id)
	}
}

// Get returns a snapshot of one process's info.
func (s *Supervisor) Get(id string) (*ProcessInfo, bool) {
	s.mu.RLock()
	p, ok := s.procs[id]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return s.snapshot(p), true
}

// List returns snapshots of all managed processes.
func (s *Supervisor) List() []*ProcessInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*ProcessInfo, 0, len(s.procs))
	for _, p := range s.procs {
		out = append(out, s.snapshot(p))
	}
	return out
}

// Logs returns the most recent log lines from the given process (stderr +
// stdout interleaved).
func (s *Supervisor) Logs(id string, lines int) []string {
	s.mu.RLock()
	p, ok := s.procs[id]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	return p.logBuf.tail(lines)
}

func (s *Supervisor) snapshot(p *Process) *ProcessInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()
	info := p.Info
	return &info
}

func (s *Supervisor) markFailed(p *Process, err error) {
	p.mu.Lock()
	p.Info.Status = "failed"
	p.Info.ExitErr = err.Error()
	p.mu.Unlock()
	p.cancel()
}

func (s *Supervisor) waitExit(p *Process) {
	err := p.cmd.Wait()
	p.mu.Lock()
	if p.Info.Status == "stopped" {
		p.mu.Unlock()
		return // we initiated this — no restart
	}
	if err != nil {
		p.Info.Status = "failed"
		p.Info.ExitErr = err.Error()
	} else {
		p.Info.Status = "stopped"
	}
	prevPID := p.Info.PID
	restartEnabled := p.spec.Restart
	restarts := p.Info.Restarts
	maxRestarts := p.spec.MaxRestarts
	backoff := p.spec.RestartBackoff
	p.mu.Unlock()

	s.log.Info("process exited", "id", p.spec.ID, "pid", prevPID, "status", p.Info.Status, "err", err, "restarts", restarts)

	if !restartEnabled {
		return
	}
	if maxRestarts > 0 && restarts >= maxRestarts {
		p.mu.Lock()
		p.Info.Status = "crashloop"
		p.mu.Unlock()
		s.log.Error("process exceeded MaxRestarts — entering crashloop", "id", p.spec.ID, "restarts", restarts, "max", maxRestarts)
		return
	}

	// Exponential backoff up to 30s. Each consecutive failure doubles.
	wait := backoff
	for i := 0; i < restarts; i++ {
		wait *= 2
		if wait > 30*time.Second {
			wait = 30 * time.Second
			break
		}
	}
	s.log.Warn("process exited abnormally — restarting", "id", p.spec.ID, "wait", wait, "attempt", restarts+1)
	time.Sleep(wait)

	// Was the process Stopped during the backoff sleep?
	p.mu.Lock()
	if p.Info.Status == "stopped" {
		p.mu.Unlock()
		return
	}
	p.Info.Restarts = restarts + 1
	p.mu.Unlock()

	// Re-launch. launchProc spawns its own waitExit goroutine on success.
	if err := s.launchProc(context.Background(), p); err != nil {
		s.log.Error("restart failed", "id", p.spec.ID, "err", err)
		// waitExit recurses via launchProc's goroutine, so a re-launch failure
		// here means we don't get another chance until external action.
		p.mu.Lock()
		p.Info.Status = "failed"
		p.Info.ExitErr = "restart failed: " + err.Error()
		p.mu.Unlock()
	}
}

// waitReady polls the given host:port via TCP until it accepts a connection
// or the timeout expires.
func waitReady(ctx context.Context, host string, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s not reachable after %s", addr, timeout)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// ---- ring buffer for log lines ----

type ringBuffer struct {
	mu   sync.Mutex
	buf  []string
	pos  int
	full bool
	cap  int
}

func newRingBuffer(capacity int) *ringBuffer {
	return &ringBuffer{cap: capacity, buf: make([]string, capacity)}
}

func (r *ringBuffer) append(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.pos] = line
	r.pos = (r.pos + 1) % r.cap
	if r.pos == 0 {
		r.full = true
	}
}

func (r *ringBuffer) tail(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full && r.pos == 0 {
		return nil
	}
	size := r.pos
	if r.full {
		size = r.cap
	}
	if n <= 0 || n > size {
		n = size
	}
	out := make([]string, 0, n)
	for i := size - n; i < size; i++ {
		idx := (r.pos - size + i + r.cap) % r.cap
		out = append(out, r.buf[idx])
	}
	return out
}

func readLines(rc io.ReadCloser, dst *ringBuffer) {
	defer rc.Close()
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		dst.append(sc.Text())
	}
}
