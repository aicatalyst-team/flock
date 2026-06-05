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

func cmdModel(args []string) {
	if len(args) == 0 {
		die("usage: flock model <add|ls|remove> [args]")
	}
	switch args[0] {
	case "add":
		if len(args) < 2 {
			die("usage: flock model add <id>")
		}
		modelAdd(args[1])
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
	default:
		die("unknown subcommand: model %s", args[0])
	}
}

func modelAdd(id string) {
	cfg := loadConfigOrExit()
	cat := loadCatalogOrExit(cfg)
	entry := models.FindByID(cat, id)
	if entry == nil {
		die("no catalog entry for %q (try `flock model search`)", id)
	}
	engineName := entry.Source.OllamaName
	if engineName == "" {
		engineName = entry.ID
	}
	st := openStoreOrExit(cfg)
	defer st.Close()
	eng := newEngineFromConfig(cfg)
	if err := eng.Health(context.Background()); err != nil {
		die("engine not reachable (%v) — start Ollama: `ollama serve`", err)
	}
	note(os.Stdout, "pulling %s (%s) ...", entry.ID, engineName)
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
		Source:      "ollama:" + engineName,
		Status:      "ready",
		SizeBytes:   entry.SizeBytes,
		InstalledAt: time.Now(),
	})
	ok(os.Stdout, "installed: %s", entry.ID)
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
	engineName := strings.TrimPrefix(m.Source, "ollama:")
	if engineName == m.Source { // no prefix found
		engineName = id
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
	fmt.Printf("%-22s %-32s %8s %s\n", "ID", "NAME", "RAM/GB", "TAGS")
	for _, e := range cat {
		if q != "" &&
			!strings.Contains(strings.ToLower(e.ID), q) &&
			!strings.Contains(strings.ToLower(e.DisplayName), q) {
			continue
		}
		fmt.Printf("%-22s %-32s %8d %v\n", e.ID, e.DisplayName, e.Hardware.MinRAMGB, e.Tags)
	}
}

