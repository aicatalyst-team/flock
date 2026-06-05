package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hadihonarvar/flock/internal/agent"
	"github.com/hadihonarvar/flock/internal/mesh"

	"gopkg.in/yaml.v3"
)

// cmdJoin connects this machine to an existing Flock cluster as a worker.
//
// Usage:
//
//	flock join http://leader:8080?token=sk-orc-...
func cmdJoin(args []string) {
	if len(args) == 0 {
		die("usage: flock join <leader-url>?token=<token>")
	}
	leader, token, err := parseJoinTarget(args[0])
	if err != nil {
		die("%v", err)
	}

	cfg := loadConfigOrExit()
	log := newLogger(cfg)

	caps := agent.Detect()
	addr, err := mesh.NewLAN().Address(8081) // workers default to :8081
	if err != nil {
		warn(os.Stdout, "could not determine local address: %v", err)
		addr = "0.0.0.0:8081"
	}
	nodeID := generateNodeID()

	// Persist node config so a subsequent `flock up` enters worker mode.
	nodeCfg := NodeConfig{
		NodeID:    nodeID,
		LeaderURL: leader,
		Token:     token,
		Address:   addr,
		JoinedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	if err := nodeCfg.Save(filepath.Join(cfg.DataDir, "node.yaml")); err != nil {
		warn(os.Stdout, "could not persist node config: %v", err)
	}

	// Local engine — the worker proxies its inference requests to this.
	eng := newEngineFromConfig(cfg)

	a := &agent.Agent{
		NodeID:            nodeID,
		LeaderURL:         leader,
		Token:             token,
		Address:           addr,
		Capabilities:      caps,
		Engine:            eng,
		HTTP:              &http.Client{Timeout: 10 * time.Second},
		HeartbeatInterval: 5 * time.Second,
		Log:               log,
	}

	// Worker HTTP server — leader will call into here for inference AND
	// for launching/stopping shard processes (rpc-server etc).
	sup := agent.NewSupervisor(log)
	srv := &agent.Server{Engine: eng, Token: token, Supervisor: sup}
	defer sup.StopAll()

	ok(os.Stdout, "joining cluster at %s as %s", leader, nodeID)
	note(os.Stdout, "address: %s", addr)
	note(os.Stdout, "hardware: %s/%s · %d GB RAM", caps.OS, caps.Arch, caps.RAMGB)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)

	// Heartbeat loop
	go func() {
		defer wg.Done()
		if err := a.Loop(ctx); err != nil && err != context.Canceled {
			log.Error("agent loop exited", "err", err)
			cancel()
		}
	}()

	// Worker HTTP server (binds to the tailnet/LAN address from mesh)
	go func() {
		defer wg.Done()
		if err := srv.Start(ctx, addr); err != nil && err != context.Canceled {
			log.Error("worker server exited", "err", err)
			cancel()
		}
	}()

	wg.Wait()
	ok(os.Stdout, "worker shutdown complete")
}

func parseJoinTarget(raw string) (leader, token string, err error) {
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("invalid URL: %w", err)
	}
	q := u.Query()
	token = q.Get("token")
	if token == "" {
		return "", "", fmt.Errorf("token query param required")
	}
	u.RawQuery = ""
	leader = strings.TrimRight(u.String(), "/")
	return leader, token, nil
}

func generateNodeID() string {
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	return "n_" + base64.RawURLEncoding.EncodeToString(buf)
}

// NodeConfig is the per-worker config saved at ~/.flock/node.yaml.
type NodeConfig struct {
	NodeID    string `yaml:"node_id"`
	LeaderURL string `yaml:"leader_url"`
	Token     string `yaml:"token"`
	Address   string `yaml:"address"`
	JoinedAt  string `yaml:"joined_at"`
}

func (n *NodeConfig) Save(path string) error {
	data, err := yaml.Marshal(n)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// loadNodeConfig is intentionally unexported and currently unused; it's the
// piece a future "worker resumes from saved state on `flock up`" feature
// would call. Marked with `var _` so go vet / unused linters don't object.
//
//nolint:unused
func loadNodeConfig(path string) (*NodeConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var n NodeConfig
	if err := yaml.Unmarshal(data, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

var _ = loadNodeConfig // reserve for v0.5 worker resume
