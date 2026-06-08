// Package controlplane wires together the gateway, control-plane HTTP routes,
// and protocol adapters. It owns the chi router and the *http.Server lifecycle.
package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/hadihonarvar/flock/internal/api"
	"github.com/hadihonarvar/flock/internal/auth"
	"github.com/hadihonarvar/flock/internal/config"
	"github.com/hadihonarvar/flock/internal/engines"
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

	// OpenAI-compatible + Anthropic-compatible (auth + quota)
	r.Route("/v1", func(r chi.Router) {
		r.Use(auth.Middleware(s.store.APIKeys(), s.cfg.Auth.RequireKeys))
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

			// Tokens
			r.Get("/tokens", s.listTokens)
			r.Post("/tokens", s.createToken)
			r.Delete("/tokens/{id}", s.revokeToken)

			// Observability
			r.Get("/usage/recent", s.listUsageRecent)
			r.Get("/audit/recent", s.listAuditRecent)

			// Shards
			r.Get("/shards", s.listShards)
			r.Get("/shards/processes", s.listShardProcesses)
			r.Post("/shards/create", s.createShards)
			r.Delete("/shards/{model_id}", s.deleteShards)

			// Config (read-only sanitized view)
			r.Get("/config", s.getConfig)

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
	entry := models.FindByID(s.cat, req.ID)
	if entry == nil {
		writeJSONError(w, http.StatusNotFound, "no catalog entry for "+req.ID)
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
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed", "id": id, "kind": "local"})
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
			Revoked:          k.Revoked, CreatedAt: k.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var req struct {
		Name             string `json:"name"`
		Scope            string `json:"scope"` // admin | user | node
		UserID           string `json:"user_id"`
		QuotaDailyTokens int64  `json:"quota_daily_tokens"`
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
	if err := s.store.APIKeys().Create(r.Context(), rec); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":         rec.ID,
		"name":       rec.Name,
		"scope":      rec.Scope,
		"plaintext":  plain, // shown ONCE; caller must save it now
		"created_at": rec.CreatedAt,
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
		Listen      string            `json:"listen"`
		ExternalURL string            `json:"external_url"`
		DataDir     string            `json:"data_dir"`
		LogLevel    string            `json:"log_level"`
		Engine      map[string]string `json:"engine"`
		Router      map[string]any    `json:"router"`
		Storage     map[string]string `json:"storage"`
		Auth        map[string]any    `json:"auth"`
		EditHint    string            `json:"edit_hint"`
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
		EditHint: "Edit " + s.cfg.DataDir + "/config.yaml or set ANTHROPIC_API_KEY / OPENAI_API_KEY / FLOCK_* env vars, then restart flock.",
	}
	writeJSON(w, http.StatusOK, v)
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": msg, "type": "invalid_request"}})
}
