package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"syscall"
	"time"

	"github.com/hadihonarvar/flock/internal/engines"
	"github.com/hadihonarvar/flock/internal/lifecycle"
)

func cmdDown(args []string) {
	fs := flag.NewFlagSet("down", flag.ExitOnError)
	noUnload := fs.Bool("no-unload", false, "leave models resident in the engine's memory (skip the default unload)")
	fs.Usage = func() {
		showHelp(helpSpec{
			name:    "down",
			summary: "stop the local flock node and release engine memory",
			usage:   "flock down [--no-unload]",
			flags:   fs,
			examples: []string{
				"flock down              # stop + unload models from engine RAM",
				"flock down --no-unload  # stop only; models stay resident (Ollama TTL applies)",
			},
			notes: []string{
				"`down` is a deliberate teardown, so it unloads resident models by default.",
				"Ctrl-C of `flock up` does NOT unload (fast dev restarts) — use --unload-on-exit there.",
				"Engines without an unload protocol (vLLM, MLX-LM) are skipped with a note; Flock-spawned llama-server processes are killed by `flock up`'s own shutdown.",
			},
		})
	}
	if wantsHelp(args) {
		fs.Usage()
	}
	_ = fs.Parse(args)

	cfg := loadConfigOrExit()
	pid, err := readPID(cfg)
	if err != nil {
		die("no PID file at %s (is flock running?)", pidFilePath(cfg))
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		die("find process %d: %v", pid, err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		die("signal pid %d: %v", pid, err)
	}
	ok(os.Stdout, "sent SIGTERM to pid %d", pid)

	// Wait for the server to actually exit (signal 0 probes liveness) so
	// the unload below doesn't race its graceful shutdown.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			break // process gone
		}
		time.Sleep(200 * time.Millisecond)
	}

	if *noUnload {
		return
	}
	// Deliberate teardown defaults to releasing engine memory. The engine
	// (e.g. the Ollama daemon) outlives the flock process, so this must
	// happen client-side after the server is down.
	eng := newEngineFromConfig(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := eng.Health(ctx); err != nil {
		// No engine running → nothing resident; stay quiet.
		return
	}
	mgr := &lifecycle.Manager{Engine: eng, Log: slog.Default()}
	n, err := mgr.UnloadAll(ctx)
	switch {
	case errors.Is(err, engines.ErrUnloadNotSupported):
		note(os.Stdout, "%s has no unload protocol — restart it to free RAM (Flock-spawned engines are already stopped)", eng.Name())
	case err != nil:
		warn(os.Stdout, "unload: %v (unloaded %d)", err, n)
	case n > 0:
		ok(os.Stdout, "unloaded %d model(s) from %s", n, eng.Name())
	}
}
