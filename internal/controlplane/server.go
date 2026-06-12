// Package controlplane wires together the gateway, control-plane HTTP routes,
// and protocol adapters. It owns the chi router and the *http.Server lifecycle.
package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hadihonarvar/flock/internal/api"
	"github.com/hadihonarvar/flock/internal/auth"
	"github.com/hadihonarvar/flock/internal/cache"
	"github.com/hadihonarvar/flock/internal/callbacks"
	"github.com/hadihonarvar/flock/internal/config"
	"github.com/hadihonarvar/flock/internal/engines"
	"github.com/hadihonarvar/flock/internal/events"
	"github.com/hadihonarvar/flock/internal/guardrails"
	"github.com/hadihonarvar/flock/internal/lifecycle"
	"github.com/hadihonarvar/flock/internal/models"
	"github.com/hadihonarvar/flock/internal/router"
	"github.com/hadihonarvar/flock/internal/scheduler"
	"github.com/hadihonarvar/flock/internal/store"
	"github.com/hadihonarvar/flock/internal/ui"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Server is the leader-side HTTP server.
type Server struct {
	cfg    *config.Config
	store  store.Store
	engine engines.Engine
	cat    []models.Entry
	log    *slog.Logger
	http   *http.Server

	router      *router.Router
	orch        *scheduler.Orchestrator
	lifecycle   *lifecycle.Manager
	openaiH     *api.Handler
	anthropicH  *api.AnthropicHandler
	egressH     *api.EgressHandler
	rateBuckets *api.BucketStore
	callbacks   *callbacks.Dispatcher

	// bus fans out dashboard refresh events. /admin/v1/events streams
	// to subscribed dashboards; producers (addModel, deleteModel, etc.)
	// publish topic strings on state change.
	bus *events.Bus

	// Version is stamped into traces and the access log. Set by callers
	// before Start; defaults to "dev" if unset.
	Version string

	tracerShutdown func(context.Context) error
}

func NewServer(cfg *config.Config, st store.Store, eng engines.Engine, cat []models.Entry, log *slog.Logger, orch *scheduler.Orchestrator) *Server {
	routed := router.New(eng, st)

	// Wire catalog-driven fallback chains: when a request to model X fails,
	// retry against X's catalog fallback list in order. The resolver
	// returns the typed chains (generic + per-error-class) so the router
	// can pick the right list after classifying the primary's failure.
	// Closure captures the catalog slice — fresh lookups happen per call
	// so a catalog reload would be observed (catalog hot-reload isn't
	// shipped yet but this leaves room).
	routed.SetFallbackResolver(func(modelID string) router.FallbackChains {
		entry := models.FindByID(cat, modelID)
		if entry == nil {
			return router.FallbackChains{}
		}
		return router.FallbackChains{
			Generic:       entry.Fallback,
			ContextLength: entry.FallbackOnContextLength,
			ContentPolicy: entry.FallbackOnContentPolicy,
		}
	})
	// Latency-aware fallback (Bet #1): opt-in via router.latency_fallback_p95_seconds.
	// Zero (default) leaves behavior unchanged.
	if cfg.Router.LatencyFallbackP95Seconds > 0 {
		routed.SetLatencyConfig(router.LatencyConfig{
			P95Threshold: time.Duration(cfg.Router.LatencyFallbackP95Seconds) * time.Second,
		})
	}
	// Placement cooldown ("penalty box"): a worker that errors N times
	// in a row gets parked for the cooldown duration so pick() skips it.
	// Both knobs must be > 0 to enable.
	if cfg.Router.PlacementAllowedFails > 0 && cfg.Router.PlacementCooldownSeconds > 0 {
		routed.SetPlacementCooldown(
			cfg.Router.PlacementAllowedFails,
			time.Duration(cfg.Router.PlacementCooldownSeconds)*time.Second,
		)
	}
	// Sticky sessions: pin (user_id, model) to its last worker for the
	// TTL so multi-turn chats reuse KV cache. Disabled when ttl == 0.
	if cfg.Router.StickySessionTTLSeconds > 0 {
		routed.SetStickyTTL(time.Duration(cfg.Router.StickySessionTTLSeconds) * time.Second)
	}
	// Request hedging — opt-in per request via flock.hedge.
	if cfg.Router.HedgeReplicas > 1 {
		routed.SetHedgeReplicas(cfg.Router.HedgeReplicas)
	}

	openaiH := &api.Handler{
		Engine:  routed,
		Store:   st,
		Catalog: cat,
		Default: cfg.Router.DefaultModel,
	}
	anthropicH := &api.AnthropicHandler{Handler: openaiH}
	egressH := &api.EgressHandler{
		Store: st,
		Config: api.FallbackConfig{
			AnthropicKey:   cfg.Router.Fallback.AnthropicKey,
			AnthropicURL:   cfg.Router.Fallback.AnthropicURL,
			OpenAIKey:      cfg.Router.Fallback.OpenAIKey,
			OpenAIURL:      cfg.Router.Fallback.OpenAIURL,
			BedrockRegion:  cfg.Router.Fallback.BedrockRegion,
			BedrockURL:     cfg.Router.Fallback.BedrockURL,
			VertexProject:  cfg.Router.Fallback.VertexProject,
			VertexLocation: cfg.Router.Fallback.VertexLocation,
			VertexURL:      cfg.Router.Fallback.VertexURL,
			OpenRouterKey:  cfg.Router.Fallback.OpenRouterKey,
			OpenRouterURL:  cfg.Router.Fallback.OpenRouterURL,
			GroqKey:        cfg.Router.Fallback.GroqKey,
			GroqURL:        cfg.Router.Fallback.GroqURL,
			TogetherKey:    cfg.Router.Fallback.TogetherKey,
			TogetherURL:    cfg.Router.Fallback.TogetherURL,
			FireworksKey:   cfg.Router.Fallback.FireworksKey,
			FireworksURL:   cfg.Router.Fallback.FireworksURL,
			CohereKey:      cfg.Router.Fallback.CohereKey,
			CohereURL:      cfg.Router.Fallback.CohereURL,
			MistralKey:     cfg.Router.Fallback.MistralKey,
			MistralURL:     cfg.Router.Fallback.MistralURL,
			PerplexityKey:  cfg.Router.Fallback.PerplexityKey,
			PerplexityURL:  cfg.Router.Fallback.PerplexityURL,
		},
	}
	buckets := api.NewBucketStore()
	api.SetBucketStore(buckets)
	// Wire the catalog into the per-request cost computation path. The
	// recordUsage step does a price lookup against this catalog +
	// vendor pricing table to populate usage.cost_usd.
	api.SetCatalog(cat)
	// Observability callbacks (webhooks / Langfuse). Each configured
	// sink runs in its own goroutine with a bounded queue.
	dispatcher := buildCallbackDispatcher(cfg.Observability.Callbacks, log)
	api.SetCallbackDispatcher(dispatcher)
	// Guardrails (pre-call hooks). Synchronous on the request path.
	api.SetGuardrails(buildGuardrailRegistry(cfg.Observability.Guardrails, log))
	// Response cache (embeddings today; chat in follow-up).
	api.SetResponseCache(buildResponseCache(cfg.Observability.ResponseCache, st, log))
	// Audio + rerank endpoint proxies. Empty endpoints → handler
	// returns 501 with setup hint instead of trying.
	api.SetRerankAudioConfig(api.RerankAudioConfig{
		LlamaCppEndpoint: cfg.Engine.LlamaCppEndpoint,
		WhisperEndpoint:  cfg.Engine.WhisperEndpoint,
		PiperEndpoint:    cfg.Engine.PiperEndpoint,
	})
	return &Server{
		cfg:         cfg,
		store:       st,
		engine:      eng,
		cat:         cat,
		log:         log,
		router:      routed,
		orch:        orch,
		openaiH:     openaiH,
		anthropicH:  anthropicH,
		egressH:     egressH,
		rateBuckets: buckets,
		callbacks:   dispatcher,
		bus:         events.New(),
	}
}

// buildResponseCache instantiates the configured driver (memory or
// sqlite). Returns nil when disabled — the api package short-circuits
// the cache path on nil.
func buildResponseCache(cfg config.ResponseCacheConfig, st store.Store, log *slog.Logger) cache.Cache {
	if !cfg.Enabled {
		return nil
	}
	ttl := time.Duration(cfg.DefaultTTLSeconds) * time.Second
	switch cfg.Driver {
	case "", "memory":
		return cache.NewMemory(cfg.MaxEntries, ttl)
	case "sqlite":
		return cache.NewSQLite(st.Cache(), ttl)
	default:
		log.Warn("unknown response_cache driver — disabling cache", "driver", cfg.Driver)
		return nil
	}
}

// buildGuardrailRegistry constructs the three-mode guardrail
// registry from the YAML rows. Unknown kinds or modes are ignored
// (with a warn log) so a typo'd config doesn't crash startup.
func buildGuardrailRegistry(rows []config.GuardrailConfig, log *slog.Logger) *guardrails.Registry {
	if len(rows) == 0 {
		return nil
	}
	reg := &guardrails.Registry{
		Pre:         guardrails.NewChain(),
		Post:        guardrails.NewChain(),
		LoggingOnly: guardrails.NewChain(),
	}
	pre := []guardrails.Guardrail{}
	post := []guardrails.Guardrail{}
	log0 := []guardrails.Guardrail{}
	for _, r := range rows {
		mode := guardrails.Mode(r.Mode)
		switch mode {
		case guardrails.ModePre, guardrails.ModePost, guardrails.ModeLoggingOnly:
		default:
			log.Warn("guardrail with unknown mode — skipping", "name", r.Name, "mode", r.Mode)
			continue
		}
		var g guardrails.Guardrail
		switch r.Kind {
		case "webhook":
			if r.URL == "" {
				log.Warn("guardrail webhook missing url — skipping", "name", r.Name)
				continue
			}
			to := time.Duration(r.TimeoutSeconds) * time.Second
			g = guardrails.NewWebhook(guardrails.WebhookConfig{
				ID:       r.Name,
				Mode:     mode,
				URL:      r.URL,
				AuthKey:  os.ExpandEnv(r.AuthKey),
				Headers:  r.Headers,
				FailOpen: r.FailOpen,
				Timeout:  to,
			})
		default:
			log.Warn("guardrail with unknown kind — skipping", "name", r.Name, "kind", r.Kind)
			continue
		}
		switch mode {
		case guardrails.ModePre:
			pre = append(pre, g)
		case guardrails.ModePost:
			post = append(post, g)
		case guardrails.ModeLoggingOnly:
			log0 = append(log0, g)
		}
	}
	reg.Pre = guardrails.NewChain(pre...)
	reg.Post = guardrails.NewChain(post...)
	reg.LoggingOnly = guardrails.NewChain(log0...)
	return reg
}

// buildCallbackDispatcher constructs the observability fan-out from
// the YAML rows. Each row maps to either a Webhook or a Langfuse
// driver; unknown kinds are ignored (with a warn log) so a typo'd
// config doesn't crash startup.
func buildCallbackDispatcher(rows []config.CallbackConfig, log *slog.Logger) *callbacks.Dispatcher {
	if len(rows) == 0 {
		return nil
	}
	sinks := make([]callbacks.Sink, 0, len(rows))
	for _, r := range rows {
		switch r.Kind {
		case "webhook":
			if r.URL == "" {
				log.Warn("webhook callback missing url — skipping", "id", r.ID)
				continue
			}
			sinks = append(sinks, callbacks.NewWebhook(callbacks.WebhookConfig{
				ID:      r.ID,
				URL:     r.URL,
				Secret:  os.ExpandEnv(r.Secret),
				Events:  r.Events,
				QueueSz: r.QueueSize,
			}, log))
		case "langfuse":
			pub := os.ExpandEnv(r.PublicKey)
			sec := os.ExpandEnv(r.SecretKey)
			if pub == "" || sec == "" {
				log.Warn("langfuse callback missing keys — skipping", "id", r.ID)
				continue
			}
			sinks = append(sinks, callbacks.NewLangfuse(callbacks.LangfuseConfig{
				ID:        r.ID,
				Host:      r.Host,
				PublicKey: pub,
				SecretKey: sec,
				QueueSz:   r.QueueSize,
			}, log))
		default:
			log.Warn("unknown callback kind — skipping", "kind", r.Kind)
		}
	}
	if len(sinks) == 0 {
		return nil
	}
	return callbacks.NewDispatcher(log, sinks...)
}

func (s *Server) Start(ctx context.Context) error {
	if s.Version == "" {
		s.Version = "dev"
	}
	// Init OTLP tracing (no-op if endpoint not configured).
	shutdown, err := initTracing(ctx, s.cfg.Observability.OTLPEndpoint, s.Version, s.log)
	if err != nil {
		return fmt.Errorf("init tracing: %w", err)
	}
	s.tracerShutdown = shutdown

	// Wrap chi router with otelhttp so each inbound request gets a span.
	// Even when tracing is disabled (NoopTracerProvider), the wrapper still
	// participates in W3C traceparent propagation — cheap.
	handler := otelhttp.NewHandler(s.routes(), "http.request",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
	)

	s.http = &http.Server{
		Addr:              s.cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 30 * time.Second,
	}
	s.log.Info("listening", "addr", s.cfg.Listen)
	errCh := make(chan error, 1)
	go func() { errCh <- s.http.ListenAndServe() }()
	select {
	case <-ctx.Done():
		return s.Shutdown(context.Background())
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("listen: %w", err)
		}
		return nil
	}
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.http == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	httpErr := s.http.Shutdown(ctx)
	if s.tracerShutdown != nil {
		// Best-effort: flush any pending spans. Don't mask the http shutdown
		// error if both fail.
		if err := s.tracerShutdown(ctx); err != nil {
			s.log.Warn("tracer shutdown", "err", err)
		}
	}
	return httpErr
}

func (s *Server) routes() http.Handler {
	r := chi.NewRouter()
	// Recoverer first: a panic anywhere downstream is caught, and the
	// accessLog middleware can still record the 500.
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(s.accessLog)

	// Public
	r.Get("/healthz", s.healthz)
	r.Get("/readyz", s.readyz)
	r.Handle("/metrics", promhttp.Handler())

	// Web UI (single embedded HTML page; assets via CDN)
	r.Get("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(ui.IndexHTML)
	})

	// Localhost-only bootstrap: hand the dashboard the saved admin key
	// so first-time / returning users don't have to copy it from the
	// terminal. The endpoint is unauthenticated by design — it's how
	// you GET the credential in the first place — but it's gated to:
	//   1. requests from a loopback peer (127.0.0.1 / ::1), AND
	//   2. requests with no proxy-forwarding headers (otherwise a
	//      remote attacker could spoof loopback by setting
	//      X-Forwarded-For: 127.0.0.1 through a misconfigured proxy).
	// If either check fails we return 404, indistinguishable from
	// "endpoint doesn't exist."
	r.Get("/admin/v1/bootstrap-key", s.bootstrapAdminKey)

	// OpenAI-compatible + Anthropic-compatible (auth + quota)
	r.Route("/v1", func(r chi.Router) {
		r.Use(auth.Middleware(s.store.APIKeys(), s.cfg.Auth.RequireKeys))
		// Stamp a request id + standard rate-limit headers on every
		// response so client SDKs see throttling status without
		// special-casing Flock. Runs after auth so the key is known.
		r.Use(api.ResponseHeadersMiddleware(s.rateBuckets))
		// Per-key model allowlist runs BEFORE quota: a key with no quota
		// to spend on an unauthorized model would otherwise burn a 429
		// instead of the more accurate 403.
		r.Use(api.ModelAllowMiddleware(s.store))
		// RPM/TPM ceilings. Wired before the daily quota check so a
		// runaway client gets the more-actionable 429 with Retry-After
		// instead of the daily 429.
		r.Use(api.RateLimitMiddleware(s.rateBuckets))
		// Monthly + dollar budgets — refuse if any budget for this
		// key is at or above its limit. Runs before the legacy
		// daily-quota check (which is now a single-budget special
		// case the new system subsumes).
		r.Use(api.BudgetMiddleware(s.store))
		r.Use(api.QuotaMiddleware(s.store))
		r.Get("/models", s.openaiH.ListModels)
		r.Post("/chat/completions", s.dispatchOpenAIChat)
		r.Post("/embeddings", s.openaiH.Embeddings)
		r.Post("/rerank", s.openaiH.Rerank)
		r.Post("/audio/transcriptions", s.openaiH.AudioTranscriptions)
		r.Post("/audio/speech", s.openaiH.AudioSpeech)
		r.Post("/messages", s.dispatchAnthropicMessages)
		r.Post("/messages/count_tokens", s.anthropicH.CountTokens)
	})

	// Admin (admin-only)
	r.Route("/admin/v1", func(r chi.Router) {
		r.Use(auth.Middleware(s.store.APIKeys(), s.cfg.Auth.RequireKeys))
		r.Use(s.auditMiddleware)

		// Node lifecycle endpoints accept either admin or node scope so
		// agents can register and heartbeat with a scope=node token.
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireScopeAny("admin", "node"))
			r.Post("/nodes/register", s.registerNode)
			r.Post("/nodes/heartbeat", s.heartbeatNode)
		})

		// Everything else is admin only.
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireScope("admin"))

			// Nodes
			r.Get("/nodes", s.listNodes)
			r.Post("/nodes/{id}/drain", s.drainNode)
			r.Delete("/nodes/{id}", s.deleteNode)

			// Models
			r.Get("/models", s.listInstalledModels)
			r.Get("/catalog", s.listCatalog)
			r.Post("/models", s.addModel)
			r.Delete("/models/{id}", s.deleteModel)
			r.Post("/models/{id}/unload", s.unloadModel)
			r.Post("/models/{id}/load", s.loadModel)

			// Memory: live engine residency + the desired-placement set.
			r.Get("/memory", s.memoryStatus)

			// Tokens
			r.Get("/tokens", s.listTokens)
			r.Post("/tokens", s.createToken)
			r.Patch("/tokens/{id}", s.editToken)
			r.Delete("/tokens/{id}", s.revokeToken)
			r.Get("/tokens/{id}/budgets", s.listBudgets)
			r.Post("/tokens/{id}/budgets", s.createBudget)
			r.Delete("/tokens/{id}/budgets/{bid}", s.deleteBudget)

			// Observability
			r.Get("/usage/recent", s.listUsageRecent)
			r.Get("/usage/summary", s.usageSummary)
			r.Get("/usage/breakdown", s.usageBreakdown)
			r.Get("/audit/recent", s.listAuditRecent)
			r.Get("/audit/summary", s.auditSummary)

			// Shards
			r.Get("/shards", s.listShards)
			r.Get("/shards/processes", s.listShardProcesses)
			r.Post("/shards/create", s.createShards)
			r.Delete("/shards/{model_id}", s.deleteShards)

			// Config (read-only sanitized view)
			r.Get("/config", s.getConfig)

			// Compact status used by the dashboard top-bar chips. Same
			// data the `flock status` CLI surfaces, returned as one JSON
			// blob so the UI can poll a single endpoint.
			r.Get("/status", s.statusSummary)

			// Server-Sent Events stream. Dashboards subscribe once and
			// re-fetch the relevant view on every event. Replaces the
			// per-tab 5 s polling with push-on-change.
			r.Get("/events", s.eventsStream)

			// Onboarding-and-sharing (M3-T23 / M3-T24 / M3-T26)
			r.Get("/connect/clients", s.listConnectClients)
			r.Post("/connect/snippet", s.renderConnectSnippet)
			r.Post("/invite", s.inviteUser)
			r.Post("/healthcheck", s.healthcheck)

			// Observability callbacks — list configured sinks +
			// fire a synthetic test event.
			r.Get("/callbacks", s.listCallbacks)
			r.Post("/callbacks/test", s.testCallback)

			// Response cache stats + flush.
			r.Get("/cache/stats", s.cacheStats)
			r.Delete("/cache", s.cacheFlush)
		})
	})

	return r
}

// dispatchOpenAIChat inspects the request body's "model" field. If it names
// a vendor model (claude-*, gpt-*) AND fallback is configured, the request is
// proxied to the vendor; otherwise it goes to the local engine.
func (s *Server) dispatchOpenAIChat(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	model := peekModel(body)
	if vendor := api.Vendor(model); vendor != "" && s.cfg.Router.Fallback.Enabled {
		switch vendor {
		case "openai":
			r.Body = io.NopCloser(bytes.NewReader(body))
			s.egressH.ServeOpenAI(w, r)
		case "vertex":
			r.Body = io.NopCloser(bytes.NewReader(body))
			s.egressH.ServeVertex(w, r)
		case "openrouter":
			r.Body = io.NopCloser(bytes.NewReader(body))
			s.egressH.ServeOpenRouter(w, r)
		case "groq":
			r.Body = io.NopCloser(bytes.NewReader(body))
			s.egressH.ServeGroq(w, r)
		case "together":
			r.Body = io.NopCloser(bytes.NewReader(body))
			s.egressH.ServeTogether(w, r)
		case "fireworks":
			r.Body = io.NopCloser(bytes.NewReader(body))
			s.egressH.ServeFireworks(w, r)
		case "cohere":
			r.Body = io.NopCloser(bytes.NewReader(body))
			s.egressH.ServeCohere(w, r)
		case "mistral":
			r.Body = io.NopCloser(bytes.NewReader(body))
			s.egressH.ServeMistral(w, r)
		case "perplexity":
			r.Body = io.NopCloser(bytes.NewReader(body))
			s.egressH.ServePerplexity(w, r)
		case "anthropic", "bedrock":
			// Protocol mismatch: OpenAI-format request with a Claude model.
			// Anthropic's API only accepts /v1/messages, so return an actionable
			// error rather than forwarding garbage upstream.
			writeJSONError(w, http.StatusBadRequest,
				fmt.Sprintf("model %q uses the Anthropic message shape; POST to /v1/messages instead of /v1/chat/completions", model))
		}
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	s.openaiH.ChatCompletions(w, r)
}

func (s *Server) dispatchAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	model := peekModel(body)
	if vendor := api.Vendor(model); vendor != "" && s.cfg.Router.Fallback.Enabled {
		switch vendor {
		case "anthropic":
			r.Body = io.NopCloser(bytes.NewReader(body))
			s.egressH.ServeAnthropic(w, r)
		case "bedrock":
			r.Body = io.NopCloser(bytes.NewReader(body))
			s.egressH.ServeBedrock(w, r)
		case "openai", "vertex":
			// Protocol mismatch: Anthropic-format request with a non-Anthropic
			// model.
			writeJSONError(w, http.StatusBadRequest,
				fmt.Sprintf("model %q does not use the Anthropic message shape; POST to /v1/chat/completions instead", model))
		}
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	s.anthropicH.Messages(w, r)
}

func peekModel(body []byte) string {
	var m struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &m)
	return m.Model
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, "ok")
}

// statusSummary returns the compact status payload used by the dashboard
// top-bar chips and `flock status --json`. Single round-trip lookup so
// polling stays cheap.
func (s *Server) statusSummary(w http.ResponseWriter, r *http.Request) {
	type engineStatus struct {
		Name      string `json:"name"`
		Endpoint  string `json:"endpoint"`
		Reachable bool   `json:"reachable"`
		Error     string `json:"error,omitempty"`
	}
	out := struct {
		Role            string       `json:"role"`
		Engine          engineStatus `json:"engine"`
		Nodes           int          `json:"nodes"`
		ModelsInstalled int          `json:"models_installed"`
	}{
		Role: "leader",
		Engine: engineStatus{
			Name:     s.engine.Name(),
			Endpoint: s.engine.Endpoint(),
		},
	}
	if err := s.engine.Health(r.Context()); err != nil {
		out.Engine.Error = err.Error()
	} else {
		out.Engine.Reachable = true
	}
	if nodes, err := s.store.Nodes().List(r.Context()); err == nil {
		out.Nodes = len(nodes)
	}
	if ms, err := s.store.Models().List(r.Context()); err == nil {
		out.ModelsInstalled = len(ms)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if err := s.engine.Health(r.Context()); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "degraded", "engine": err.Error()})
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, "ready")
}

func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.store.Nodes().List(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Decorate each row with cooldown_until when the router has the
	// node in its penalty box. JSON omits the field when the time is
	// zero so legacy clients see the unchanged shape.
	type nodeView struct {
		store.Node
		CooldownUntil *time.Time `json:"cooldown_until,omitempty"`
	}
	out := make([]nodeView, 0, len(nodes))
	for _, n := range nodes {
		v := nodeView{Node: n}
		if t := s.router.CooldownUntil(n.ID); !t.IsZero() {
			v.CooldownUntil = &t
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) registerNode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID           string `json:"id"`
		Hostname     string `json:"hostname"`
		OS           string `json:"os"`
		Arch         string `json:"arch"`
		RAMGB        int    `json:"ram_gb"`
		Address      string `json:"address"`
		HardwareJSON string `json:"hardware_json"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	// The presented bearer token doubles as the shared secret for both
	// directions of communication. Store it on the node row so the router
	// can authenticate outbound calls to the worker.
	// NOTE: stored plaintext today — assumes a trusted network (LAN or
	// Tailscale). Replace with HMAC-based mutual auth once the OIDC +
	// key-management story lands.
	workerToken := extractBearer(r)
	n := store.Node{
		ID:            req.ID,
		Hostname:      req.Hostname,
		OS:            req.OS,
		Arch:          req.Arch,
		RAMGB:         req.RAMGB,
		Address:       req.Address,
		WorkerToken:   workerToken,
		HardwareJSON:  req.HardwareJSON,
		LastHeartbeat: time.Now(),
		State:         "ready",
	}
	if err := s.store.Nodes().Upsert(r.Context(), n); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.bus.Publish(events.Event{Topic: events.TopicNodes, ID: n.ID})
	writeJSON(w, http.StatusOK, map[string]string{"status": "registered", "id": n.ID})
}

func (s *Server) heartbeatNode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID           string   `json:"id"`
		LoadedModels []string `json:"loaded_models"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}
	n, err := s.store.Nodes().Get(r.Context(), req.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n == nil {
		writeJSONError(w, http.StatusNotFound, "unknown node — register first")
		return
	}
	n.LastHeartbeat = time.Now()
	if n.State == "joining" {
		n.State = "ready"
	}
	if err := s.store.Nodes().Upsert(r.Context(), *n); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Reconcile placements with what the worker reports loaded right now.
	placements := make([]store.Placement, 0, len(req.LoadedModels))
	for _, m := range req.LoadedModels {
		placements = append(placements, store.Placement{
			NodeID:   req.ID,
			ModelID:  m,
			Status:   "ready",
			LastSeen: time.Now(),
		})
	}
	if err := s.store.Placements().ReplaceForNode(r.Context(), req.ID, placements); err != nil {
		s.log.Warn("placements replace failed", "node", req.ID, "err", err)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---- node admin ----

func (s *Server) drainNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	n, err := s.store.Nodes().Get(r.Context(), id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n == nil {
		writeJSONError(w, http.StatusNotFound, "no such node: "+id)
		return
	}
	n.State = "draining"
	if err := s.store.Nodes().Upsert(r.Context(), *n); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.bus.Publish(events.Event{Topic: events.TopicNodes, ID: id})
	writeJSON(w, http.StatusOK, map[string]string{"status": "draining", "id": id})
}

func (s *Server) deleteNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.Nodes().Delete(r.Context(), id); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Also clean up placements for the removed node so the router doesn't
	// keep trying it.
	if ps, _ := s.store.Placements().GetByNode(r.Context(), id); ps != nil {
		for _, p := range ps {
			_ = s.store.Placements().Delete(r.Context(), p.NodeID, p.ModelID)
		}
	}
	s.bus.Publish(events.Event{Topic: events.TopicNodes, ID: id})
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed", "id": id})
}

// ---- model admin ----

func (s *Server) listCatalog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cat)
}

func (s *Server) addModel(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	// Scheme-prefixed ids (hf:/ollama:/file:) bypass the catalog so the
	// dashboard "Add custom model" input can install anything the engine
	// supports — same surface as `flock model add hf:owner/repo`.
	var entry *models.Entry
	if e, ok := models.ParseSchemeID(req.ID); ok {
		entry = e
	} else {
		entry = models.FindByID(s.cat, req.ID)
	}
	if entry == nil {
		writeJSONError(w, http.StatusNotFound, "no catalog entry for "+req.ID+" (try a scheme-prefixed id like hf:owner/repo, ollama:tag, or file:/path)")
		return
	}
	// Sharded models delegate to the orchestrator.
	if entry.Sharding.Required {
		if s.orch == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "sharding orchestrator not configured")
			return
		}
		if err := s.orch.CreateSharded(r.Context(), *entry, 0); err != nil {
			writeJSONError(w, http.StatusBadGateway, err.Error())
			return
		}
		s.router.InvalidateModel(req.ID)
		s.bus.Publish(events.Event{Topic: events.TopicModels, ID: req.ID})
		s.bus.Publish(events.Event{Topic: events.TopicShards, ID: req.ID})
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready", "id": req.ID, "kind": "sharded"})
		return
	}
	// Non-sharded: pull via the local engine.
	engineName := ""
	switch s.engine.Name() {
	case "ollama":
		engineName = entry.Source.OllamaName
	case "vllm", "mlx", "mlx-lm":
		engineName = entry.Source.Repo
		if engineName == "" {
			engineName = entry.Source.Path
		}
	default:
		// llamacpp variants accept either an HF repo (-hf) or a local path (-m).
		if entry.Source.Repo != "" {
			engineName = entry.Source.Repo
		} else if entry.Source.Path != "" {
			engineName = entry.Source.Path
		}
	}
	if engineName == "" {
		engineName = entry.ID
	}
	// Synchronous pull — may take minutes. Future: stream progress via SSE.
	if err := s.engine.Pull(r.Context(), engineName, nil); err != nil {
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	_ = s.store.Models().Upsert(r.Context(), store.Model{
		ID: entry.ID, CatalogID: entry.ID,
		Source: s.engine.Name() + ":" + engineName,
		Status: "ready", SizeBytes: entry.SizeBytes,
		InstalledAt: time.Now(),
	})
	_ = s.store.Placements().Upsert(r.Context(), store.Placement{
		NodeID: "local", ModelID: engineName, Status: "ready", LastSeen: time.Now(),
	})
	s.bus.Publish(events.Event{Topic: events.TopicModels, ID: req.ID})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready", "id": req.ID, "kind": "local"})
}

func (s *Server) deleteModel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// Sharded model? Tear down via the orchestrator.
	shards, _ := s.store.Shards().GetByModel(r.Context(), id)
	if len(shards) > 0 {
		if s.orch == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "sharding orchestrator not configured")
			return
		}
		if err := s.orch.RemoveSharded(r.Context(), id); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.router.InvalidateModel(id)
		s.bus.Publish(events.Event{Topic: events.TopicModels, ID: id})
		s.bus.Publish(events.Event{Topic: events.TopicShards, ID: id})
		writeJSON(w, http.StatusOK, map[string]string{"status": "removed", "id": id, "kind": "sharded"})
		return
	}
	// Non-sharded: delete from store + engine.
	m, _ := s.store.Models().Get(r.Context(), id)
	if m != nil {
		engineName := id
		if idx := indexByte(m.Source, ':'); idx >= 0 && idx < len(m.Source)-1 {
			engineName = m.Source[idx+1:]
		}
		_ = s.engine.Delete(r.Context(), engineName)
		_ = s.store.Placements().Delete(r.Context(), "local", engineName)
		_ = s.store.DesiredPlacements().Delete(r.Context(), "local", id)
	}
	if err := s.store.Models().Delete(r.Context(), id); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.bus.Publish(events.Event{Topic: events.TopicModels, ID: id})
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed", "id": id, "kind": "local"})
}

// eventsStream serves Server-Sent Events to dashboard subscribers. Each
// connection gets its own buffered channel from the bus; producers
// elsewhere in the server (model add/remove, node heartbeat, usage
// record) publish topic strings that we encode as one-line SSE messages.
// The handler also sends a 25 s heartbeat ping so reverse proxies don't
// idle the connection out.
func (s *Server) eventsStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, cancel := s.bus.Subscribe(32)
	defer cancel()

	// Greet so the EventSource client knows the stream is alive even
	// before the first state change.
	_, _ = fmt.Fprintf(w, "event: hello\ndata: {\"ok\":true}\n\n")
	flusher.Flush()

	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			// SSE comment line — clients ignore it, but proxies see traffic.
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			payload, _ := json.Marshal(ev)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Topic, payload)
			flusher.Flush()
		}
	}
}

// unloadModel asks the engine to drop the model from RAM without
// deleting weights from disk. Mirrors `flock model unload`.
//
// With the lifecycle manager attached (the normal `flock up` path) the
// unload also drains in-flight requests first and clears the model's
// desired-placement row so it stays unloaded across restarts. The
// legacy direct-engine path below remains for embedded uses without a
// manager.
func (s *Server) unloadModel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if s.lifecycle != nil {
		err := s.lifecycle.Unload(r.Context(), id, actorFrom(r))
		switch {
		case errors.Is(err, lifecycle.ErrNotInstalled):
			writeJSONError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, engines.ErrUnloadNotSupported):
			writeJSON(w, http.StatusOK, map[string]string{
				"status": "noop", "id": id,
				"reason": s.engine.Name() + " does not support online unload",
			})
		case err != nil:
			writeJSONError(w, http.StatusInternalServerError, err.Error())
		default:
			s.bus.Publish(events.Event{Topic: events.TopicModels, ID: id})
			writeJSON(w, http.StatusOK, map[string]string{"status": "unloaded", "id": id})
		}
		return
	}
	m, _ := s.store.Models().Get(r.Context(), id)
	engineName := id
	if m != nil {
		if idx := indexByte(m.Source, ':'); idx >= 0 && idx < len(m.Source)-1 {
			engineName = m.Source[idx+1:]
		}
	}
	// Bounded context: a wedged-but-listening engine should fail fast
	// rather than tying up the admin connection.
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.engine.Health(ctx); err != nil {
		writeJSONError(w, http.StatusServiceUnavailable,
			"engine not reachable: "+err.Error())
		return
	}
	if err := s.engine.Unload(ctx, engineName); err != nil {
		if errors.Is(err, engines.ErrUnloadNotSupported) {
			writeJSON(w, http.StatusOK, map[string]string{
				"status": "noop",
				"id":     id,
				"reason": s.engine.Name() + " does not support online unload",
			})
			return
		}
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.bus.Publish(events.Event{Topic: events.TopicModels, ID: id})
	writeJSON(w, http.StatusOK, map[string]string{"status": "unloaded", "id": id})
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// ---- token admin ----

type tokenView struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Scope            string     `json:"scope"`
	UserID           string     `json:"user_id"`
	QuotaDailyTokens int64      `json:"quota_daily_tokens"`
	RPMLimit         int        `json:"rpm_limit"`
	TPMLimit         int        `json:"tpm_limit"`
	AllowedModels    []string   `json:"allowed_models"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	Revoked          bool       `json:"revoked"`
	CreatedAt        time.Time  `json:"created_at"`
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	keys, err := s.store.APIKeys().List(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]tokenView, 0, len(keys))
	for _, k := range keys {
		view := tokenView{
			ID: k.ID, Name: k.Name, Scope: k.Scope, UserID: k.UserID,
			QuotaDailyTokens: k.QuotaDailyTokens,
			RPMLimit:         k.RPMLimit,
			TPMLimit:         k.TPMLimit,
			AllowedModels:    k.AllowedModels,
			Revoked:          k.Revoked, CreatedAt: k.CreatedAt,
		}
		if !k.ExpiresAt.IsZero() {
			t := k.ExpiresAt
			view.ExpiresAt = &t
		}
		out = append(out, view)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var req struct {
		Name             string     `json:"name"`
		Scope            string     `json:"scope"` // admin | user | node
		UserID           string     `json:"user_id"`
		QuotaDailyTokens int64      `json:"quota_daily_tokens"`
		RPMLimit         int        `json:"rpm_limit"`
		TPMLimit         int        `json:"tpm_limit"`
		AllowedModels    []string   `json:"allowed_models"`
		ExpiresAt        *time.Time `json:"expires_at"`
		TTLSeconds       int        `json:"ttl_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if req.Name == "" {
		writeJSONError(w, http.StatusBadRequest, "name required")
		return
	}
	if req.Scope == "" {
		req.Scope = "user"
	}
	if req.Scope != "admin" && req.Scope != "user" && req.Scope != "node" {
		writeJSONError(w, http.StatusBadRequest, "scope must be admin|user|node")
		return
	}
	userID := req.UserID
	if userID == "" && req.Scope != "node" {
		userID = req.Name
	}
	plain, rec, err := auth.Generate(req.Name, req.Scope, userID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rec.QuotaDailyTokens = req.QuotaDailyTokens
	rec.RPMLimit = req.RPMLimit
	rec.TPMLimit = req.TPMLimit
	rec.AllowedModels = req.AllowedModels
	switch {
	case req.TTLSeconds > 0:
		rec.ExpiresAt = time.Now().Add(time.Duration(req.TTLSeconds) * time.Second)
	case req.ExpiresAt != nil:
		rec.ExpiresAt = req.ExpiresAt.UTC()
	}
	if err := s.store.APIKeys().Create(r.Context(), rec); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := map[string]any{
		"id":             rec.ID,
		"name":           rec.Name,
		"scope":          rec.Scope,
		"rpm_limit":      rec.RPMLimit,
		"tpm_limit":      rec.TPMLimit,
		"allowed_models": rec.AllowedModels,
		"plaintext":      plain, // shown ONCE; caller must save it now
		"created_at":     rec.CreatedAt,
	}
	if !rec.ExpiresAt.IsZero() {
		resp["expires_at"] = rec.ExpiresAt
	}
	writeJSON(w, http.StatusOK, resp)
}

// editToken updates editable fields on an existing token. Today only
// the allowlist (`allowed_models`) is editable; revoke/delete is handled
// by DELETE. The body is a partial-update — pass `allowed_models: null`
// to remove the restriction, `[]` to deny all, or a list to replace.
func (s *Server) editToken(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	id := chi.URLParam(r, "id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "token id required")
		return
	}
	// Use a json.RawMessage for allowed_models so we can tell
	// "field absent" from "field present and null" — both round-trip
	// to a nil slice in a plain `[]string` field. RPM/TPM use
	// pointers (nil = field absent, *int = explicit set).
	var req struct {
		AllowedModels *json.RawMessage `json:"allowed_models"`
		RPMLimit      *int             `json:"rpm_limit"`
		TPMLimit      *int             `json:"tpm_limit"`
		ExpiresAt     *json.RawMessage `json:"expires_at"` // RFC3339 string or null
		TTLSeconds    *int             `json:"ttl_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if req.AllowedModels == nil && req.RPMLimit == nil && req.TPMLimit == nil && req.ExpiresAt == nil && req.TTLSeconds == nil {
		writeJSONError(w, http.StatusBadRequest, "no editable fields in body (try `allowed_models`, `rpm_limit`, `tpm_limit`, `expires_at`, `ttl_seconds`)")
		return
	}
	resp := map[string]any{"id": id}
	if req.AllowedModels != nil {
		raw := string(*req.AllowedModels)
		var allowed []string // nil = unrestricted
		if raw != "null" {
			if err := json.Unmarshal(*req.AllowedModels, &allowed); err != nil {
				writeJSONError(w, http.StatusBadRequest, "allowed_models must be a list or null")
				return
			}
			if allowed == nil {
				allowed = []string{} // empty list = deny all
			}
		}
		if err := s.store.APIKeys().UpdateAllowedModels(r.Context(), id, allowed); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		resp["allowed_models"] = allowed
	}
	if req.RPMLimit != nil || req.TPMLimit != nil {
		// Fetch current values so a partial edit doesn't accidentally
		// reset the field not being changed.
		current, err := s.store.APIKeys().GetByID(r.Context(), id)
		if err != nil || current == nil {
			writeJSONError(w, http.StatusNotFound, "token not found")
			return
		}
		rpm, tpm := current.RPMLimit, current.TPMLimit
		if req.RPMLimit != nil {
			rpm = *req.RPMLimit
		}
		if req.TPMLimit != nil {
			tpm = *req.TPMLimit
		}
		if err := s.store.APIKeys().UpdateRateLimits(r.Context(), id, rpm, tpm); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		resp["rpm_limit"] = rpm
		resp["tpm_limit"] = tpm
	}
	if req.ExpiresAt != nil || req.TTLSeconds != nil {
		var expiresAt time.Time
		switch {
		case req.TTLSeconds != nil:
			if *req.TTLSeconds <= 0 {
				expiresAt = time.Time{} // "never expires"
			} else {
				expiresAt = time.Now().Add(time.Duration(*req.TTLSeconds) * time.Second)
			}
		case req.ExpiresAt != nil:
			raw := string(*req.ExpiresAt)
			if raw == "null" {
				expiresAt = time.Time{}
			} else {
				var ts time.Time
				if err := json.Unmarshal(*req.ExpiresAt, &ts); err != nil {
					writeJSONError(w, http.StatusBadRequest, "expires_at must be an RFC3339 timestamp or null")
					return
				}
				expiresAt = ts.UTC()
			}
		}
		if err := s.store.APIKeys().UpdateExpiresAt(r.Context(), id, expiresAt); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if expiresAt.IsZero() {
			resp["expires_at"] = nil
		} else {
			resp["expires_at"] = expiresAt
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// listBudgets returns every budget currently attached to the given
// API key id. Lazily rolls expired windows before reading so the
// dashboard sees fresh current_value / reset_at fields.
func (s *Server) listBudgets(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	_ = s.store.Budgets().ResetExpired(r.Context(), id, time.Now())
	bs, err := s.store.Budgets().ListByKey(r.Context(), id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if bs == nil {
		bs = []store.Budget{}
	}
	writeJSON(w, http.StatusOK, bs)
}

// createBudget attaches a new spend/token budget to an API key.
//
//	POST /admin/v1/tokens/{id}/budgets
//	  { "window": "day|week|month", "limit_unit": "tokens|usd", "limit_value": 100 }
func (s *Server) createBudget(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	id := chi.URLParam(r, "id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "token id required")
		return
	}
	var req struct {
		Window     string  `json:"window"`
		LimitUnit  string  `json:"limit_unit"`
		LimitValue float64 `json:"limit_value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	switch req.Window {
	case "day", "week", "month":
	default:
		writeJSONError(w, http.StatusBadRequest, "window must be day|week|month")
		return
	}
	switch req.LimitUnit {
	case "tokens", "usd":
	default:
		writeJSONError(w, http.StatusBadRequest, "limit_unit must be tokens|usd")
		return
	}
	if req.LimitValue <= 0 {
		writeJSONError(w, http.StatusBadRequest, "limit_value must be > 0")
		return
	}
	b := store.Budget{
		APIKeyID:   id,
		Window:     req.Window,
		LimitUnit:  req.LimitUnit,
		LimitValue: req.LimitValue,
	}
	bid, err := s.store.Budgets().Create(r.Context(), b)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	b.ID = bid
	b.ResetAt = store.NextBudgetReset(b.Window, time.Now())
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) deleteBudget(w http.ResponseWriter, r *http.Request) {
	bidStr := chi.URLParam(r, "bid")
	bid, err := strconv.ParseInt(bidStr, 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid budget id")
		return
	}
	if err := s.store.Budgets().Delete(r.Context(), bid); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "removed", "id": bid})
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.APIKeys().Revoke(r.Context(), id); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked", "id": id})
}

// ---- config view ----

// getConfig returns a sanitized view of the effective config (secrets
// redacted). Editing is file-based — too easy to brick a running cluster
// via a typo'd HTTP PUT.
func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	type view struct {
		Listen        string            `json:"listen"`
		ExternalURL   string            `json:"external_url"`
		DataDir       string            `json:"data_dir"`
		LogLevel      string            `json:"log_level"`
		Engine        map[string]string `json:"engine"`
		Router        map[string]any    `json:"router"`
		Storage       map[string]string `json:"storage"`
		Auth          map[string]any    `json:"auth"`
		Observability map[string]any    `json:"observability"`
		Egress        map[string]any    `json:"egress"`
		EditHint      string            `json:"edit_hint"`
	}
	v := view{
		Listen:      s.cfg.Listen,
		ExternalURL: s.cfg.ExternalURL,
		DataDir:     s.cfg.DataDir,
		LogLevel:    s.cfg.LogLevel,
		Engine: map[string]string{
			"preferred":         s.cfg.Engine.Preferred,
			"ollama_endpoint":   s.cfg.Engine.OllamaEndpoint,
			"vllm_endpoint":     s.cfg.Engine.VLLMEndpoint,
			"vllm_api_key":      redact(s.cfg.Engine.VLLMAPIKey),
			"mlx_endpoint":      s.cfg.Engine.MLXEndpoint,
			"llamacpp_endpoint": s.cfg.Engine.LlamaCppEndpoint,
			"whisper_endpoint":  s.cfg.Engine.WhisperEndpoint,
			"piper_endpoint":    s.cfg.Engine.PiperEndpoint,
		},
		Router: map[string]any{
			"default_model":   s.cfg.Router.DefaultModel,
			"sticky_sessions": s.cfg.Router.StickySessions,
			"fallback": map[string]any{
				"enabled":       s.cfg.Router.Fallback.Enabled,
				"anthropic_url": s.cfg.Router.Fallback.AnthropicURL,
				"openai_url":    s.cfg.Router.Fallback.OpenAIURL,
				"anthropic_key": redact(s.cfg.Router.Fallback.AnthropicKey),
				"openai_key":    redact(s.cfg.Router.Fallback.OpenAIKey),
			},
		},
		Storage: map[string]string{
			"type":       s.cfg.Storage.Type,
			"dsn":        s.cfg.Storage.DSN,
			"models_dir": s.cfg.Storage.ModelsDir,
		},
		Auth: map[string]any{
			"require_keys": s.cfg.Auth.RequireKeys,
		},
		Observability: map[string]any{
			"otlp_endpoint":  s.cfg.Observability.OTLPEndpoint,
			"otlp_status":    otlpStatus(s.cfg.Observability.OTLPEndpoint),
			"callbacks":      s.cfg.Observability.Callbacks,  // names + kinds; secrets are env-expanded server-side
			"guardrails":     s.cfg.Observability.Guardrails, // mode + url; auth_key is the literal config string, may be ${ENV} unexpanded
			"response_cache": s.cfg.Observability.ResponseCache,
		},
		Egress: map[string]any{
			"bedrock_region":  s.cfg.Router.Fallback.BedrockRegion,
			"bedrock_status":  bedrockStatus(s.cfg.Router.Fallback.BedrockRegion),
			"vertex_project":  s.cfg.Router.Fallback.VertexProject,
			"vertex_location": s.cfg.Router.Fallback.VertexLocation,
			"vertex_status":   vertexStatus(s.cfg.Router.Fallback.VertexProject),
			// OpenAI-compatible hosted gateways. Status is the
			// presence of the API key (the URL has a sensible
			// default per vendor and is rarely overridden).
			"openrouter_status": presence(s.cfg.Router.Fallback.OpenRouterKey, "OPENROUTER_API_KEY"),
			"groq_status":       presence(s.cfg.Router.Fallback.GroqKey, "GROQ_API_KEY"),
			"together_status":   presence(s.cfg.Router.Fallback.TogetherKey, "TOGETHER_API_KEY"),
			"fireworks_status":  presence(s.cfg.Router.Fallback.FireworksKey, "FIREWORKS_API_KEY"),
			"cohere_status":     presence(s.cfg.Router.Fallback.CohereKey, "COHERE_API_KEY"),
			"mistral_status":    presence(s.cfg.Router.Fallback.MistralKey, "MISTRAL_API_KEY"),
			"perplexity_status": presence(s.cfg.Router.Fallback.PerplexityKey, "PERPLEXITY_API_KEY"),
		},
		EditHint: "Edit " + s.cfg.DataDir + "/config.yaml or set ANTHROPIC_API_KEY / OPENAI_API_KEY / FLOCK_* env vars, then restart flock.",
	}
	writeJSON(w, http.StatusOK, v)
}

// presence returns a one-line operator-facing summary for a vendor
// passthrough — "disabled" when the key is missing, or "configured"
// when it's set. The dashboard's egress card renders this directly
// so an operator can see at-a-glance which vendors are reachable.
func presence(key, envName string) string {
	if key == "" {
		return "disabled (set " + envName + ")"
	}
	return "configured"
}

// otlpStatus returns a one-line operator-facing summary of the tracing
// state — "disabled" if no endpoint, "configured" otherwise. We don't
// probe the collector here because that would either block the request
// or require a background prober; the operator already gets feedback
// from "tracing enabled" log line at flock up.
func otlpStatus(endpoint string) string {
	if endpoint == "" {
		return "disabled (set FLOCK_OTLP_ENDPOINT to a collector URL)"
	}
	return "configured → " + endpoint
}

// bedrockStatus mirrors otlpStatus shape for the Bedrock egress route.
// "configured" here means: the routing pipe is wired AND SigV4 signing
// is active for anthropic.* models. amazon.*/meta.*/mistral.* still 501.
func bedrockStatus(region string) string {
	if region == "" {
		return "disabled (set FLOCK_BEDROCK_REGION to enable anthropic.* via SigV4)"
	}
	return "configured → region=" + region + ", SigV4 active for anthropic.* (other families v0.7)"
}

func vertexStatus(project string) string {
	if project == "" {
		return "disabled (set FLOCK_VERTEX_PROJECT to enable ADC auth probe)"
	}
	return "configured → project=" + project + ", ADC probe active (body translation v0.7)"
}

func redact(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 10 {
		return "set (redacted)"
	}
	return s[:6] + "…" + s[len(s)-4:]
}

// ---- shard endpoints ----

func (s *Server) listShards(w http.ResponseWriter, r *http.Request) {
	shs, err := s.store.Shards().List(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, shs)
}

// listShardProcesses returns the leader's supervisor view of every process
// it manages, keyed by ProcessID. The dashboard joins this against the
// shard rows from /shards to surface Restarts + the live runtime status
// (which can be running | starting | stopped | failed | crashloop). For
// shards that run on a worker (rpc-server, or a coordinator placed on a
// non-leader host), the leader doesn't see the process directly — the
// dashboard renders "—" for those.
func (s *Server) listShardProcesses(w http.ResponseWriter, r *http.Request) {
	if s.orch == nil || s.orch.Supervisor == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	writeJSON(w, http.StatusOK, s.orch.Supervisor.List())
}

func (s *Server) createShards(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var req struct {
		ModelID string `json:"model_id"`
		Shards  int    `json:"shards"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	entry := models.FindByID(s.cat, req.ModelID)
	if entry == nil {
		writeJSONError(w, http.StatusNotFound, "no catalog entry for "+req.ModelID)
		return
	}
	if s.orch == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "sharding orchestrator not configured")
		return
	}
	if err := s.orch.CreateSharded(r.Context(), *entry, req.Shards); err != nil {
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.router.InvalidateModel(req.ModelID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready", "model_id": req.ModelID})
}

func (s *Server) deleteShards(w http.ResponseWriter, r *http.Request) {
	modelID := chi.URLParam(r, "model_id")
	if modelID == "" {
		writeJSONError(w, http.StatusBadRequest, "model_id required")
		return
	}
	if s.orch == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "sharding orchestrator not configured")
		return
	}
	if err := s.orch.RemoveSharded(r.Context(), modelID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.router.InvalidateModel(modelID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed", "model_id": modelID})
}

// extractBearer pulls the token out of the Authorization header (Bearer
// prefix optional) or the x-api-key header.
func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h != "" {
		if len(h) > 7 && h[:7] == "Bearer " {
			return h[7:]
		}
		return h
	}
	return r.Header.Get("X-Api-Key")
}

func (s *Server) listInstalledModels(w http.ResponseWriter, r *http.Request) {
	ms, err := s.store.Models().List(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ms)
}

func (s *Server) listUsageRecent(w http.ResponseWriter, r *http.Request) {
	us, err := s.store.Usage().Recent(r.Context(), 200)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, us)
}

// usageBreakdown serves time-bucketed aggregates of the usage table.
//
//	GET /admin/v1/usage/breakdown
//	  ?bucket=hour|day|month|total      (default: day)
//	  &since=YYYY-MM-DD                  (default: 30 days ago)
//	  &until=YYYY-MM-DD                  (default: now)
//	  &group_by=user,model,protocol,outcome   (comma-separated, any order)
//	  &limit=N                           (0 = no cap)
//
// Response:
//
//	{
//	  "rows":   [{"bucket":"2026-06-09","user":"alice","model":"qwen3.6-27b","prompt_tokens":12345,"completion_tokens":6789,"requests":42}, …],
//	  "totals": {"prompt_tokens":…, "completion_tokens":…, "requests":…}
//	}
func (s *Server) usageBreakdown(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	opts := store.BreakdownOpts{
		Bucket: q.Get("bucket"),
	}
	if v := q.Get("since"); v != "" {
		t, err := parseDateFlexible(v)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid since: "+err.Error())
			return
		}
		opts.Since = t
	}
	if v := q.Get("until"); v != "" {
		t, err := parseDateFlexible(v)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid until: "+err.Error())
			return
		}
		opts.Until = t
	}
	if v := q.Get("group_by"); v != "" {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				opts.GroupBy = append(opts.GroupBy, p)
			}
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			opts.Limit = n
		}
	}
	rows, totals, err := s.store.Usage().Breakdown(r.Context(), opts)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rows":   rows,
		"totals": totals,
		"bucket": opts.Bucket,
		"since":  opts.Since,
		"until":  opts.Until,
	})
}

// parseDateFlexible accepts either a YYYY-MM-DD or an RFC3339 timestamp.
// Dates are interpreted in UTC at midnight so "since=2026-06-01" matches
// "the start of June 1".
func parseDateFlexible(s string) (time.Time, error) {
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("expected YYYY-MM-DD or RFC3339, got %q", s)
}

func (s *Server) listAuditRecent(w http.ResponseWriter, r *http.Request) {
	es, err := s.store.Audit().Recent(r.Context(), 200)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, es)
}

// usageSummary computes aggregate stats from the last 1000 usage rows:
// total requests, top models, p50/p95/p99 latency, error rate, and a
// 60-minute requests-per-minute series suitable for a sparkline. All
// computed in-memory; the table is small enough that this stays cheap.
func (s *Server) usageSummary(w http.ResponseWriter, r *http.Request) {
	us, err := s.store.Usage().Recent(r.Context(), 1000)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type modelStat struct {
		Model   string  `json:"model"`
		Count   int     `json:"count"`
		CostUSD float64 `json:"cost_usd"`
	}
	out := struct {
		Total       int         `json:"total"`
		TokensTotal int64       `json:"tokens_total"`
		CostUSD     float64     `json:"cost_usd_total"`
		CostToday   float64     `json:"cost_usd_today"`
		ErrorRate   float64     `json:"error_rate"`
		P50MS       int         `json:"p50_ms"`
		P95MS       int         `json:"p95_ms"`
		P99MS       int         `json:"p99_ms"`
		TopModels   []modelStat `json:"top_models"`
		RPM60Min    []int       `json:"rpm_60min"`
		SinceTS     *time.Time  `json:"since_ts,omitempty"`
		UntilTS     *time.Time  `json:"until_ts,omitempty"`
	}{
		Total:     len(us),
		TopModels: []modelStat{},
		RPM60Min:  make([]int, 60),
	}
	if len(us) == 0 {
		writeJSON(w, http.StatusOK, out)
		return
	}

	latencies := make([]int, 0, len(us))
	modelCounts := map[string]int{}
	modelCost := map[string]float64{}
	var errCount int
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	for _, u := range us {
		out.TokensTotal += int64(u.PromptTokens) + int64(u.CompletionTokens)
		out.CostUSD += u.CostUSD
		if !u.TS.Before(todayStart) {
			out.CostToday += u.CostUSD
		}
		modelCounts[u.Model]++
		modelCost[u.Model] += u.CostUSD
		latencies = append(latencies, u.LatencyMS)
		switch strings.ToLower(u.Outcome) {
		case "error", "failed", "timeout", "cancelled":
			errCount++
		}
		if ago := now.Sub(u.TS); ago >= 0 && ago < 60*time.Minute {
			bucket := 59 - int(ago.Minutes())
			if bucket >= 0 && bucket < 60 {
				out.RPM60Min[bucket]++
			}
		}
	}
	sort.Ints(latencies)
	pct := func(p float64) int {
		idx := int(float64(len(latencies)) * p / 100.0)
		if idx >= len(latencies) {
			idx = len(latencies) - 1
		}
		return latencies[idx]
	}
	out.P50MS = pct(50)
	out.P95MS = pct(95)
	out.P99MS = pct(99)
	out.ErrorRate = float64(errCount) / float64(len(us))

	for m, c := range modelCounts {
		out.TopModels = append(out.TopModels, modelStat{Model: m, Count: c, CostUSD: modelCost[m]})
	}
	sort.Slice(out.TopModels, func(i, j int) bool { return out.TopModels[i].Count > out.TopModels[j].Count })
	if len(out.TopModels) > 5 {
		out.TopModels = out.TopModels[:5]
	}

	// Usage rows come back newest-first; until = first row, since = last.
	since := us[len(us)-1].TS
	until := us[0].TS
	out.SinceTS = &since
	out.UntilTS = &until

	writeJSON(w, http.StatusOK, out)
}

// auditSummary aggregates the last 1000 audit entries into a compact
// "who's doing what" view: top actors, top actions, total entries.
func (s *Server) auditSummary(w http.ResponseWriter, r *http.Request) {
	es, err := s.store.Audit().Recent(r.Context(), 1000)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type countStat struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	out := struct {
		Total      int         `json:"total"`
		TopActors  []countStat `json:"top_actors"`
		TopActions []countStat `json:"top_actions"`
		SinceTS    *time.Time  `json:"since_ts,omitempty"`
		UntilTS    *time.Time  `json:"until_ts,omitempty"`
	}{
		Total:      len(es),
		TopActors:  []countStat{},
		TopActions: []countStat{},
	}
	if len(es) == 0 {
		writeJSON(w, http.StatusOK, out)
		return
	}
	actorCounts := map[string]int{}
	actionCounts := map[string]int{}
	for _, e := range es {
		actorCounts[e.Actor]++
		actionCounts[e.Action]++
	}
	for k, v := range actorCounts {
		out.TopActors = append(out.TopActors, countStat{Name: k, Count: v})
	}
	sort.Slice(out.TopActors, func(i, j int) bool { return out.TopActors[i].Count > out.TopActors[j].Count })
	if len(out.TopActors) > 5 {
		out.TopActors = out.TopActors[:5]
	}
	for k, v := range actionCounts {
		out.TopActions = append(out.TopActions, countStat{Name: k, Count: v})
	}
	sort.Slice(out.TopActions, func(i, j int) bool { return out.TopActions[i].Count > out.TopActions[j].Count })
	if len(out.TopActions) > 5 {
		out.TopActions = out.TopActions[:5]
	}
	since := es[len(es)-1].TS
	until := es[0].TS
	out.SinceTS = &since
	out.UntilTS = &until

	writeJSON(w, http.StatusOK, out)
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		s.log.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"bytes", ww.BytesWritten(),
			"dur_ms", time.Since(start).Milliseconds(),
			"req_id", middleware.GetReqID(r.Context()),
		)
	})
}

// cacheStats returns the response cache driver + counters. Returns
// 200 with a "cache disabled" sentinel when no cache is configured
// rather than a 404 — that way the dashboard's settings tab always
// gets a parseable payload.
func (s *Server) cacheStats(w http.ResponseWriter, r *http.Request) {
	c := api.ResponseCache()
	if c == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	stats := c.Stats()
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": true,
		"stats":   stats,
	})
}

// cacheFlush drops cached entries. With no query string it would be
// dangerous on a busy cache; require an explicit namespace (or `all=1`
// for the nuclear option). The audit middleware records the call.
func (s *Server) cacheFlush(w http.ResponseWriter, r *http.Request) {
	c := api.ResponseCache()
	if c == nil {
		writeJSONError(w, http.StatusOK, "cache_disabled")
		return
	}
	ns := r.URL.Query().Get("namespace")
	all := r.URL.Query().Get("all") == "1"
	if ns == "" && !all {
		writeJSONError(w, http.StatusBadRequest, "specify ?namespace=<name> or ?all=1")
		return
	}
	if all {
		// "all" is implemented as deleting the empty-string namespace
		// — every key in the memory driver ends up in some namespace
		// folder (empty namespace = no prefix), and the SQLite driver's
		// DeleteNamespace("") matches all rows with no explicit ns. For
		// the bullet-proof case we walk both possibilities.
		c.DeleteNamespace(r.Context(), "")
	}
	if ns != "" {
		c.DeleteNamespace(r.Context(), ns)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "flushed", "namespace": ns, "all": all})
}

// listCallbacks returns the names of every configured observability
// sink so an operator can confirm which ones are running.
func (s *Server) listCallbacks(w http.ResponseWriter, r *http.Request) {
	out := []map[string]string{}
	if s.callbacks != nil {
		for _, sink := range s.callbacks.Sinks() {
			out = append(out, map[string]string{"name": sink.Name()})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sinks": out})
}

// testCallback fires a synthetic event so the operator can verify a
// receiver is wired up without waiting for real traffic. Optional
// query param `?sink=<name>` targets a single sink; otherwise every
// sink that subscribes to "test" gets a copy. The "test" event kind
// piggybacks on existing subscriptions — sinks that listen to "all"
// (empty events filter) will pick it up.
func (s *Server) testCallback(w http.ResponseWriter, r *http.Request) {
	if s.callbacks == nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "no-callbacks-configured"})
		return
	}
	target := r.URL.Query().Get("sink")
	evt := callbacks.Event{
		Kind: "test",
		Payload: map[string]any{
			"request_id": api.RequestIDFrom(r.Context()),
			"sent_by":    "/admin/v1/callbacks/test",
			"note":       "synthetic event — if you see this, the sink is reachable",
		},
	}
	fired := 0
	for _, sink := range s.callbacks.Sinks() {
		if target != "" && sink.Name() != target {
			continue
		}
		sink.Send(r.Context(), evt)
		fired++
	}
	writeJSON(w, http.StatusOK, map[string]any{"fired": fired, "target": target})
}

// auditMiddleware records every admin action.
//
// Target is set to the caller's remote address (useful for forensics) rather
// than the URL query string — RawQuery can contain secrets (?token=...) that
// would otherwise be persisted in plaintext to the audit_log table.
func (s *Server) auditMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		actor := "anonymous"
		if k := auth.KeyFrom(r.Context()); k != nil {
			actor = k.Name
		}
		action := r.Method + " " + r.URL.Path
		_ = s.store.Audit().Record(r.Context(), store.AuditEntry{
			TS: time.Now(), Actor: actor,
			Action: action,
			Target: r.RemoteAddr,
		})
		// Mirror to any configured observability callbacks. Same
		// shape as the audit_log row so a receiver doesn't need to
		// keep a separate schema.
		if s.callbacks != nil {
			s.callbacks.Publish(r.Context(), callbacks.Event{
				Kind: "audit",
				Payload: map[string]any{
					"actor":      actor,
					"action":     action,
					"target":     r.RemoteAddr,
					"request_id": api.RequestIDFrom(r.Context()),
				},
			})
		}
	})
}

// bootstrapAdminKey hands the local dashboard the admin key saved at
// `<DataDir>/admin.key` so the UI can auto-log-in without making the
// operator copy it from the terminal. See the route comment in
// `routes()` for the security model.
func (s *Server) bootstrapAdminKey(w http.ResponseWriter, r *http.Request) {
	// Reject any request that has been proxied — RealIP middleware
	// (above) rewrites RemoteAddr from these headers, so without this
	// guard a remote attacker behind a misconfigured reverse proxy
	// could pose as loopback.
	for _, h := range []string{"X-Forwarded-For", "X-Real-IP", "Forwarded", "X-Forwarded-Host"} {
		if r.Header.Get(h) != "" {
			http.NotFound(w, r)
			return
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "admin.key"))
	if err != nil {
		// "Not bootstrapped" surfaces as 404 so the UI can render the
		// CLI-recovery hint instead of mistaking it for a server error.
		http.NotFound(w, r)
		return
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"key": key})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": msg, "type": "invalid_request"}})
}
