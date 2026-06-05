// Egress proxies — when a request asks for a model owned by a third-party
// vendor (Anthropic, OpenAI), Flock forwards the request to that vendor's API
// using a team-scoped key, logs the call via usage + audit, and returns the
// vendor's response transparently.
//
// Configuration lives in config.Router.Fallback (or env vars at minimum).
package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hadihonarvar/flock/internal/auth"
	"github.com/hadihonarvar/flock/internal/metrics"
	"github.com/hadihonarvar/flock/internal/store"
)

// FallbackConfig is what the gateway needs to proxy to upstream vendors.
type FallbackConfig struct {
	AnthropicKey  string
	AnthropicURL  string // default https://api.anthropic.com
	OpenAIKey     string
	OpenAIURL     string // default https://api.openai.com
	EnabledModels map[string]bool
}

// Vendor returns the vendor a model name belongs to (or "" if it's local).
func Vendor(model string) string {
	switch {
	case strings.HasPrefix(model, "claude-"):
		return "anthropic"
	case strings.HasPrefix(model, "gpt-"),
		strings.HasPrefix(model, "o1"), strings.HasPrefix(model, "o3"),
		strings.HasPrefix(model, "o4"):
		return "openai"
	}
	return ""
}

// EgressHandler proxies requests to vendor APIs.
type EgressHandler struct {
	Store  store.Store
	Config FallbackConfig
}

// ServeAnthropic proxies a /v1/messages request to the real Anthropic API.
func (e *EgressHandler) ServeAnthropic(w http.ResponseWriter, r *http.Request) {
	if e.Config.AnthropicKey == "" {
		writeAnthropicError(w, http.StatusServiceUnavailable, "configuration_error",
			"Anthropic fallback not configured; set ANTHROPIC_API_KEY")
		return
	}
	url := orDefault(e.Config.AnthropicURL, "https://api.anthropic.com") + r.URL.Path
	e.proxy(w, r, url, "anthropic", map[string]string{
		"x-api-key":         e.Config.AnthropicKey,
		"anthropic-version": "2023-06-01",
		"Content-Type":      "application/json",
	})
}

// ServeOpenAI proxies a /v1/chat/completions request to the real OpenAI API.
func (e *EgressHandler) ServeOpenAI(w http.ResponseWriter, r *http.Request) {
	if e.Config.OpenAIKey == "" {
		writeJSONError(w, http.StatusServiceUnavailable, "configuration_error",
			"OpenAI fallback not configured; set OPENAI_API_KEY")
		return
	}
	url := orDefault(e.Config.OpenAIURL, "https://api.openai.com") + r.URL.Path
	e.proxy(w, r, url, "openai", map[string]string{
		"Authorization": "Bearer " + e.Config.OpenAIKey,
		"Content-Type":  "application/json",
	})
}

// proxy forwards the request body to upstream, streams the response back,
// and records usage. It does not parse the body — vendor wire formats pass
// through unchanged.
func (e *EgressHandler) proxy(w http.ResponseWriter, r *http.Request, url, vendor string, headers map[string]string) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, r.Method, url, r.Body)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "proxy_error", err.Error())
		return
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	// preserve client's Accept for SSE/non-SSE distinction
	if accept := r.Header.Get("Accept"); accept != "" {
		req.Header.Set("Accept", accept)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "upstream_error", err.Error())
		recordEgress(ctx, e.Store, vendor, "unknown", start, "error")
		return
	}
	defer resp.Body.Close()

	// Mirror response headers
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	// Stream body verbatim (handles SSE for streaming responses)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			if err != io.EOF {
				recordEgress(ctx, e.Store, vendor, "unknown", start, "error")
				return
			}
			break
		}
	}
	outcome := "ok"
	if resp.StatusCode >= 400 {
		outcome = "error"
	}
	recordEgress(ctx, e.Store, vendor, "unknown", start, outcome)
}

func recordEgress(ctx context.Context, st store.Store, vendor, model string, start time.Time, outcome string) {
	dur := time.Since(start)
	metrics.ObserveRequest(model, vendor, outcome, dur, 0, 0)
	key := auth.KeyFrom(ctx)
	if key == nil {
		return
	}
	_ = st.Usage().Record(ctx, store.Usage{
		TS: time.Now(), APIKeyID: key.ID, UserID: key.UserID,
		Model: model, Protocol: vendor,
		LatencyMS: int(dur.Milliseconds()), Outcome: outcome,
	})
	_ = st.Audit().Record(ctx, store.AuditEntry{
		TS: time.Now(), Actor: key.UserID,
		Action: fmt.Sprintf("egress.%s", vendor),
		Target: model,
	})
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
