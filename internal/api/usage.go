package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/hadihonarvar/flock/internal/auth"
	"github.com/hadihonarvar/flock/internal/callbacks"
	"github.com/hadihonarvar/flock/internal/engines"
	"github.com/hadihonarvar/flock/internal/metrics"
	"github.com/hadihonarvar/flock/internal/models"
	"github.com/hadihonarvar/flock/internal/store"
)

// catalogMu protects globalCatalog. Cost lookups happen per-request
// from concurrent handlers; the catalog only changes on
// SetCatalog (called once from the server constructor today, but the
// mutex leaves room for a future hot-reload).
var (
	catalogMu     sync.RWMutex
	globalCatalog []models.Entry
)

// SetCatalog wires the loaded catalog into the recordUsage path so it
// can compute per-call dollar cost via models.CostOf. Safe to call
// multiple times (e.g. on catalog reload) — the swap is atomic.
func SetCatalog(cat []models.Entry) {
	catalogMu.Lock()
	globalCatalog = cat
	catalogMu.Unlock()
}

func getCatalog() []models.Entry {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	return globalCatalog
}

// rateLimitEstimateKey is unexported so other packages can't shadow the
// estimate (which is meaningful only to the rate-limit reconciliation
// path).
type rateLimitEstimateKey struct{}

// rateLimitEstimate is the upfront token count attached to a request
// by RateLimitMiddleware. recordUsage reads it to reconcile against
// the actual prompt+completion tokens once the response is done.
type rateLimitEstimate struct {
	KeyID    string
	Estimate int
}

// WithRateLimitEstimate stashes the upfront estimate on ctx for the
// downstream recordUsage reconciliation step. Exported so the
// middleware (in the same package, but kept callable for tests too) can
// build the context.
func WithRateLimitEstimate(ctx context.Context, keyID string, estimate int) context.Context {
	return context.WithValue(ctx, rateLimitEstimateKey{}, rateLimitEstimate{KeyID: keyID, Estimate: estimate})
}

// rateLimitEstimateFrom reads the stashed estimate. Returns the zero
// value when no estimate was set — the reconciliation step is then a
// no-op.
func rateLimitEstimateFrom(ctx context.Context) rateLimitEstimate {
	v, _ := ctx.Value(rateLimitEstimateKey{}).(rateLimitEstimate)
	return v
}

// globalBucketStore holds the per-process bucket map so middleware
// instances and the recordUsage reconciliation point share state.
// nil until SetBucketStore is called from the server wiring.
var globalBucketStore *BucketStore

// SetBucketStore wires the per-process bucket store. Called from the
// control plane at startup; recordUsage uses it to refund / deduct
// based on actual completion tokens vs the upfront estimate.
func SetBucketStore(s *BucketStore) { globalBucketStore = s }

// globalCallbackDispatcher is the per-process fan-out for observability
// events. nil = no sinks configured; Publish is a no-op.
var globalCallbackDispatcher *callbacks.Dispatcher

// SetCallbackDispatcher wires the dispatcher so recordUsage can emit
// usage events to webhooks / Langfuse / etc.
func SetCallbackDispatcher(d *callbacks.Dispatcher) { globalCallbackDispatcher = d }

// CallbackDispatcher returns the configured dispatcher (or nil).
// Exposed so the audit middleware in the controlplane package can
// publish "audit" events without re-importing the global.
func CallbackDispatcher() *callbacks.Dispatcher { return globalCallbackDispatcher }

// recordUsage writes a usage row for a completed request and updates metrics.
// Best-effort — failures are not surfaced to the caller (the request already
// completed successfully from the user's perspective).
//
// Metrics always fire (even when no API key is in context — e.g., dev mode
// with require_keys=false). The DB row is written with empty key/user
// identifiers in that case; the per-key index simply has more empty-string
// rows but everything stays observable.
func recordUsage(ctx context.Context, st store.Store, protocol, model string,
	u *engines.Usage, latency time.Duration, outcome string) {

	var keyID, userID string
	if k := auth.KeyFrom(ctx); k != nil {
		keyID = k.ID
		userID = k.UserID
	}

	prompt, completion := 0, 0
	if u != nil {
		prompt = u.PromptTokens
		completion = u.CompletionTokens
	}

	// Metrics first — always, regardless of auth state.
	metrics.ObserveRequest(model, protocol, outcome, latency, prompt, completion)

	cost := models.CostOf(model, getCatalog(), prompt, completion)
	rec := store.Usage{
		TS:               time.Now(),
		APIKeyID:         keyID,
		UserID:           userID,
		Model:            model,
		Protocol:         protocol,
		PromptTokens:     prompt,
		CompletionTokens: completion,
		LatencyMS:        int(latency.Milliseconds()),
		Outcome:          outcome,
		CostUSD:          cost,
	}
	if err := st.Usage().Record(ctx, rec); err != nil {
		// swallow — store outage should not affect user-visible behavior
		_ = err
	}

	// Bump the per-key budgets for the actual cost and token count.
	// Best-effort: a write failure here is logged but doesn't surface
	// to the caller. The pre-check on the next request will reject
	// once the running total catches up.
	if keyID != "" {
		incrementBudgetsAfterUsage(ctx, st, keyID, int64(prompt+completion), cost)
	}

	// Publish the usage event to any configured observability sinks
	// (webhook / Langfuse / etc). Non-blocking; a slow receiver can't
	// stall the response path.
	if globalCallbackDispatcher != nil {
		globalCallbackDispatcher.Publish(ctx, callbacks.Event{
			Kind: "usage",
			Payload: map[string]any{
				"request_id":        RequestIDFrom(ctx),
				"key_id":            keyID,
				"user_id":           userID,
				"model":             model,
				"protocol":          protocol,
				"prompt_tokens":     float64(prompt),
				"completion_tokens": float64(completion),
				"latency_ms":        latency.Milliseconds(),
				"outcome":           outcome,
				"cost_usd":          cost,
			},
		})
	}

	// Reconcile the rate-limit TPM bucket. The middleware deducted an
	// upfront estimate; once the real usage is known we either refund
	// (over-estimated) or deduct the delta (under-estimated). The
	// bucket can go briefly negative — that's fine; subsequent
	// requests refill and rate-limit normally.
	if est := rateLimitEstimateFrom(ctx); est.KeyID != "" && globalBucketStore != nil {
		actual := prompt + completion
		if _, tpm := globalBucketStore.Get(est.KeyID); tpm != nil {
			switch {
			case actual > est.Estimate:
				tpm.Refund(-float64(actual - est.Estimate))
			case actual < est.Estimate:
				tpm.Refund(float64(est.Estimate - actual))
			}
		}
	}
}

// QuotaMiddleware enforces per-key daily token quotas. Keys with quota=0
// are unlimited.
func QuotaMiddleware(st store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := auth.KeyFrom(r.Context())
			if key == nil || key.QuotaDailyTokens <= 0 {
				next.ServeHTTP(w, r)
				return
			}
			midnight := startOfUTCDay(time.Now())
			used, err := st.Usage().SumTokensSince(r.Context(), key.ID, midnight)
			if err == nil && used >= key.QuotaDailyTokens {
				writeQuotaExceeded(w, key.QuotaDailyTokens, used)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func startOfUTCDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func writeQuotaExceeded(w http.ResponseWriter, quota, used int64) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "3600")
	w.WriteHeader(http.StatusTooManyRequests)
	body := map[string]any{
		"error": map[string]any{
			"type":    "rate_limit_error",
			"message": "Daily token quota exceeded",
			"quota":   quota,
			"used":    used,
		},
	}
	_ = json.NewEncoder(w).Encode(body)
}
