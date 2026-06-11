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
	"strings"
	"time"

	"github.com/hadihonarvar/flock/internal/api"
	"github.com/hadihonarvar/flock/internal/auth"
	"github.com/hadihonarvar/flock/internal/config"
	"github.com/hadihonarvar/flock/internal/engines"
	"github.com/hadihonarvar/flock/internal/events"
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

	router     *router.Router
	orch       *scheduler.Orchestrator
	openaiH    *api.Handler
	anthropicH *api.AnthropicHandler
	egressH    *api.EgressHandler

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
	// retry against X's catalog fallback list in order. Closure captures the
	// catalog slice — fresh lookups happen per call so a catalog reload would
	// be observed (catalog hot-reload isn't shipped yet but this leaves room).
	routed.SetFallbackResolver(func(modelID string) []string {
		entry := models.FindByID(cat, modelID)
		if entry == nil {
			return nil
		}
		return entry.Fallback
	})
	// Latency-aware fallback (Bet #1): opt-in via router.latency_fallback_p95_seconds.
	// Zero (default) leaves behavior unchanged.
	if cfg.Router.LatencyFallbackP95Seconds > 0 {
		routed.SetLatencyConfig(router.LatencyConfig{
			P95Threshold: time.Duration(cfg.Router.LatencyFallbackP95Seconds) * time.Second,
		})
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
		},
	}
	return &Server{
		cfg:        cfg,
		store:      st,
		engine:     eng,
		cat:        cat,
		log:        log,
		router:     routed,
		orch:       orch,
		openaiH:    openaiH,
		anthropicH: anthropicH,
		egressH:    egressH,
		bus:        events.New(),
	}
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
		// Per-key model allowlist runs BEFORE quota: a key with no quota
		// to spend on an unauthorized model would otherwise burn a 429
		// instead of the more accurate 403.
		r.Use(api.ModelAllowMiddleware(s.store))
		r.Use(api.QuotaMiddleware(s.store))
		r.Get("/models", s.openaiH.ListModels)
		r.Post("/chat/completions", s.dispatchOpenAIChat)
		r.Post("/embeddings", s.openaiH.Embeddings)
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

			// Tokens
			r.Get("/tokens", s.listTokens)
			r.Post("/tokens", s.createToken)
			r.Patch("/tokens/{id}", s.editToken)
			r.Delete("/tokens/{id}", s.revokeToken)

			// Observability
			r.Get("/usage/recent", s.listUsageRecent)
			r.Get("/usage/summary", s.usageSummary)
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
	writeJSON(w, http.StatusOK, nodes)
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
func (s *Server) unloadModel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
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
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Scope            string    `json:"scope"`
	UserID           string    `json:"user_id"`
	QuotaDailyTokens int64     `json:"quota_daily_tokens"`
	AllowedModels    []string  `json:"allowed_models"`
	Revoked          bool      `json:"revoked"`
	CreatedAt        time.Time `json:"created_at"`
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	keys, err := s.store.APIKeys().List(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]tokenView, 0, len(keys))
	for _, k := range keys {
		out = append(out, tokenView{
			ID: k.ID, Name: k.Name, Scope: k.Scope, UserID: k.UserID,
			QuotaDailyTokens: k.QuotaDailyTokens,
			AllowedModels:    k.AllowedModels,
			Revoked:          k.Revoked, CreatedAt: k.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var req struct {
		Name             string   `json:"name"`
		Scope            string   `json:"scope"` // admin | user | node
		UserID           string   `json:"user_id"`
		QuotaDailyTokens int64    `json:"quota_daily_tokens"`
		AllowedModels    []string `json:"allowed_models"`
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
	rec.AllowedModels = req.AllowedModels
	if err := s.store.APIKeys().Create(r.Context(), rec); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":             rec.ID,
		"name":           rec.Name,
		"scope":          rec.Scope,
		"allowed_models": rec.AllowedModels,
		"plaintext":      plain, // shown ONCE; caller must save it now
		"created_at":     rec.CreatedAt,
	})
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
	// Use a json.RawMessage so we can tell "field absent" from
	// "field present and null" — both round-trip to a nil slice in a
	// plain `[]string` field.
	var req struct {
		AllowedModels *json.RawMessage `json:"allowed_models"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if req.AllowedModels == nil {
		writeJSONError(w, http.StatusBadRequest, "no editable fields in body (try `allowed_models`)")
		return
	}
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
	writeJSON(w, http.StatusOK, map[string]any{
		"id":             id,
		"allowed_models": allowed,
	})
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
			"otlp_endpoint": s.cfg.Observability.OTLPEndpoint,
			"otlp_status":   otlpStatus(s.cfg.Observability.OTLPEndpoint),
		},
		Egress: map[string]any{
			"bedrock_region":  s.cfg.Router.Fallback.BedrockRegion,
			"bedrock_status":  bedrockStatus(s.cfg.Router.Fallback.BedrockRegion),
			"vertex_project":  s.cfg.Router.Fallback.VertexProject,
			"vertex_location": s.cfg.Router.Fallback.VertexLocation,
			"vertex_status":   vertexStatus(s.cfg.Router.Fallback.VertexProject),
		},
		EditHint: "Edit " + s.cfg.DataDir + "/config.yaml or set ANTHROPIC_API_KEY / OPENAI_API_KEY / FLOCK_* env vars, then restart flock.",
	}
	writeJSON(w, http.StatusOK, v)
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
		Model string `json:"model"`
		Count int    `json:"count"`
	}
	out := struct {
		Total       int         `json:"total"`
		TokensTotal int64       `json:"tokens_total"`
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
	var errCount int
	now := time.Now()
	for _, u := range us {
		out.TokensTotal += int64(u.PromptTokens) + int64(u.CompletionTokens)
		modelCounts[u.Model]++
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
		out.TopModels = append(out.TopModels, modelStat{Model: m, Count: c})
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
		_ = s.store.Audit().Record(r.Context(), store.AuditEntry{
			TS: time.Now(), Actor: actor,
			Action: r.Method + " " + r.URL.Path,
			Target: r.RemoteAddr,
		})
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
