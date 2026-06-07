package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hadihonarvar/flock/internal/models"
	"github.com/hadihonarvar/flock/internal/store"
)

// truncStr is also defined in cmd_usage.go (this file uses it).
var _ = truncStr

func cmdModel(args []string) {
	help := helpSpec{
		name:    "model",
		summary: "install, list, search, inspect, or uninstall LLM models",
		usage:   "flock model <add <id> | ls | search [query] | info <id> | remove <id>>",
		examples: []string{
			"flock model search                # browse the full catalog",
			"flock model search coder          # filter to coding models",
			"flock model info qwen-coder-14b   # full details on one model",
			"flock model add llama-3.2-3b      # install (auto-delegates if sharded)",
			"flock model ls                    # list installed models",
			"flock model remove llama-3.2-3b",
		},
		notes: []string{
			"For sharded models (split across multiple machines) see `flock shard --help`.",
			"For the complete per-model walkthrough see MODELS.md in the repo.",
		},
	}
	if len(args) == 0 {
		dieHelp(help)
	}
	if wantsHelp(args) {
		showHelp(help)
	}
	switch args[0] {
	case "add":
		if len(args) < 2 {
			die("usage: flock model add <id> [--force]")
		}
		id, force := parseModelAddArgs(args[1:])
		modelAdd(id, force)
	case "ls", "list":
		modelLs()
	case "remove", "rm":
		if len(args) < 2 {
			die("usage: flock model remove <id>")
		}
		modelRemove(args[1])
	case "search":
		query := ""
		if len(args) > 1 {
			query = args[1]
		}
		modelSearch(query)
	case "info":
		if len(args) < 2 {
			die("usage: flock model info <id>")
		}
		modelInfo(args[1])
	default:
		die("unknown subcommand: model %s (run `flock model --help` for usage)", args[0])
	}
}

// parseModelAddArgs extracts the model id and the --force flag from the args
// passed after "model add". Order doesn't matter; `--force` may appear before
// or after the id.
func parseModelAddArgs(args []string) (id string, force bool) {
	for _, a := range args {
		if a == "--force" || a == "-force" {
			force = true
			continue
		}
		if id == "" {
			id = a
		}
	}
	return id, force
}

func modelAdd(id string, force bool) {
	if id == "" {
		die("usage: flock model add <id> [--force]")
	}
	cfg := loadConfigOrExit()
	cat := loadCatalogOrExit(cfg)
	entry := models.FindByID(cat, id)
	if entry == nil {
		die("no catalog entry for %q (try `flock model search`)", id)
	}

	// Pre-install hardware check — refuse if this machine clearly can't
	// run the model. Cheap to compute and saves the user a long failing
	// pull. Sharded entries are exempt (sharding is how you fit a model
	// that doesn't fit on any single node).
	if !entry.Sharding.Required {
		if msg := checkHardwareForModel(entry); msg != "" {
			if force {
				warn(os.Stdout, "%s — proceeding because --force was set", msg)
			} else {
				die("%s\n  (override with `flock model add %s --force` if you know what you're doing)", msg, id)
			}
		}
	}

	// Sharded model? Hand off to the shard orchestrator on the leader.
	if entry.Sharding.Required {
		note(os.Stdout, "%s requires sharding — delegating to `flock shard create`", id)
		shardCreate(id, 0)
		return
	}

	st := openStoreOrExit(cfg)
	defer st.Close()
	eng := newEngineFromConfig(cfg)

	// Pick the engine-native model name based on which engine we're using.
	engineName := engineNativeName(eng.Name(), entry)
	if engineName == "" {
		die("catalog entry %s has no source name compatible with engine %s", entry.ID, eng.Name())
	}

	if err := eng.Health(context.Background()); err != nil {
		die("engine not reachable (%v) — start it first", err)
	}
	note(os.Stdout, "pulling %s (%s via %s) ...", entry.ID, engineName, eng.Name())
	err := eng.Pull(context.Background(), engineName, func(status string, completed, total int64) {
		if total > 0 {
			pct := completed * 100 / total
			fmt.Printf("\r  %s %d%%  ", status, pct)
		}
	})
	fmt.Println()
	if err != nil {
		die("pull failed: %v", err)
	}
	_ = st.Models().Upsert(context.Background(), store.Model{
		ID:          entry.ID,
		CatalogID:   entry.ID,
		Source:      eng.Name() + ":" + engineName,
		Status:      "ready",
		SizeBytes:   entry.SizeBytes,
		InstalledAt: time.Now(),
	})
	// Mark this node's placement so the router can find it. On a worker
	// this row will be reconciled by the leader on the next heartbeat too.
	_ = st.Placements().Upsert(context.Background(), store.Placement{
		NodeID:   "local",
		ModelID:  engineName,
		Status:   "ready",
		LastSeen: time.Now(),
	})
	ok(os.Stdout, "installed: %s", entry.ID)
}

// engineNativeName picks the right field from the catalog source for a given
// engine. Returns "" if no compatible field is set.
func engineNativeName(engine string, e *models.Entry) string {
	switch engine {
	case "ollama":
		if e.Source.OllamaName != "" {
			return e.Source.OllamaName
		}
	case "vllm", "mlx", "mlx-lm":
		if e.Source.Repo != "" {
			return e.Source.Repo
		}
		if e.Source.Path != "" {
			return e.Source.Path
		}
	}
	return e.ID
}

func modelLs() {
	cfg := loadConfigOrExit()
	st := openStoreOrExit(cfg)
	defer st.Close()
	ms, err := st.Models().List(context.Background())
	if err != nil {
		die("list models: %v", err)
	}
	if len(ms) == 0 {
		fmt.Println("(no models installed — try `flock model add llama-3.2-3b`)")
		return
	}
	fmt.Printf("%-22s %-10s %-30s %s\n", "ID", "STATUS", "SOURCE", "INSTALLED")
	for _, m := range ms {
		fmt.Printf("%-22s %-10s %-30s %s\n", m.CatalogID, m.Status, m.Source, m.InstalledAt.Format(time.RFC3339))
	}
}

func modelRemove(id string) {
	cfg := loadConfigOrExit()
	st := openStoreOrExit(cfg)
	defer st.Close()
	m, err := st.Models().Get(context.Background(), id)
	if err != nil {
		die("get model: %v", err)
	}
	if m == nil {
		die("no such model: %s", id)
	}
	eng := newEngineFromConfig(cfg)
	// Source format is "<engine_name>:<model_name>" — strip the first prefix.
	engineName := id
	if idx := strings.Index(m.Source, ":"); idx >= 0 && idx < len(m.Source)-1 {
		engineName = m.Source[idx+1:]
	}
	if err := eng.Delete(context.Background(), engineName); err != nil {
		warn(os.Stdout, "engine delete failed (continuing): %v", err)
	}
	if err := st.Models().Delete(context.Background(), id); err != nil {
		die("store delete: %v", err)
	}
	ok(os.Stdout, "removed: %s", id)
}

func modelSearch(query string) {
	cfg := loadConfigOrExit()
	cat := loadCatalogOrExit(cfg)
	q := strings.ToLower(query)

	// Find which models are already installed so we can mark them ✓
	installed := map[string]bool{}
	if st, err := store.OpenSQLite(cfg.Storage.DSN); err == nil {
		defer st.Close()
		if ms, err := st.Models().List(context.Background()); err == nil {
			for _, m := range ms {
				installed[m.CatalogID] = true
			}
		}
	}

	fmt.Printf("%-26s %-32s %7s %5s %-22s %s\n", "ID", "NAME", "SIZE", "RAM", "CAPABILITIES", "INSTALLED")
	for _, e := range cat {
		if q != "" &&
			!strings.Contains(strings.ToLower(e.ID), q) &&
			!strings.Contains(strings.ToLower(e.DisplayName), q) &&
			!containsAny(e.Capabilities, q) &&
			!containsAny(e.Tags, q) {
			continue
		}
		size := "?"
		if e.SizeBytes > 0 {
			size = fmt.Sprintf("%.1f GB", float64(e.SizeBytes)/1e9)
		}
		caps := strings.Join(e.Capabilities, ",")
		if len(caps) > 22 {
			caps = caps[:21] + "…"
		}
		mark := ""
		if installed[e.ID] {
			mark = "✓"
		}
		fmt.Printf("%-26s %-32s %7s %4dG %-22s %s\n",
			e.ID, truncStr(e.DisplayName, 32), size, e.Hardware.MinRAMGB, caps, mark)
	}
	fmt.Println()
	fmt.Println("Tip: `flock model info <id>` for full details on one model. `flock model add <id>` to install.")
}

func containsAny(items []string, q string) bool {
	for _, it := range items {
		if strings.Contains(strings.ToLower(it), q) {
			return true
		}
	}
	return false
}

// modelInfo prints the full metadata for a single catalog model + whether
// it's installed locally + ready-to-paste usage snippets. Matches the kind
// of info in MODELS.md but condensed for terminal display.
func modelInfo(id string) {
	cfg := loadConfigOrExit()
	cat := loadCatalogOrExit(cfg)
	entry := models.FindByID(cat, id)
	if entry == nil {
		die("no catalog entry for %q (try `flock model search`)", id)
	}

	// Check installed state
	st, _ := store.OpenSQLite(cfg.Storage.DSN)
	var installedRow *store.Model
	if st != nil {
		defer st.Close()
		installedRow, _ = st.Models().Get(context.Background(), id)
	}

	bold := "\033[1m"
	dim := "\033[2m"
	reset := "\033[0m"
	if os.Getenv("NO_COLOR") != "" {
		bold, dim, reset = "", "", ""
	}

	fmt.Printf("%s%s%s — %s\n", bold, entry.ID, reset, entry.DisplayName)
	fmt.Println()

	// Status
	status := dim + "not installed" + reset
	if installedRow != nil {
		status = "installed · " + installedRow.Status + dim + " (" + installedRow.InstalledAt.Format("2006-01-02") + ")" + reset
	}
	fmt.Printf("  %sStatus%s         %s\n", bold, reset, status)

	// Source
	source := "—"
	switch entry.Source.Type {
	case "ollama":
		source = "ollama: " + entry.Source.OllamaName
	case "huggingface":
		source = "huggingface: " + entry.Source.Repo
		if entry.Source.File != "" {
			source += " (" + entry.Source.File + ")"
		}
	case "file":
		source = "file: " + entry.Source.Path
	}
	fmt.Printf("  %sSource%s         %s\n", bold, reset, source)

	// Size
	if entry.SizeBytes > 0 {
		fmt.Printf("  %sSize%s           %.1f GB\n", bold, reset, float64(entry.SizeBytes)/1e9)
	}
	if entry.Quant != "" {
		fmt.Printf("  %sQuant%s          %s\n", bold, reset, entry.Quant)
	}
	if entry.ContextWindow > 0 {
		fmt.Printf("  %sContext window%s %d tokens\n", bold, reset, entry.ContextWindow)
	}
	fmt.Printf("  %sMin RAM%s        %d GB\n", bold, reset, entry.Hardware.MinRAMGB)
	if entry.Hardware.MinVRAMGB > 0 {
		fmt.Printf("  %sMin VRAM%s       %d GB\n", bold, reset, entry.Hardware.MinVRAMGB)
	}
	if len(entry.Capabilities) > 0 {
		fmt.Printf("  %sCapabilities%s   %s\n", bold, reset, strings.Join(entry.Capabilities, ", "))
	}
	if len(entry.RecommendedEngines) > 0 {
		fmt.Printf("  %sEngines%s        %s\n", bold, reset, strings.Join(entry.RecommendedEngines, ", "))
	}
	if len(entry.Tags) > 0 {
		fmt.Printf("  %sTags%s           %s\n", bold, reset, strings.Join(entry.Tags, ", "))
	}
	if entry.Sharding.Required {
		fmt.Printf("  %sSharding%s       required (default %d shards, %s engine)\n",
			bold, reset, entry.Sharding.DefaultShards, entry.Sharding.Engine)
	}

	// Install + usage snippets
	fmt.Println()
	fmt.Printf("%sInstall%s\n", bold, reset)
	if entry.Sharding.Required {
		fmt.Printf("  flock shard create %s %d\n", entry.ID, max2(entry.Sharding.DefaultShards))
	} else {
		fmt.Printf("  flock model add %s\n", entry.ID)
	}

	fmt.Println()
	fmt.Printf("%sUse via API (OpenAI shape)%s\n", bold, reset)
	fmt.Printf("  curl http://localhost:8080/v1/chat/completions \\\n")
	fmt.Printf("    -H 'Authorization: Bearer sk-orc-...' \\\n")
	fmt.Printf("    -d '{\"model\":\"%s\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}'\n", entry.ID)
	fmt.Println()
	fmt.Printf("%sUse via Claude Code%s\n", bold, reset)
	fmt.Printf("  export ANTHROPIC_BASE_URL=http://localhost:8080\n")
	fmt.Printf("  export ANTHROPIC_AUTH_TOKEN=sk-orc-...\n")
	fmt.Printf("  export ANTHROPIC_MODEL=%s\n", entry.ID)
	fmt.Printf("  claude\n")
	fmt.Println()
	fmt.Printf("%sFull walkthrough%s   https://github.com/hadihonarvar/flock/blob/main/MODELS.md#%s\n",
		bold, reset, strings.ReplaceAll(entry.ID, ".", "-"))
}

func max2(n int) int {
	if n < 2 {
		return 2
	}
	return n
}
