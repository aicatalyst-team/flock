// Process supervisor. Used on both leader (to launch the coordinator
// llama-server for sharded models) and workers (to launch rpc-server per
// shard). Wraps os/exec with start/stop/list/logs + a TCP-port readiness
// probe so callers can wait until a launched process is actually serving.
//
// v0.4 scope: no automatic restart on crash, no resource limits, no
// process namespacing. The orchestrator marks failed shards and the
// admin re-runs `flock model add`. Restart-on-crash is the obvious
// follow-up.
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
	"time"
)

// ProcessSpec describes a child process to launch.
type ProcessSpec struct {
	ID         string            // caller-assigned unique id
	Command    string            // absolute path or PATH-resolvable binary
	Args       []string
	Env        map[string]string // appended to os.Environ()
	WorkDir    string            // optional; defaults to CWD
	HealthPort int               // optional; if >0, waitReady probes TCP this port
	HealthHost string            // default "127.0.0.1"
	LogLines   int               // ring-buffer capacity (default 200)
}

// ProcessInfo is the observable state of a managed process.
type ProcessInfo struct {
	ID        string    `json:"id"`
	Command   string    `json:"command"`
	Args      []string  `json:"args"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	Status    string    `json:"status"` // starting | running | stopped | failed
	ExitErr   string    `json:"exit_err,omitempty"`
	Address   string    `json:"address,omitempty"`
}

type Process struct {
	Info     ProcessInfo
	spec     ProcessSpec
	cmd      *exec.Cmd
	cancel   context.CancelFunc
	logBuf   *ringBuffer
	mu       sync.RWMutex
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

	s.mu.Lock()
	if _, ok := s.procs[spec.ID]; ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("process %q already exists", spec.ID)
	}
	s.mu.Unlock()

	procCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, spec.Command, spec.Args...)
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
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	logBuf := newRingBuffer(spec.LogLines)
	go readLines(stdout, logBuf)
	go readLines(stderr, logBuf)

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start %s: %w", filepath.Base(spec.Command), err)
	}

	addr := ""
	if spec.HealthPort > 0 {
		addr = net.JoinHostPort(spec.HealthHost, strconv.Itoa(spec.HealthPort))
	}

	p := &Process{
		Info: ProcessInfo{
			ID:        spec.ID,
			Command:   spec.Command,
			Args:      spec.Args,
			PID:       cmd.Process.Pid,
			StartedAt: time.Now(),
			Status:    "starting",
			Address:   addr,
		},
		spec:   spec,
		cmd:    cmd,
		cancel: cancel,
		logBuf: logBuf,
	}

	s.mu.Lock()
	s.procs[spec.ID] = p
	s.mu.Unlock()

	// Wait for the process to either become reachable on its health port,
	// or fail. Done in foreground because callers want to know when their
	// rpc-server is ready before launching the coordinator.
	if spec.HealthPort > 0 {
		if err := waitReady(ctx, spec.HealthHost, spec.HealthPort, 30*time.Second); err != nil {
			s.markFailed(p, fmt.Errorf("health probe: %w", err))
			return &p.Info, fmt.Errorf("process did not become ready: %w", err)
		}
	}

	p.mu.Lock()
	p.Info.Status = "running"
	p.mu.Unlock()

	// Background waiter: capture exit status.
	go s.waitExit(p)

	s.log.Info("process started", "id", spec.ID, "pid", p.Info.PID, "command", spec.Command, "addr", addr)
	return s.snapshot(p), nil
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
	_ = p.cmd.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() {
		_ = p.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = p.cmd.Process.Kill()
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
	defer p.mu.Unlock()
	if p.Info.Status == "stopped" {
		return // we initiated this
	}
	if err != nil {
		p.Info.Status = "failed"
		p.Info.ExitErr = err.Error()
	} else {
		p.Info.Status = "stopped"
	}
	s.log.Info("process exited", "id", p.spec.ID, "pid", p.Info.PID, "status", p.Info.Status, "err", err)
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
	mu    sync.Mutex
	buf   []string
	pos   int
	full  bool
	cap   int
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
