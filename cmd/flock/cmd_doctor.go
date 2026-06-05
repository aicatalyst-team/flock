package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/hadihonarvar/flock/internal/agent"
	"github.com/hadihonarvar/flock/internal/models"
)

func cmdDoctor(_ []string) {
	cfg := loadConfigOrExit()
	fmt.Println("Flock doctor")
	fmt.Println("============")

	// Hardware
	caps := agent.Detect()
	ok(os.Stdout, "hardware: %s/%s · %d cores · %d GB RAM",
		caps.OS, caps.Arch, caps.CPUCores, caps.RAMGB)
	for _, g := range caps.GPUs {
		ok(os.Stdout, "GPU: %s (%d GB)", g.Name, g.VRAMGB)
	}

	// Listen port
	addr := cfg.Listen
	if addr == "" {
		addr = ":8080"
	}
	if portAvailable(addr) {
		ok(os.Stdout, "listen port %s available", addr)
	} else {
		warn(os.Stdout, "listen port %s already in use", addr)
	}

	// Ollama
	if path, err := exec.LookPath("ollama"); err == nil {
		ok(os.Stdout, "ollama binary at %s", path)
	} else {
		warn(os.Stdout, "ollama not found in PATH — install: brew install ollama")
	}

	// Ollama daemon
	eng := newEngineFromConfig(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := eng.Health(ctx); err != nil {
		warn(os.Stdout, "ollama daemon not reachable: %v", err)
		warn(os.Stdout, "  → start it with: ollama serve")
	} else {
		ok(os.Stdout, "ollama daemon healthy at %s", eng.Endpoint())
	}

	// Data dir
	if _, err := os.Stat(cfg.DataDir); err == nil {
		ok(os.Stdout, "data dir: %s", cfg.DataDir)
	} else {
		warn(os.Stdout, "data dir missing: %s", cfg.DataDir)
	}

	// Catalog
	if entries, err := models.LoadCatalog(cfg.CatalogDir); err == nil {
		ok(os.Stdout, "catalog: %d entries", len(entries))
	} else {
		warn(os.Stdout, "catalog: %v", err)
	}

	fmt.Println()
}

func portAvailable(addr string) bool {
	if !strings.HasPrefix(addr, ":") {
		addr = ":" + addr
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}
