package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/hadihonarvar/flock/internal/auth"
)

// Bucket is a leaky/token bucket. capacity == fillRate * 60 because all
// caller-facing limits are per-minute. Tokens replenish linearly; a
// caller that's been idle for a minute can burst up to `capacity`
// before being rate-limited.
//
// Bucket is safe for concurrent use.
type Bucket struct {
	capacity float64 // RPM or TPM ceiling
	fillRate float64 // tokens per second (= capacity / 60)
	tokens   float64
	last     time.Time
	mu       sync.Mutex
}

// NewBucket constructs a Bucket sized for the given per-minute limit.
// Limit ≤ 0 disables rate limiting — the returned bucket always allows.
func NewBucket(limitPerMinute int) *Bucket {
	if limitPerMinute <= 0 {
		return nil
	}
	return &Bucket{
		capacity: float64(limitPerMinute),
		fillRate: float64(limitPerMinute) / 60.0,
		tokens:   float64(limitPerMinute),
		last:     time.Now(),
	}
}

// Take attempts to deduct n tokens. Returns ok=true on success;
// ok=false with the suggested retry wait when the bucket can't cover
// the request. retryAfter is rounded up to the nearest whole second so
// it fits cleanly in the `Retry-After` header.
func (b *Bucket) Take(n float64) (ok bool, retryAfter time.Duration) {
	if b == nil {
		return true, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill()
	if b.tokens >= n {
		b.tokens -= n
		return true, 0
	}
	deficit := n - b.tokens
	seconds := math.Ceil(deficit / b.fillRate)
	return false, time.Duration(seconds) * time.Second
}

// Available returns the current token count (after a lazy refill). Used
// by the rate-limit header writer to populate
// `x-ratelimit-remaining-*`. nil → +Inf so a key with no limit reports
// effectively unlimited remaining.
func (b *Bucket) Available() float64 {
	if b == nil {
		return -1
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill()
	return b.tokens
}

// Capacity returns the configured maximum tokens (per-minute limit).
// Used by the header writer to populate `x-ratelimit-limit-*`.
func (b *Bucket) Capacity() float64 {
	if b == nil {
		return -1
	}
	return b.capacity
}

// RefillETA returns the number of seconds until the bucket is fully
// refilled from its current level. Used for `x-ratelimit-reset-*`,
// which clients interpret as "wait this long for the limit to reset".
// 0 when the bucket is already full.
func (b *Bucket) RefillETA() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill()
	deficit := b.capacity - b.tokens
	if deficit <= 0 {
		return 0
	}
	return int(math.Ceil(deficit / b.fillRate))
}

// Refund returns n tokens to the bucket (used after the response when
// the upfront estimate was too generous). Capped at capacity.
func (b *Bucket) Refund(n float64) {
	if b == nil || n <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill()
	b.tokens += n
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
}

func (b *Bucket) refill() {
	now := time.Now()
	delta := now.Sub(b.last).Seconds()
	b.tokens += delta * b.fillRate
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
	b.last = now
}

// BucketStore holds per-key RPM + TPM buckets. Buckets are created on
// first use and cached for the lifetime of the process; the leader
// restart resets all counters (acceptable for v1 per the planning
// doc — persistent buckets are deferred).
type BucketStore struct {
	mu  sync.Mutex
	rpm map[string]*Bucket
	tpm map[string]*Bucket
}

// NewBucketStore returns an empty store ready to back the middleware.
func NewBucketStore() *BucketStore {
	return &BucketStore{
		rpm: make(map[string]*Bucket),
		tpm: make(map[string]*Bucket),
	}
}

// For returns (or lazily creates) the RPM and TPM buckets for keyID
// with the given limits. Limit changes since last call are honored by
// rebuilding the bucket — the cached counter would otherwise represent
// the old capacity.
func (s *BucketStore) For(keyID string, rpmLimit, tpmLimit int) (rpm, tpm *Bucket) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rpmLimit > 0 {
		if b := s.rpm[keyID]; b == nil || int(b.capacity) != rpmLimit {
			s.rpm[keyID] = NewBucket(rpmLimit)
		}
		rpm = s.rpm[keyID]
	} else {
		// Limit removed: drop the bucket so the next set is a fresh start.
		delete(s.rpm, keyID)
	}
	if tpmLimit > 0 {
		if b := s.tpm[keyID]; b == nil || int(b.capacity) != tpmLimit {
			s.tpm[keyID] = NewBucket(tpmLimit)
		}
		tpm = s.tpm[keyID]
	} else {
		delete(s.tpm, keyID)
	}
	return rpm, tpm
}

// Get returns the current RPM + TPM buckets for keyID *without*
// rebuilding. Returns nil for either bucket when no limit was ever
// configured (or it was cleared). Used by the post-response
// reconciliation path, which doesn't know the current limit but needs
// to find the bucket if it exists.
func (s *BucketStore) Get(keyID string) (rpm, tpm *Bucket) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rpm[keyID], s.tpm[keyID]
}

// RateLimitMiddleware enforces per-key RPM (requests per minute) and
// TPM (tokens per minute) ceilings using in-memory leaky buckets.
//
// On admit:
//   - Deduct 1 from the RPM bucket.
//   - Deduct an upfront token estimate from the TPM bucket. We use the
//     char/4 heuristic over the JSON body — rough, but close enough
//     for streaming where the real prompt size is unknowable without
//     parsing per-protocol.
//
// On overflow → HTTP 429 `rate_limited` with `Retry-After` set to the
// shorter of the two bucket wait times. Caller is expected to honor
// the header before retrying.
//
// Reconciliation between estimate and actual completion tokens is
// handled in recordUsage (best-effort refund / deduct).
func RateLimitMiddleware(buckets *BucketStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := auth.KeyFrom(r.Context())
			if key == nil || (key.RPMLimit == 0 && key.TPMLimit == 0) {
				next.ServeHTTP(w, r)
				return
			}
			rpm, tpm := buckets.For(key.ID, key.RPMLimit, key.TPMLimit)

			if r.Method != http.MethodPost {
				next.ServeHTTP(w, r)
				return
			}
			body, err := io.ReadAll(r.Body)
			_ = r.Body.Close()
			if err != nil {
				writeJSONErr(w, http.StatusBadRequest, "invalid_request", "read body: "+err.Error())
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))

			estimate := estimateTokens(body)
			// Stash the estimate on the request context so recordUsage
			// can refund / borrow once the real completion-tokens are
			// known. We attach this *before* deducting so a 429 path
			// doesn't poison ctx with a stale figure.
			r = r.WithContext(WithRateLimitEstimate(r.Context(), key.ID, estimate))

			if ok, retry := rpm.Take(1); !ok {
				writeRateLimited(w, retry, "rpm", key.RPMLimit)
				return
			}
			if ok, retry := tpm.Take(float64(estimate)); !ok {
				// Refund the RPM token so the user isn't double-charged
				// for a request we never admitted.
				rpm.Refund(1)
				writeRateLimited(w, retry, "tpm", key.TPMLimit)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// estimateTokens returns a rough upfront token count for the request
// body. We use the chars/4 heuristic over the entire body — good
// enough for admit-time gating without parsing each protocol shape.
// The post-response reconciliation in recordUsage corrects the
// estimate once real usage is known.
func estimateTokens(body []byte) int {
	n := len(body) / 4
	if n < 1 {
		return 1
	}
	return n
}

func writeRateLimited(w http.ResponseWriter, retryAfter time.Duration, limitType string, limit int) {
	if retryAfter < time.Second {
		retryAfter = time.Second
	}
	secs := int(retryAfter.Seconds())
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	body := map[string]any{
		"error": map[string]any{
			"type":        "rate_limited",
			"message":     fmt.Sprintf("%s ceiling exceeded (%d/min)", limitType, limit),
			"limit_type":  limitType,
			"limit":       limit,
			"retry_after": secs,
		},
	}
	_ = json.NewEncoder(w).Encode(body)
}
