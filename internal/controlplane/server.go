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

	openaiH    *api.Handler
	anthropicH *api.AnthropicHandler
	egressH    *api.EgressHandler
}

func NewServer(cfg *config.Config, st store.Store, eng engines.Engine, cat []models.Entry, log *slog.Logger) *Server {
	openaiH := &api.Handler{
		Engine:  eng,
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
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(s.accessLog)
	r.Use(middleware.Recoverer)

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
		r.Body = io.NopCloser(bytes.NewReader(body))
		switch vendor {
		case "openai":
			s.egressH.ServeOpenAI(w, r)
		case "anthropic":
			// odd case: OpenAI-format request asking for Claude. Proxy to OpenAI; let
			// upstream complain about the model.
			s.egressH.ServeOpenAI(w, r)
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
	if vendor := api.Vendor(model); vendor == "anthropic" && s.cfg.Router.Fallback.Enabled {
		r.Body = io.NopCloser(bytes.NewReader(body))
		s.egressH.ServeAnthropic(w, r)
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
	n := store.Node{
		ID:            req.ID,
		Hostname:      req.Hostname,
		OS:            req.OS,
		Arch:          req.Arch,
		RAMGB:         req.RAMGB,
		Address:       req.Address,
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
		ID string `json:"id"`
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
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
			Target: r.URL.RawQuery,
		})
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": msg}})
}
