package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
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

	// 6. Engine — bounded health probe so a wedged-but-listening engine
	//    doesn't block startup indefinitely.
	eng := newEngineFromConfig(cfg)
	healthCtx, healthCancel := context.WithTimeout(context.Background(), 3*time.Second)
	engineOK := eng.Health(healthCtx) == nil
	healthCancel()

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
		warn(os.Stdout, "start Ollama with `ollama serve` then check `flock status`")
	} else {
		ok(os.Stdout, "engine: %s at %s", eng.Name(), eng.Endpoint())
		if *autoPull && cfg.Router.DefaultModel != "" {
			ensureDefaultModel(cfg, cat, st, eng)
		}
	}

	// 7. Persist PID
	if err := writePID(cfg); err != nil {
		warn(os.Stdout, "could not write PID file: %v", err)
	}
	defer removePID(cfg)

	// 8. Print ready block
	printReady(cfg, plainKey)

	// 9. Process supervisor + sharding orchestrator (leader runs coordinator
	//    llama-server processes locally for sharded models).
	sup := agent.NewSupervisor(log)
	defer sup.StopAll()
	orch := scheduler.New(st, sup, log)

	// 10. Start server with signal context
	srv := controlplane.NewServer(cfg, st, eng, cat, log, orch)
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
	fmt.Printf("  API:    %s/v1\n", base)
	fmt.Printf("  Health: %s/healthz\n", base)
	if adminKey != "" {
		fmt.Println()
		fmt.Println("  Admin API key (shown once — store it now):")
		fmt.Printf("    %s\n", adminKey)
		fmt.Println()
		fmt.Println("  Try it:")
		fmt.Printf("    curl %s/v1/chat/completions \\\n", base)
		fmt.Printf("      -H 'Authorization: Bearer %s' \\\n", adminKey)
		fmt.Println(`      -H 'Content-Type: application/json' \`)
		fmt.Printf("      -d '{\"model\":\"%s\",\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}]}'\n", cfg.Router.DefaultModel)
	}
	fmt.Println()
	fmt.Println("  Press Ctrl-C to stop.")
	fmt.Println()
}
