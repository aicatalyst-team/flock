package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/hadihonarvar/flock/internal/agent"
	"github.com/hadihonarvar/flock/internal/auth"
	"github.com/hadihonarvar/flock/internal/config"
	"github.com/hadihonarvar/flock/internal/controlplane"
	"github.com/hadihonarvar/flock/internal/engines"
	"github.com/hadihonarvar/flock/internal/models"
	"github.com/hadihonarvar/flock/internal/scheduler"
	"github.com/hadihonarvar/flock/internal/store"
)

func cmdUp(args []string) {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml (default: ~/.flock/config.yaml)")
	autoPull := fs.Bool("auto-pull", true, "auto-pull the default model on first run")
	fs.Usage = func() {
		showHelp(helpSpec{
			name:    "up",
			summary: "start the local node (becomes the cluster leader on first run)",
			usage:   "flock up [--config <path>] [--auto-pull=false]",
			flags:   fs,
			examples: []string{
				"flock up",
				"FLOCK_DEFAULT_MODEL=llama-3.2-1b flock up",
				"FLOCK_ENGINE=llamacpp flock up           # auto-spawns llama-server if not already running",
				"flock up --config ~/.flock/staging.yaml",
				"flock up --auto-pull=false              # don't pre-pull the default model",
			},
			notes: []string{
				"On first run, prints an admin API key — save it. Subsequent runs reuse the saved key.",
				"When engine.preferred=llamacpp and no llama-server is listening on engine.llamacpp_endpoint, Flock auto-launches `llama-server -hf <repo>` for the default model (if its catalog entry has source.repo set) and stops it again on shutdown.",
			},
		})
	}
	if wantsHelp(args) {
		fs.Usage()
	}
	_ = fs.Parse(args)

	// 1. Config + logger
	cfg, err := config.Load(*configPath)
	if err != nil {
		die("config: %v", err)
	}
	log := newLogger(cfg)

	// 2. Hardware detection
	caps := agent.Detect()
	note(os.Stdout, "detected %s/%s · %d GB RAM · %d cores",
		caps.OS, caps.Arch, caps.RAMGB, caps.CPUCores)

	// 3. Store
	st := openStoreOrExit(cfg)
	defer st.Close()

	// 4. Bootstrap admin key on first run
	plainKey := bootstrapAdminKey(st, cfg)

	// 5. Catalog + auto-pick default model
	cat := loadCatalogOrExit(cfg)
	if cfg.Router.DefaultModel == "" {
		if pick, found := models.AutoPick(cat, caps, 4); found {
			cfg.Router.DefaultModel = pick.ID
			ok(os.Stdout, "auto-selected model: %s (%s)", pick.ID, pick.DisplayName)
		} else {
			warn(os.Stdout, "no catalog entry fits this hardware; set router.default_model in config")
		}
	} else {
		ok(os.Stdout, "default model: %s", cfg.Router.DefaultModel)
	}

	// 6. Process supervisor — created early so it can also auto-spawn
	//    llama-server below when engine.preferred=llamacpp. Used later by
	//    the sharding orchestrator for the coordinator on sharded models.
	sup := agent.NewSupervisor(log)
	defer sup.StopAll()

	// 7. Engine — bounded health probe so a wedged-but-listening engine
	//    doesn't block startup indefinitely.
	eng := newEngineFromConfig(cfg)
	healthCtx, healthCancel := context.WithTimeout(context.Background(), 3*time.Second)
	engineOK := eng.Health(healthCtx) == nil
	healthCancel()

	// 7a. Auto-spawn llama-server when the user picked the llamacpp engine
	//     and there's nothing listening yet. Keeps the UX symmetric with
	//     `ollama serve` running in the background. Skipped for the other
	//     engines for now — vLLM and MLX-LM both have heavier launch
	//     surfaces (Python deps, GPU allocation flags) that warrant explicit
	//     user control.
	if !engineOK && isLlamaCppEngine(eng.Name()) && cfg.Router.DefaultModel != "" {
		if entry := models.FindByID(cat, cfg.Router.DefaultModel); entry != nil {
			port := parseEndpointPort(cfg.Engine.LlamaCppEndpoint)
			spawnCtx, spawnCancel := context.WithTimeout(context.Background(), 5*time.Minute)
			note(os.Stdout, "auto-spawning llama-server for %s on :%d ...", entry.ID, port)
			_, err := scheduler.EnsureLlamaServer(spawnCtx, sup, log, scheduler.LlamaCppLaunchSpec{
				Entry: entry, Port: port,
			})
			spawnCancel()
			if err != nil {
				warn(os.Stdout, "auto-spawn failed: %v", err)
			} else {
				reprobeCtx, reprobeCancel := context.WithTimeout(context.Background(), 10*time.Second)
				engineOK = eng.Health(reprobeCtx) == nil
				reprobeCancel()
				if engineOK {
					ok(os.Stdout, "llama-server ready (auto-spawned)")
				}
			}
		}
	}

	// Register the leader as a "local" Node row so `flock node ls` and the
	// admin UI show this machine alongside any joined workers. Best-effort.
	{
		listen := cfg.Listen
		if listen == "" {
			listen = ":8080"
		}
		hwJSON, _ := json.Marshal(caps)
		_ = st.Nodes().Upsert(context.Background(), store.Node{
			ID:            "local",
			Hostname:      caps.Hostname,
			OS:            caps.OS,
			Arch:          caps.Arch,
			RAMGB:         caps.RAMGB,
			Address:       "127.0.0.1" + listen,
			HardwareJSON:  string(hwJSON),
			LastHeartbeat: time.Now(),
			State:         "ready",
		})
	}

	// Sync any locally-loaded models into placements so the Router knows
	// the leader's own engine has them. Best-effort.
	if engineOK {
		listCtx, listCancel := context.WithTimeout(context.Background(), 3*time.Second)
		if loaded, lerr := eng.List(listCtx); lerr == nil {
			for _, m := range loaded {
				_ = st.Placements().Upsert(listCtx, store.Placement{
					NodeID: "local", ModelID: m, Status: "ready", LastSeen: time.Now(),
				})
			}
		}
		listCancel()
	}
	if !engineOK {
		warn(os.Stdout, "engine (%s) at %s is not reachable", eng.Name(), eng.Endpoint())
		warn(os.Stdout, "  → %s", engineStartHint(eng.Name()))
		warn(os.Stdout, "  then check `flock status`")
	} else {
		ok(os.Stdout, "engine: %s at %s", eng.Name(), eng.Endpoint())
		if *autoPull && cfg.Router.DefaultModel != "" {
			ensureDefaultModel(cfg, cat, st, eng)
		}
	}

	// 8. Persist PID
	if err := writePID(cfg); err != nil {
		warn(os.Stdout, "could not write PID file: %v", err)
	}
	defer removePID(cfg)

	// 9. Print ready block
	printReady(cfg, plainKey)

	// 10. Sharding orchestrator (uses the supervisor created at step 6;
	//     leader runs the coordinator llama-server for sharded models).
	orch := scheduler.New(st, sup, log)

	// 11. Start server with signal context
	srv := controlplane.NewServer(cfg, st, eng, cat, log, orch)
	srv.Version = version
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := srv.Start(ctx); err != nil {
		die("server: %v", err)
	}
	ok(os.Stdout, "shutdown complete")
}

func bootstrapAdminKey(st store.Store, cfg *config.Config) string {
	ctx := context.Background()
	keys, err := st.APIKeys().List(ctx)
	if err != nil {
		warn(os.Stdout, "could not list api keys (%v) — skipping bootstrap; check `flock token ls`", err)
		return ""
	}
	if len(keys) > 0 {
		return ""
	}
	plain, rec, err := auth.Generate("initial-admin", "admin", "admin")
	if err != nil {
		warn(os.Stdout, "could not bootstrap admin key: %v", err)
		return ""
	}
	if err := st.APIKeys().Create(ctx, rec); err != nil {
		warn(os.Stdout, "could not persist admin key: %v", err)
		return ""
	}
	// Save plaintext to ~/.flock/admin.key (mode 0600) so subsequent CLI
	// invocations on this host can authenticate to the leader without the
	// user having to remember the key. Same trust model as ~/.aws/credentials.
	path := localAdminKeyPath(cfg)
	if err := os.WriteFile(path, []byte(plain), 0o600); err != nil {
		warn(os.Stdout, "could not save admin key to %s: %v", path, err)
	}
	return plain
}

// ensureDefaultModel records the default model in the store and triggers a pull
// if the engine doesn't already have it. Best-effort; failures are logged but
// don't block startup.
func ensureDefaultModel(cfg *config.Config, cat []models.Entry, st store.Store, eng engines.Engine) {
	entry := models.FindByID(cat, cfg.Router.DefaultModel)
	if entry == nil {
		warn(os.Stdout, "default model %q not found in catalog", cfg.Router.DefaultModel)
		return
	}
	engineModelName := entry.Source.OllamaName
	if engineModelName == "" {
		engineModelName = entry.ID
	}
	// Already pulled?
	existing, _ := eng.List(context.Background())
	for _, m := range existing {
		if m == engineModelName {
			_ = st.Models().Upsert(context.Background(), store.Model{
				ID: entry.ID, CatalogID: entry.ID,
				Source: "ollama:" + engineModelName, Status: "ready",
				SizeBytes: entry.SizeBytes, InstalledAt: time.Now(),
			})
			return
		}
	}
	note(os.Stdout, "pulling %s ...", engineModelName)
	err := eng.Pull(context.Background(), engineModelName, func(status string, completed, total int64) {
		if total > 0 {
			pct := completed * 100 / total
			fmt.Printf("\r  %s %d%%  ", status, pct)
		}
	})
	fmt.Println()
	if err != nil {
		warn(os.Stdout, "pull failed: %v", err)
		return
	}
	_ = st.Models().Upsert(context.Background(), store.Model{
		ID: entry.ID, CatalogID: entry.ID,
		Source: "ollama:" + engineModelName, Status: "ready",
		SizeBytes: entry.SizeBytes, InstalledAt: time.Now(),
	})
	ok(os.Stdout, "model ready: %s", entry.ID)
}

func printReady(cfg *config.Config, adminKey string) {
	listen := cfg.Listen
	if listen == "" {
		listen = ":8080"
	}
	base := "http://localhost" + listen
	if cfg.ExternalURL != "" {
		base = cfg.ExternalURL
	}
	fmt.Println()
	fmt.Println("  Flock is ready.")
	fmt.Println()
	fmt.Printf("  Dashboard:  %s\n", base)
	fmt.Printf("  API:        %s/v1\n", base)
	fmt.Printf("  Health:     %s/healthz\n", base)
	if adminKey != "" {
		// First run: a brand-new admin key was generated. Walk the operator
		// through every next step so they don't have to dig through README.
		fmt.Println()
		fmt.Println("  Admin API key (shown once — store it now):")
		fmt.Printf("    %s\n", adminKey)
		fmt.Println()
		fmt.Println("  Next steps:")
		fmt.Printf("    →  Test in the browser:  %s\n", base)
		fmt.Println("    →  Wire up Claude Code:  flock connect claude-code")
		fmt.Println("    →  Wire up Cursor:       flock connect cursor")
		fmt.Println("    →  See all clients:      flock connect --list")
		fmt.Println("    →  Invite a teammate:    flock invite <name>")
		fmt.Println()
		fmt.Println("  Quick test from the shell:")
		fmt.Printf("    curl %s/v1/chat/completions \\\n", base)
		fmt.Printf("      -H 'Authorization: Bearer %s' \\\n", adminKey)
		fmt.Println(`      -H 'Content-Type: application/json' \`)
		fmt.Printf("      -d '{\"model\":\"%s\",\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}]}'\n", cfg.Router.DefaultModel)
	} else {
		// Returning user: admin key already on disk, no need to repeat it
		// or the quick-test curl. Just nudge them at the dashboard.
		fmt.Println()
		fmt.Println("  Wire up a tool:")
		fmt.Println("    flock connect claude-code   # or: cursor, aider, continue, …")
		fmt.Println("    flock connect --list        # see all supported clients")
	}
	MaybeShowUpdateNotice(os.Stdout)
	printNetworkPosture(cfg)
	fmt.Println()
	fmt.Println("  Press Ctrl-C to stop.")
	fmt.Println()
}

// printNetworkPosture lists every outbound network call Flock can make
// from this process. Designed so an operator running `flock up` can audit
// the privacy posture at a glance — no surprises, no silent calls. Reads
// the live config so it reflects what *this* invocation will actually do.
func printNetworkPosture(cfg *config.Config) {
	fmt.Println()
	fmt.Println("  Network behavior on this node:")

	// Tracing
	if cfg.Observability.OTLPEndpoint == "" {
		fmt.Println("    · Tracing:       OFF  (set FLOCK_OTLP_ENDPOINT=… to your collector to enable)")
	} else {
		fmt.Printf("    · Tracing:       → %s  (your collector; set OFF by clearing FLOCK_OTLP_ENDPOINT)\n", cfg.Observability.OTLPEndpoint)
	}

	// Update check
	if os.Getenv("FLOCK_NO_UPDATE_CHECK") == "1" {
		fmt.Println("    · Update check:  OFF  (FLOCK_NO_UPDATE_CHECK=1)")
	} else {
		fmt.Println("    · Update check:  github.com/hadihonarvar/flock/releases/latest, max 1× per 24h  (FLOCK_NO_UPDATE_CHECK=1 to disable)")
	}

	// Vendor egress — only print rows where the operator opted in.
	if cfg.Router.Fallback.AnthropicKey != "" {
		fmt.Println("    · Anthropic:     → api.anthropic.com on claude-* requests  (ANTHROPIC_API_KEY set)")
	}
	if cfg.Router.Fallback.OpenAIKey != "" {
		fmt.Println("    · OpenAI:        → api.openai.com on gpt-*/o-* requests  (OPENAI_API_KEY set)")
	}
	if cfg.Router.Fallback.BedrockRegion != "" {
		fmt.Printf("    · Bedrock:       → bedrock-runtime.%s.amazonaws.com on anthropic.* (SigV4 via AWS chain)\n", cfg.Router.Fallback.BedrockRegion)
	}
	if cfg.Router.Fallback.VertexProject != "" {
		fmt.Printf("    · Vertex:        → %s-aiplatform.googleapis.com (ADC for project %s)\n", orDefault(cfg.Router.Fallback.VertexLocation, "us-central1"), cfg.Router.Fallback.VertexProject)
	}

	fmt.Println("    · Telemetry:     none. Flock never reports installs, usage, errors, or any data to flockllm.com.")
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// isLlamaCppEngine matches every alias the engine registry accepts for
// the llama.cpp driver. Mirrors the cases in newEngineFromConfig +
// internal/engines/registry.go so the auto-spawn trigger stays in sync.
func isLlamaCppEngine(name string) bool {
	switch name {
	case "llamacpp", "llama-cpp", "llamacpp-rpc":
		return true
	}
	return false
}

// parseEndpointPort extracts the port from "http://host:port" / "host:port".
// Returns 0 when the input lacks an explicit port — callers must error out
// before passing 0 to the supervisor.
func parseEndpointPort(endpoint string) int {
	if u, err := url.Parse(endpoint); err == nil && u.Port() != "" {
		if p, err := strconv.Atoi(u.Port()); err == nil {
			return p
		}
	}
	return 0
}

// engineStartHint returns the copy-pasteable command that brings the
// configured engine up. Used by `flock up` and `flock doctor` when the
// engine health probe fails.
func engineStartHint(engineName string) string {
	switch engineName {
	case "ollama":
		return "start it with: ollama serve"
	case "vllm":
		return "start vLLM (see https://docs.vllm.ai/) and ensure FLOCK_VLLM_ENDPOINT matches"
	case "mlx", "mlx-lm":
		return "start MLX-LM: mlx_lm.server --port 8080"
	case "llamacpp", "llama-cpp", "llamacpp-rpc":
		return "start llama.cpp: llama-server -m /path/to/model.gguf --port 8089"
	default:
		return "start the configured engine, then check `flock status`"
	}
}
