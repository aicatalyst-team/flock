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
	Placement     PlacementConfig     `yaml:"placement"`
}

// PlacementConfig tunes the memory-lifecycle manager (admission,
// evict-and-swap) for this node's local engine.
type PlacementConfig struct {
	// Exclusive enforces one resident chat model per machine: loading a
	// model evicts every other non-pinned resident model first, not just
	// enough to fit. Env override: FLOCK_EXCLUSIVE=1.
	Exclusive bool `yaml:"exclusive"`
	// ReservePercent of total RAM is held back from the admission budget
	// for the OS and engine overhead. Default 20. Env override:
	// FLOCK_PLACEMENT_RESERVE_PERCENT.
	ReservePercent int `yaml:"reserve_percent"`
	// DrainTimeoutSeconds bounds how long an eviction waits for in-flight
	// requests to finish before unloading anyway. Default 30.
	// Env override: FLOCK_PLACEMENT_DRAIN_TIMEOUT_SECONDS.
	DrainTimeoutSeconds int `yaml:"drain_timeout_seconds"`
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
	// WhisperEndpoint and PiperEndpoint are optional engines for the
	// audio endpoints; the gateway proxies to them when set and
	// returns 501 with a setup hint otherwise.
	WhisperEndpoint string `yaml:"whisper_endpoint"`
	PiperEndpoint   string `yaml:"piper_endpoint"`
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

	// HedgeReplicas, when > 1, enables request hedging — the router
	// can fire a single request to N least-loaded workers
	// concurrently and return whichever responds first. Each request
	// opts in individually via `flock.hedge: true` body field or
	// `X-Flock-Hedge: 1` header. Cap is router.MaxHedgeReplicas.
	HedgeReplicas int `yaml:"hedge_replicas"`
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

	// OpenAI-compatible hosted gateways. The corresponding env vars
	// (OPENROUTER_API_KEY, GROQ_API_KEY, TOGETHER_API_KEY,
	// FIREWORKS_API_KEY, COHERE_API_KEY, MISTRAL_API_KEY,
	// PERPLEXITY_API_KEY) populate the key fields at runtime.
	OpenRouterURL string `yaml:"openrouter_url"`
	OpenRouterKey string `yaml:"-"`
	GroqURL       string `yaml:"groq_url"`
	GroqKey       string `yaml:"-"`
	TogetherURL   string `yaml:"together_url"`
	TogetherKey   string `yaml:"-"`
	FireworksURL  string `yaml:"fireworks_url"`
	FireworksKey  string `yaml:"-"`
	CohereURL     string `yaml:"cohere_url"`
	CohereKey     string `yaml:"-"`
	MistralURL    string `yaml:"mistral_url"`
	MistralKey    string `yaml:"-"`
	PerplexityURL string `yaml:"perplexity_url"`
	PerplexityKey string `yaml:"-"`
}

// ObservabilityConfig holds knobs for traces/logs/metrics integrations
// that aren't on by default. The Prometheus /metrics endpoint is always
// on — this struct is for the optional extras.
type ObservabilityConfig struct {
	// OTLPEndpoint is the OTLP/HTTP collector URL (e.g.
	// http://localhost:4318). Empty → tracing disabled (NoopTracerProvider,
	// zero overhead). Set via FLOCK_OTLP_ENDPOINT env or this YAML key.
	OTLPEndpoint string `yaml:"otlp_endpoint"`

	// Callbacks ship usage / audit / fallback events to external
	// observability sinks (webhooks, Langfuse, etc.). Each entry runs
	// in its own goroutine with a bounded queue — a slow receiver
	// can't stall the gateway. Drops on overflow are counted via
	// flock_callback_sent_total{outcome="dropped"}.
	Callbacks []CallbackConfig `yaml:"callbacks"`

	// Guardrails run synchronously on the request path. Each entry
	// chooses a mode (pre | post | logging_only) and a driver (today:
	// `webhook`). Pre guardrails can rewrite or block the request
	// before the engine sees it; logging_only entries observe without
	// intervening. Post mode is reserved for a follow-up — see the
	// CHANGELOG for the streaming-response design limitation.
	Guardrails []GuardrailConfig `yaml:"guardrails"`

	// ResponseCache stores deterministic responses (embeddings today;
	// chat completions to follow). Disabled when Enabled=false.
	ResponseCache ResponseCacheConfig `yaml:"response_cache"`
}

// ResponseCacheConfig configures the response cache.
type ResponseCacheConfig struct {
	Enabled           bool   `yaml:"enabled"`
	Driver            string `yaml:"driver"`              // memory | sqlite
	MaxEntries        int    `yaml:"max_entries"`         // memory only; 0 = 1000
	DefaultTTLSeconds int    `yaml:"default_ttl_seconds"` // 0 = 24h
}

// GuardrailConfig is one row from the observability.guardrails list.
// Today only `kind: webhook` is implemented.
type GuardrailConfig struct {
	Name           string            `yaml:"name"`
	Kind           string            `yaml:"kind"`      // webhook
	Mode           string            `yaml:"mode"`      // pre | post | logging_only
	URL            string            `yaml:"url"`       // webhook only
	AuthKey        string            `yaml:"auth_key"`  // optional bearer (env-expanded)
	Headers        map[string]string `yaml:"headers"`   // optional extra headers
	FailOpen       bool              `yaml:"fail_open"` // on error: true → Allow, false → Block
	TimeoutSeconds int               `yaml:"timeout_seconds"`
}

// CallbackConfig is one row from the observability.callbacks list.
// Different `kind` values use different fields; the loader builds the
// matching internal/callbacks driver.
type CallbackConfig struct {
	Kind   string   `yaml:"kind"`   // "webhook" | "langfuse"
	ID     string   `yaml:"id"`     // optional human label; defaults to kind
	URL    string   `yaml:"url"`    // webhook only
	Secret string   `yaml:"secret"` // webhook only — env-expanded
	Events []string `yaml:"events"` // webhook only — defaults to all kinds

	// Langfuse-specific. PublicKey / SecretKey are env-expanded.
	Host      string `yaml:"host"`
	PublicKey string `yaml:"public_key"`
	SecretKey string `yaml:"secret_key"`

	QueueSize int `yaml:"queue_size"` // 0 = 100
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
	if v := os.Getenv("FLOCK_WHISPER_ENDPOINT"); v != "" {
		c.Engine.WhisperEndpoint = v
	}
	if v := os.Getenv("FLOCK_PIPER_ENDPOINT"); v != "" {
		c.Engine.PiperEndpoint = v
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
	if v := os.Getenv("OPENROUTER_API_KEY"); v != "" {
		c.Router.Fallback.OpenRouterKey = v
		c.Router.Fallback.Enabled = true
	}
	if v := os.Getenv("GROQ_API_KEY"); v != "" {
		c.Router.Fallback.GroqKey = v
		c.Router.Fallback.Enabled = true
	}
	if v := os.Getenv("TOGETHER_API_KEY"); v != "" {
		c.Router.Fallback.TogetherKey = v
		c.Router.Fallback.Enabled = true
	}
	if v := os.Getenv("FIREWORKS_API_KEY"); v != "" {
		c.Router.Fallback.FireworksKey = v
		c.Router.Fallback.Enabled = true
	}
	if v := os.Getenv("COHERE_API_KEY"); v != "" {
		c.Router.Fallback.CohereKey = v
		c.Router.Fallback.Enabled = true
	}
	if v := os.Getenv("MISTRAL_API_KEY"); v != "" {
		c.Router.Fallback.MistralKey = v
		c.Router.Fallback.Enabled = true
	}
	if v := os.Getenv("PERPLEXITY_API_KEY"); v != "" {
		c.Router.Fallback.PerplexityKey = v
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
	if v := os.Getenv("FLOCK_EXCLUSIVE"); v == "1" || v == "true" {
		c.Placement.Exclusive = true
	}
	if v := os.Getenv("FLOCK_PLACEMENT_RESERVE_PERCENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n < 100 {
			c.Placement.ReservePercent = n
		}
	}
	if v := os.Getenv("FLOCK_PLACEMENT_DRAIN_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			c.Placement.DrainTimeoutSeconds = n
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
