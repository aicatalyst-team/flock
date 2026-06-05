package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
	name := cfg.Engine.Preferred
	var endpoint, apiKey string
	switch name {
	case "ollama":
		endpoint = cfg.Engine.OllamaEndpoint
	case "vllm":
		endpoint = cfg.Engine.VLLMEndpoint
		apiKey = cfg.Engine.VLLMAPIKey
	case "mlx", "mlx-lm":
		endpoint = cfg.Engine.MLXEndpoint
	default:
		die("unknown engine %q (valid: ollama, vllm, mlx)", name)
	}
	eng, err := engines.NewWithAuth(name, endpoint, apiKey)
	if err != nil {
		die("engine: %v", err)
	}
	return eng
}

func pidFilePath(cfg *config.Config) string {
	return filepath.Join(cfg.DataDir, "flock.pid")
}

// localAdminKeyPath is where bootstrapAdminKey persists the admin key so
// subsequent CLI invocations on this host can authenticate to the leader.
func localAdminKeyPath(cfg *config.Config) string {
	return filepath.Join(cfg.DataDir, "admin.key")
}

// readLocalAdminKey returns the saved admin key, or "" if missing.
func readLocalAdminKey(cfg *config.Config) string {
	data, err := os.ReadFile(localAdminKeyPath(cfg))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// adminCall makes an authenticated HTTP request to the local leader's admin
// API. Returns the response body bytes. If the leader isn't running this
// returns a clear error rather than a confusing dial failure.
func adminCall(ctx context.Context, cfg *config.Config, method, path string, body []byte) ([]byte, error) {
	key := readLocalAdminKey(cfg)
	if key == "" {
		return nil, fmt.Errorf("no admin key on disk at %s — is `flock up` running on this host?", localAdminKeyPath(cfg))
	}
	listen := cfg.Listen
	if listen == "" {
		listen = ":8080"
	}
	url := "http://localhost" + listen + path
	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	var req *http.Request
	var err error
	if reader != nil {
		req, err = http.NewRequestWithContext(ctx, method, url, reader)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, url, http.NoBody)
	}
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("admin call: %w (is flock up running?)", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return respBody, fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}
	return respBody, nil
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
