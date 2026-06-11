// Package config loads Flock configuration from YAML and environment.
//
// Precedence (lowest → highest): defaults → YAML file → environment variables.
// All fields have sensible defaults; no config file is required to run.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the full runtime configuration for a Flock node.
type Config struct {
	Listen        string              `yaml:"listen"`
	ExternalURL   string              `yaml:"external_url"`
	DataDir       string              `yaml:"data_dir"`
	LogLevel      string              `yaml:"log_level"`
	CatalogDir    string              `yaml:"catalog_dir"`
	Storage       StorageConfig       `yaml:"storage"`
	Auth          AuthConfig          `yaml:"auth"`
	Engine        EngineConfig        `yaml:"engine"`
	Router        RouterConfig        `yaml:"router"`
	Observability ObservabilityConfig `yaml:"observability"`
}

type StorageConfig struct {
	Type      string `yaml:"type"`
	DSN       string `yaml:"dsn"`
	ModelsDir string `yaml:"models_dir"`
}

type AuthConfig struct {
	RequireKeys bool `yaml:"require_keys"`
}

type EngineConfig struct {
	Preferred        string `yaml:"preferred"`
	OllamaEndpoint   string `yaml:"ollama_endpoint"`
	VLLMEndpoint     string `yaml:"vllm_endpoint"`
	VLLMAPIKey       string `yaml:"-"` // populated from VLLM_API_KEY env
	MLXEndpoint      string `yaml:"mlx_endpoint"`
	LlamaCppEndpoint string `yaml:"llamacpp_endpoint"`
}

type RouterConfig struct {
	DefaultModel string `yaml:"default_model"`
	// StickySessions is the legacy boolean. Now superseded by
	// StickySessionTTLSeconds — keep parseable for old configs but the
	// new field is the source of truth.
	StickySessions          bool           `yaml:"sticky_sessions"`
	StickySessionTTLSeconds int            `yaml:"sticky_session_ttl_seconds"`
	Fallback                FallbackConfig `yaml:"fallback"`

	// LatencyFallbackP95Seconds enables ROADMAP Bet #1 (latency-aware
	// fallback). When the rolling p95 latency for a primary model exceeds
	// this many seconds, the router walks the catalog fallback chain for
	// a faster candidate to try FIRST. Zero (default) keeps the historical
	// failure-only behavior. Common values: 5–10 seconds. Env override:
	// FLOCK_LATENCY_P95_SECONDS.
	LatencyFallbackP95Seconds int `yaml:"latency_fallback_p95_seconds"`

	// PlacementAllowedFails + PlacementCooldownSeconds together drive the
	// per-node circuit breaker. After this many consecutive engine
	// errors from a worker, the router parks the node for the configured
	// cooldown. Both must be > 0 to enable the feature; either zero
	// disables it. Env overrides: FLOCK_PLACEMENT_ALLOWED_FAILS,
	// FLOCK_PLACEMENT_COOLDOWN_SECONDS.
	PlacementAllowedFails    int `yaml:"placement_allowed_fails"`
	PlacementCooldownSeconds int `yaml:"placement_cooldown_seconds"`
}

type FallbackConfig struct {
	Enabled      bool   `yaml:"enabled"`
	AnthropicURL string `yaml:"anthropic_url"`
	OpenAIURL    string `yaml:"openai_url"`
	AnthropicKey string `yaml:"-"` // populated from env at runtime
	OpenAIKey    string `yaml:"-"` // populated from env at runtime

	// Bedrock (AWS) — auth uses the standard AWS credentials chain (env,
	// shared config, instance role). Empty BedrockRegion disables routing.
	BedrockRegion string `yaml:"bedrock_region"`
	BedrockURL    string `yaml:"bedrock_url"` // optional override

	// Vertex (GCP) — auth uses Application Default Credentials. Empty
	// VertexProject disables routing.
	VertexProject  string `yaml:"vertex_project"`
	VertexLocation string `yaml:"vertex_location"` // default us-central1
	VertexURL      string `yaml:"vertex_url"`      // optional override
}

// ObservabilityConfig holds knobs for traces/logs/metrics integrations
// that aren't on by default. The Prometheus /metrics endpoint is always
// on — this struct is for the optional extras.
type ObservabilityConfig struct {
	// OTLPEndpoint is the OTLP/HTTP collector URL (e.g.
	// http://localhost:4318). Empty → tracing disabled (NoopTracerProvider,
	// zero overhead). Set via FLOCK_OTLP_ENDPOINT env or this YAML key.
	OTLPEndpoint string `yaml:"otlp_endpoint"`
}

// Default returns a Config populated with safe defaults for a single-node setup.
func Default() *Config {
	home, _ := os.UserHomeDir()
	dataDir := filepath.Join(home, ".flock")
	return &Config{
		Listen:      ":8080",
		ExternalURL: "",
		DataDir:     dataDir,
		LogLevel:    "info",
		CatalogDir:  "", // empty → use built-in catalog dir resolution
		Storage: StorageConfig{
			Type:      "sqlite",
			DSN:       filepath.Join(dataDir, "state.db"),
			ModelsDir: filepath.Join(dataDir, "models"),
		},
		Auth: AuthConfig{
			RequireKeys: true,
		},
		Engine: EngineConfig{
			Preferred:        "ollama",
			OllamaEndpoint:   "http://127.0.0.1:11434",
			VLLMEndpoint:     "http://127.0.0.1:8000",
			MLXEndpoint:      "http://127.0.0.1:8080",
			LlamaCppEndpoint: "http://127.0.0.1:8089",
		},
		Router: RouterConfig{
			DefaultModel:   "",
			StickySessions: true,
		},
	}
}

// Load reads config from path (if it exists), then overlays environment variables.
// If path is empty, ~/.flock/config.yaml is tried.
func Load(path string) (*Config, error) {
	cfg := Default()

	if path == "" {
		path = filepath.Join(cfg.DataDir, "config.yaml")
	}

	if data, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	applyEnv(cfg)
	expand(cfg)

	if err := ensureDirs(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Save writes the config back to YAML at path (or default location).
func (c *Config) Save(path string) error {
	if path == "" {
		path = filepath.Join(c.DataDir, "config.yaml")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return os.WriteFile(path, data, 0o600)
}

func applyEnv(c *Config) {
	if v := os.Getenv("FLOCK_LISTEN"); v != "" {
		c.Listen = v
	}
	if v := os.Getenv("FLOCK_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	if v := os.Getenv("FLOCK_LOG_LEVEL"); v != "" {
		c.LogLevel = v
	}
	if v := os.Getenv("FLOCK_EXTERNAL_URL"); v != "" {
		c.ExternalURL = v
	}
	if v := os.Getenv("FLOCK_OLLAMA_ENDPOINT"); v != "" {
		c.Engine.OllamaEndpoint = v
	}
	if v := os.Getenv("FLOCK_VLLM_ENDPOINT"); v != "" {
		c.Engine.VLLMEndpoint = v
	}
	if v := os.Getenv("VLLM_API_KEY"); v != "" {
		c.Engine.VLLMAPIKey = v
	}
	if v := os.Getenv("FLOCK_MLX_ENDPOINT"); v != "" {
		c.Engine.MLXEndpoint = v
	}
	if v := os.Getenv("FLOCK_LLAMACPP_ENDPOINT"); v != "" {
		c.Engine.LlamaCppEndpoint = v
	}
	if v := os.Getenv("FLOCK_ENGINE"); v != "" {
		c.Engine.Preferred = v
	}
	if v := os.Getenv("FLOCK_REQUIRE_KEYS"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.Auth.RequireKeys = b
		}
	}
	if v := os.Getenv("FLOCK_DEFAULT_MODEL"); v != "" {
		c.Router.DefaultModel = v
	}
	if v := os.Getenv("ANTHROPIC_API_KEY"); v != "" {
		c.Router.Fallback.AnthropicKey = v
		c.Router.Fallback.Enabled = true
	}
	if v := os.Getenv("OPENAI_API_KEY"); v != "" {
		c.Router.Fallback.OpenAIKey = v
		c.Router.Fallback.Enabled = true
	}
	if v := os.Getenv("FLOCK_OTLP_ENDPOINT"); v != "" {
		c.Observability.OTLPEndpoint = v
	}
	if v := os.Getenv("FLOCK_BEDROCK_REGION"); v != "" {
		c.Router.Fallback.BedrockRegion = v
		c.Router.Fallback.Enabled = true
	}
	if v := os.Getenv("FLOCK_VERTEX_PROJECT"); v != "" {
		c.Router.Fallback.VertexProject = v
		c.Router.Fallback.Enabled = true
	}
	if v := os.Getenv("FLOCK_VERTEX_LOCATION"); v != "" {
		c.Router.Fallback.VertexLocation = v
	}
	if v := os.Getenv("FLOCK_LATENCY_P95_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			c.Router.LatencyFallbackP95Seconds = n
		}
	}
	if v := os.Getenv("FLOCK_PLACEMENT_ALLOWED_FAILS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			c.Router.PlacementAllowedFails = n
		}
	}
	if v := os.Getenv("FLOCK_PLACEMENT_COOLDOWN_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			c.Router.PlacementCooldownSeconds = n
		}
	}
	if v := os.Getenv("FLOCK_STICKY_SESSION_TTL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			c.Router.StickySessionTTLSeconds = n
		}
	}
}

// expand replaces ~ at the start of paths with the home directory.
func expand(c *Config) {
	home, _ := os.UserHomeDir()
	expandOne := func(p string) string {
		if strings.HasPrefix(p, "~/") {
			return filepath.Join(home, p[2:])
		}
		return p
	}
	c.DataDir = expandOne(c.DataDir)
	c.Storage.DSN = expandOne(c.Storage.DSN)
	c.Storage.ModelsDir = expandOne(c.Storage.ModelsDir)
	c.CatalogDir = expandOne(c.CatalogDir)
}

func ensureDirs(c *Config) error {
	for _, d := range []string{c.DataDir, c.Storage.ModelsDir} {
		if d == "" {
			continue
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	return nil
}
