package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/hadihonarvar/flock/internal/auth"
	"github.com/hadihonarvar/flock/internal/engines"
	"github.com/hadihonarvar/flock/internal/metrics"
	"github.com/hadihonarvar/flock/internal/store"
)

// recordUsage writes a usage row for a completed request and updates metrics.
// Best-effort — failures are not surfaced to the caller (the request already
// completed successfully from the user's perspective).
func recordUsage(ctx context.Context, st store.Store, protocol, model string,
	u *engines.Usage, latency time.Duration, outcome string) {

	keyRef := auth.KeyFrom(ctx)
	if keyRef == nil {
		return
	}
	rec := store.Usage{
		TS:        time.Now(),
		APIKeyID:  keyRef.ID,
		UserID:    keyRef.UserID,
		Model:     model,
		Protocol:  protocol,
		LatencyMS: int(latency.Milliseconds()),
		Outcome:   outcome,
	}
	if u != nil {
		rec.PromptTokens = u.PromptTokens
		rec.CompletionTokens = u.CompletionTokens
	}
	if err := st.Usage().Record(ctx, rec); err != nil {
		// swallow — store outage should not affect user-visible behavior
		_ = err
	}
	metrics.ObserveRequest(model, protocol, outcome, latency, rec.PromptTokens, rec.CompletionTokens)
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
