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
}

func NewServer(cfg *config.Config, st store.Store, eng engines.Engine, cat []models.Entry, log *slog.Logger, orch *scheduler.Orchestrator) *Server {
	routed := router.New(eng, st)
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
			AnthropicKey: cfg.Router.Fallback.AnthropicKey,
			AnthropicURL: cfg.Router.Fallback.AnthropicURL,
			OpenAIKey:    cfg.Router.Fallback.OpenAIKey,
			OpenAIURL:    cfg.Router.Fallback.OpenAIURL,
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
	router := s.routes()
	s.http = &http.Server{
		Addr:              s.cfg.Listen,
		Handler:           router,
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
	return s.http.Shutdown(ctx)
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
			r.Get("/nodes", s.listNodes)
			r.Get("/models", s.listInstalledModels)
			r.Get("/usage/recent", s.listUsageRecent)
			r.Get("/audit/recent", s.listAuditRecent)
			r.Get("/shards", s.listShards)
			r.Post("/shards/create", s.createShards)
			r.Delete("/shards/{model_id}", s.deleteShards)
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
		case "anthropic":
			// Protocol mismatch: OpenAI-format request with a Claude model.
			// Anthropic's API only accepts /v1/messages, so return an actionable
			// error rather than forwarding garbage upstream.
			writeJSONError(w, http.StatusBadRequest,
				fmt.Sprintf("model %q is an Anthropic model; use POST /v1/messages with the Anthropic SDK or set ANTHROPIC_BASE_URL on your client", model))
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
		case "openai":
			// Protocol mismatch: Anthropic-format request with an OpenAI model.
			writeJSONError(w, http.StatusBadRequest,
				fmt.Sprintf("model %q is an OpenAI model; use POST /v1/chat/completions with the OpenAI SDK", model))
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
	// NOTE: stored plaintext for v0.3 — replace with HMAC-based mutual auth
	// once the OIDC + key-management story lands.
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

// ---- shard endpoints ----

func (s *Server) listShards(w http.ResponseWriter, r *http.Request) {
	shs, err := s.store.Shards().List(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, shs)
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
