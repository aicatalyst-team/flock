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
	AnthropicKey string
	AnthropicURL string // default https://api.anthropic.com
	OpenAIKey    string
	OpenAIURL    string // default https://api.openai.com

	// Bedrock (AWS) — model names like `anthropic.claude-*` or
	// `amazon.titan-*` route here when BedrockRegion is set. Auth requires
	// AWS SigV4 signing using credentials from the standard AWS chain
	// (env, shared config, instance role). Signing implementation is
	// tracked as v0.7 in ROADMAP P1 — for now Vendor() recognizes the
	// model prefix and ServeBedrock returns a 501 with a clear setup hint.
	BedrockRegion string // e.g. us-east-1; empty disables routing
	BedrockURL    string // optional override; default https://bedrock-runtime.<region>.amazonaws.com

	// Vertex (GCP) — model names like `gemini-*` route here when
	// VertexProject is set. Auth uses Application Default Credentials
	// (gcloud auth, service account JSON, or workload identity). Same
	// v0.7 story as Bedrock.
	VertexProject  string // GCP project id; empty disables routing
	VertexLocation string // e.g. us-central1; default us-central1
	VertexURL      string // optional override; default https://<location>-aiplatform.googleapis.com

	EnabledModels map[string]bool
}

// Vendor returns the vendor a model name belongs to (or "" if it's local).
// Order matters: Bedrock-flavored Anthropic model IDs like
// "anthropic.claude-3-sonnet-20240229-v1:0" carry the cloud prefix, so they
// must be matched BEFORE the plain `claude-` rule.
func Vendor(model string) string {
	switch {
	// Bedrock model IDs: "anthropic.*", "amazon.*", "meta.*", "mistral.*"
	case strings.HasPrefix(model, "anthropic."),
		strings.HasPrefix(model, "amazon."),
		strings.HasPrefix(model, "meta."),
		strings.HasPrefix(model, "mistral."),
		strings.HasPrefix(model, "ai21."),
		strings.HasPrefix(model, "cohere."):
		return "bedrock"
	// Vertex Gemini IDs: "gemini-*", "publishers/google/models/gemini-*"
	case strings.HasPrefix(model, "gemini-"),
		strings.Contains(model, "publishers/google/"):
		return "vertex"
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

// ServeBedrock proxies a /v1/messages request to AWS Bedrock for an
// `anthropic.*` model. The body format Bedrock-Anthropic expects is
// identical to Anthropic's own /v1/messages (Bedrock just adds SigV4
// signing), so we forward the body verbatim, signed with SigV4 via
// the standard AWS credentials chain (env, shared config, instance role).
//
// Other model families (amazon.*, meta.*, mistral.*) use Bedrock-specific
// body shapes — those return a 501 with the family-specific install hint
// until v0.7.
func (e *EgressHandler) ServeBedrock(w http.ResponseWriter, r *http.Request) {
	if e.Config.BedrockRegion == "" {
		writeJSONError(w, http.StatusServiceUnavailable, "configuration_error",
			"Bedrock egress not configured; set router.fallback.bedrock_region (or FLOCK_BEDROCK_REGION)")
		return
	}
	start := time.Now()
	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "read_error", err.Error())
		recordEgress(r.Context(), e.Store, "bedrock", "unknown", start, "error")
		return
	}
	model := peekVendorModel(body)
	if !strings.HasPrefix(model, "anthropic.") {
		writeJSONError(w, http.StatusNotImplemented, "not_implemented",
			"Bedrock body translation for "+model+" lands in v0.7 — "+
				"only anthropic.* models work today via Bedrock egress. "+
				"In the meantime, call Bedrock directly for non-Anthropic families.")
		recordEgress(r.Context(), e.Store, "bedrock", model, start, "not_implemented")
		return
	}
	// Bedrock expects the Anthropic body shape but DOES NOT want the "model"
	// field (the model id goes in the URL path). Strip it.
	stripped, err := stripModelField(body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "body_error", err.Error())
		recordEgress(r.Context(), e.Store, "bedrock", model, start, "error")
		return
	}
	// Also Bedrock requires "anthropic_version" in the body (not a header).
	stripped = ensureAnthropicVersion(stripped)

	if err := e.invokeBedrock(w, r, model, stripped); err != nil {
		writeJSONError(w, http.StatusBadGateway, "upstream_error", err.Error())
		recordEgress(r.Context(), e.Store, "bedrock", model, start, "error")
		return
	}
	recordEgress(r.Context(), e.Store, "bedrock", model, start, "ok")
}

// ServeVertex proxies to Google Vertex AI for a `gemini-*` model. As of v0.6
// ADC auth is wired (cloud.google.com/go/auth) so the credentials chain
// works (gcloud, service account JSON, workload identity). What's NOT yet
// wired is the body-shape translation from OpenAI / Anthropic message
// format → Vertex's `generateContent` Contents shape — that's v0.7.
//
// To make the 501 actionable we mint a token to confirm ADC actually works
// against the configured project, then return the error with project id.
func (e *EgressHandler) ServeVertex(w http.ResponseWriter, r *http.Request) {
	if e.Config.VertexProject == "" {
		writeJSONError(w, http.StatusServiceUnavailable, "configuration_error",
			"Vertex egress not configured; set router.fallback.vertex_project (or FLOCK_VERTEX_PROJECT)")
		return
	}
	model := peekVendorModelFromRequest(r)
	tokenOK := e.checkVertexADC(r.Context())
	hint := "ADC auth check: "
	if tokenOK == "" {
		hint += "✓ OK (token obtained for project " + e.Config.VertexProject + ")"
	} else {
		hint += "✗ FAILED — " + tokenOK
	}
	writeJSONError(w, http.StatusNotImplemented, "not_implemented",
		"Vertex egress: "+hint+". Body translation (OpenAI/Anthropic → "+
			"Vertex generateContent) lands in v0.7. In the meantime, call "+
			"Vertex directly for "+model+".")
	recordEgress(r.Context(), e.Store, "vertex", model, time.Now(), "not_implemented")
}

// peekVendorModelFromRequest sniffs the model name from the request body
// without consuming it for downstream handlers. Cheap; used only for usage
// metering on the stubbed Bedrock/Vertex paths.
func peekVendorModelFromRequest(r *http.Request) string {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return "unknown"
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return peekVendorModel(body)
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
