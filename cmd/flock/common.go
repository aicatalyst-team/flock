package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hadihonarvar/flock/internal/config"
	"github.com/hadihonarvar/flock/internal/engines"
	"github.com/hadihonarvar/flock/internal/models"
	"github.com/hadihonarvar/flock/internal/store"
)

// loadConfigOrExit loads ~/.flock/config.yaml or the default. On failure
// it prints to stderr and exits with status 1.
func loadConfigOrExit() *config.Config {
	cfg, err := config.Load("")
	if err != nil {
		die("config: %v", err)
	}
	return cfg
}

// newLogger returns a JSON slog logger at the configured level.
func newLogger(cfg *config.Config) *slog.Logger {
	lvl := slog.LevelInfo
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}

func openStoreOrExit(cfg *config.Config) store.Store {
	st, err := store.OpenSQLite(cfg.Storage.DSN)
	if err != nil {
		die("store: %v", err)
	}
	return st
}

func loadCatalogOrExit(cfg *config.Config) []models.Entry {
	entries, err := models.LoadCatalog(cfg.CatalogDir)
	if err != nil {
		die("catalog: %v", err)
	}
	return entries
}

func newEngineFromConfig(cfg *config.Config) engines.Engine {
	eng, err := engines.New(cfg.Engine.Preferred, cfg.Engine.OllamaEndpoint)
	if err != nil {
		die("engine: %v", err)
	}
	return eng
}

func pidFilePath(cfg *config.Config) string {
	return filepath.Join(cfg.DataDir, "flock.pid")
}

func writePID(cfg *config.Config) error {
	return os.WriteFile(pidFilePath(cfg), []byte(strconv.Itoa(os.Getpid())), 0o644)
}

func readPID(cfg *config.Config) (int, error) {
	data, err := os.ReadFile(pidFilePath(cfg))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

func removePID(cfg *config.Config) {
	_ = os.Remove(pidFilePath(cfg))
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "flock: "+format+"\n", args...)
	os.Exit(1)
}

func note(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, "\033[1;34m▶\033[0m "+format+"\n", args...)
}

func ok(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, "\033[1;32m✔\033[0m "+format+"\n", args...)
}

func warn(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, "\033[1;33m⚠\033[0m "+format+"\n", args...)
}
