// Single-node llama-server auto-launch. Lets `flock up` spawn a local
// `llama-server` process via the same Supervisor that already runs the
// sharding coordinator — so a user with engine.preferred=llamacpp doesn't
// have to start the engine binary manually before `flock up`.
//
// This is the non-RPC counterpart to the coordinator launch in
// sharding.go: identical ProcessSpec shape, just without `--rpc`.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"time"

	"github.com/hadihonarvar/flock/internal/agent"
	"github.com/hadihonarvar/flock/internal/models"
)

// LlamaCppLaunchSpec describes how to launch a single-node llama-server.
type LlamaCppLaunchSpec struct {
	// Entry is the catalog entry to serve. Source.Repo is preferred (uses
	// llama-server's -hf flag, downloads via the HF cache); Source.Path is
	// used as a fallback for already-downloaded GGUF files. If both are
	// empty EnsureLlamaServer returns an error rather than guessing.
	Entry *models.Entry
	// Port is where llama-server will listen (matches engine.llamacpp_endpoint).
	Port int
	// CtxSize, if >0, is passed as --ctx-size. Defaults to llama-server's own.
	CtxSize int
	// ExtraArgs are appended verbatim — e.g. ["-ngl", "999"] for full GPU offload.
	ExtraArgs []string
}

// LlamaCppProcessID returns the supervisor process id used for an entry.
// Exported so callers can Stop or look up the process later if needed.
func LlamaCppProcessID(entryID string) string {
	return "llamacpp-" + safeID(entryID)
}

// EnsureLlamaServer launches a llama-server for spec.Entry via sup and
// waits for the port to accept TCP. Idempotent: if the supervisor already
// has a process under the same id, returns its existing ProcessInfo.
// The supervisor's StopAll (deferred in cmd_up) tears the process down on
// flock shutdown.
func EnsureLlamaServer(ctx context.Context, sup *agent.Supervisor, log *slog.Logger, spec LlamaCppLaunchSpec) (*agent.ProcessInfo, error) {
	if spec.Entry == nil {
		return nil, fmt.Errorf("llamacpp auto-spawn: nil catalog entry")
	}
	if spec.Port <= 0 {
		return nil, fmt.Errorf("llamacpp auto-spawn: port must be > 0")
	}
	if _, err := exec.LookPath("llama-server"); err != nil {
		return nil, fmt.Errorf("llama-server not found in PATH (install: brew install llama.cpp)")
	}

	procID := LlamaCppProcessID(spec.Entry.ID)
	if existing, ok := sup.Get(procID); ok && existing.Status == "running" {
		return existing, nil
	}

	args := []string{}
	switch {
	case spec.Entry.Source.Repo != "":
		args = append(args, "-hf", spec.Entry.Source.Repo)
	case spec.Entry.Source.Path != "":
		args = append(args, "-m", spec.Entry.Source.Path)
	default:
		return nil, fmt.Errorf("catalog entry %s has no source.repo (HF GGUF) or source.path (local GGUF) for llama-server", spec.Entry.ID)
	}
	args = append(args,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(spec.Port),
	)
	if spec.CtxSize > 0 {
		args = append(args, "--ctx-size", strconv.Itoa(spec.CtxSize))
	}
	args = append(args, spec.ExtraArgs...)

	procSpec := agent.ProcessSpec{
		ID:             procID,
		Command:        "llama-server",
		Args:           args,
		HealthPort:     spec.Port,
		HealthHost:     "127.0.0.1",
		Restart:        true,
		MaxRestarts:    5,
		RestartBackoff: time.Second,
	}
	log.Info("auto-spawning llama-server", "model", spec.Entry.ID, "port", spec.Port)
	return sup.Start(ctx, procSpec)
}
