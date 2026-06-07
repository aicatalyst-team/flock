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
	Preferred      string `yaml:"preferred"`
	OllamaEndpoint string `yaml:"ollama_endpoint"`
	VLLMEndpoint   string `yaml:"vllm_endpoint"`
	VLLMAPIKey     string `yaml:"-"` // populated from VLLM_API_KEY env
	MLXEndpoint    string `yaml:"mlx_endpoint"`
}

type RouterConfig struct {
	DefaultModel   string         `yaml:"default_model"`
	StickySessions bool           `yaml:"sticky_sessions"`
	Fallback       FallbackConfig `yaml:"fallback"`
}

type FallbackConfig struct {
	Enabled      bool   `yaml:"enabled"`
	AnthropicURL string `yaml:"anthropic_url"`
	OpenAIURL    string `yaml:"openai_url"`
	AnthropicKey string `yaml:"-"` // populated from env at runtime
	OpenAIKey    string `yaml:"-"` // populated from env at runtime
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
			Preferred:      "ollama",
			OllamaEndpoint: "http://127.0.0.1:11434",
			VLLMEndpoint:   "http://127.0.0.1:8000",
			MLXEndpoint:    "http://127.0.0.1:8080",
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
