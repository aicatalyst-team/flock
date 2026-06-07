// Egress proxies — when a request asks for a model owned by a third-party
// vendor (Anthropic, OpenAI), Flock forwards the request to that vendor's API
// using a team-scoped key, logs the call via usage + audit, and returns the
// vendor's response transparently.
//
// Configuration lives in config.Router.Fallback (env vars at minimum).
package api

import (
	"bytes"
	"context"
	"encoding/json"
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
		"User-Agent":        "flock/0.2",
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
		"User-Agent":    "flock/0.2",
	})
}

// hopByHopHeaders are the headers defined in RFC 7230 §6.1 that must not be
// forwarded by intermediaries. We also strip Content-Length so net/http on
// the outbound side recomputes it for our streamed response.
var hopByHopHeaders = map[string]bool{
	"Connection":          true,
	"Proxy-Connection":    true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailers":            true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
	"Content-Length":      true,
}

// proxy forwards the inbound request body to upstream, streams the response
// back, and records usage. We re-read the body into a fresh bytes.Reader so
// the outbound request carries an accurate Content-Length (the dispatcher
// wraps the body in io.NopCloser which masks the underlying *bytes.Reader
// from http.NewRequest's known-type fast-path).
func (e *EgressHandler) proxy(w http.ResponseWriter, r *http.Request, url, vendor string, headers map[string]string) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "read_error", err.Error())
		recordEgress(ctx, e.Store, vendor, peekVendorModel(body), start, "error")
		return
	}
	model := peekVendorModel(body)

	req, err := http.NewRequestWithContext(ctx, r.Method, url, bytes.NewReader(body))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "proxy_error", err.Error())
		recordEgress(ctx, e.Store, vendor, model, start, "error")
		return
	}
	req.ContentLength = int64(len(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if accept := r.Header.Get("Accept"); accept != "" {
		req.Header.Set("Accept", accept)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "upstream_error", err.Error())
		recordEgress(ctx, e.Store, vendor, model, start, "error")
		return
	}
	defer resp.Body.Close()

	// Mirror response headers EXCEPT hop-by-hop. Let net/http compute the
	// outbound framing (chunked) on its own.
	for k, vs := range resp.Header {
		if hopByHopHeaders[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		// Check for client disconnect between iterations.
		if r.Context().Err() != nil {
			recordEgress(ctx, e.Store, vendor, model, start, "cancelled")
			return
		}
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, wErr := w.Write(buf[:n]); wErr != nil {
				recordEgress(ctx, e.Store, vendor, model, start, "error")
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				recordEgress(ctx, e.Store, vendor, model, start, "error")
				return
			}
			break
		}
	}
	outcome := "ok"
	if resp.StatusCode >= 400 {
		outcome = "error"
	}
	recordEgress(ctx, e.Store, vendor, model, start, outcome)
}

// peekVendorModel decodes just the "model" field from a JSON body. Returns ""
// if absent or unparseable.
func peekVendorModel(body []byte) string {
	var m struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &m)
	if m.Model == "" {
		return "unknown"
	}
	return m.Model
}

func recordEgress(ctx context.Context, st store.Store, vendor, model string, start time.Time, outcome string) {
	dur := time.Since(start)
	metrics.ObserveRequest(model, vendor, outcome, dur, 0, 0)
	key := auth.KeyFrom(ctx)
	keyID, userID := "", ""
	if key != nil {
		keyID = key.ID
		userID = key.UserID
	}
	_ = st.Usage().Record(ctx, store.Usage{
		TS: time.Now(), APIKeyID: keyID, UserID: userID,
		Model: model, Protocol: vendor,
		LatencyMS: int(dur.Milliseconds()), Outcome: outcome,
	})
	_ = st.Audit().Record(ctx, store.AuditEntry{
		TS: time.Now(), Actor: userID,
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
