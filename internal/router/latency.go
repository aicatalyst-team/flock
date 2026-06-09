package router

import (
	"sort"
	"sync"
	"time"
)

// LatencyConfig controls latency-aware fallback. Zero values disable
// everything; the router behaves exactly like it did before this code
// existed.
type LatencyConfig struct {
	// Window is the rolling sample count per (model). Defaults to 50.
	Window int
	// P95Threshold: when a primary model's recent p95 latency exceeds
	// this, the router walks the catalog fallback chain looking for a
	// faster candidate to try FIRST. Zero (or negative) disables — the
	// latency tracker still records samples (cheap, useful for traces +
	// future metrics) but never reorders the chain.
	P95Threshold time.Duration
}

// ringBuf is a fixed-capacity ring of latency observations. Both push
// and snapshot are O(N) only at most once — push is O(1), snapshot
// allocates and copies the current window in one pass.
type ringBuf struct {
	data []time.Duration
	head int  // next write index
	full bool // true once the buffer has wrapped
}

func newRingBuf(n int) *ringBuf {
	if n <= 0 {
		n = 50
	}
	return &ringBuf{data: make([]time.Duration, n)}
}

func (r *ringBuf) push(d time.Duration) {
	r.data[r.head] = d
	r.head++
	if r.head >= len(r.data) {
		r.head = 0
		r.full = true
	}
}

// snapshot returns the current window in chronological order, oldest
// first. Callers can mutate the result freely.
func (r *ringBuf) snapshot() []time.Duration {
	if !r.full {
		out := make([]time.Duration, r.head)
		copy(out, r.data[:r.head])
		return out
	}
	out := make([]time.Duration, len(r.data))
	n := copy(out, r.data[r.head:])
	copy(out[n:], r.data[:r.head])
	return out
}

// latencyStats is a per-model rolling window of observations. Backed by
// one ring per model so push is O(1) regardless of window size.
type latencyStats struct {
	mu      sync.RWMutex
	cfg     LatencyConfig
	samples map[string]*ringBuf
}

func newLatencyStats(cfg LatencyConfig) *latencyStats {
	if cfg.Window <= 0 {
		cfg.Window = 50
	}
	return &latencyStats{cfg: cfg, samples: map[string]*ringBuf{}}
}

func (s *latencyStats) record(model string, d time.Duration) {
	if model == "" || d <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rb, ok := s.samples[model]
	if !ok {
		rb = newRingBuf(s.cfg.Window)
		s.samples[model] = rb
	}
	rb.push(d)
}

// p95 returns the 95th percentile of the rolling window. Returns 0 if
// fewer than 5 samples (too small to be meaningful).
func (s *latencyStats) p95(model string) time.Duration {
	s.mu.RLock()
	rb := s.samples[model]
	s.mu.RUnlock()
	if rb == nil {
		return 0
	}
	xs := rb.snapshot()
	if len(xs) < 5 {
		return 0
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	// 95th percentile by nearest-rank: ceil(0.95 * N) - 1 (0-indexed).
	idx := (95*len(xs))/100 - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(xs) {
		idx = len(xs) - 1
	}
	return xs[idx]
}

// reorderByLatency, given the chain returned by resolveChain, optionally
// rearranges it so the fastest candidate is tried first when the primary
// is currently slow. When latency tracking is disabled (threshold ≤ 0) or
// the primary's p95 is under the threshold, the chain is returned
// unchanged.
//
// The relative order of the non-primary candidates is preserved — we just
// swap them in front of a slow primary. This matters because the catalog
// fallback list is itself an ordered preference (degrades to smaller /
// older / cheaper models down the list).
func (s *latencyStats) reorderByLatency(chain []string) ([]string, bool) {
	if s == nil || s.cfg.P95Threshold <= 0 || len(chain) < 2 {
		return chain, false
	}
	primary := chain[0]
	primaryP95 := s.p95(primary)
	if primaryP95 == 0 || primaryP95 <= s.cfg.P95Threshold {
		return chain, false
	}
	// Primary is slow. Find the candidate (in the fallback list) with the
	// lowest p95 that we have *any* data on. If none of the fallbacks have
	// been exercised, fall back to the original order — we have nothing
	// to compare against and shouldn't blindly skip the primary.
	bestIdx := -1
	bestP95 := primaryP95 // strictly improve
	for i := 1; i < len(chain); i++ {
		p := s.p95(chain[i])
		if p > 0 && p < bestP95 {
			bestIdx = i
			bestP95 = p
		}
	}
	if bestIdx < 0 {
		return chain, false
	}
	// Move chain[bestIdx] to front; shift the rest back. Original primary
	// drops to position 1 (we still try it eventually if the faster
	// candidate also fails — recovery path).
	reordered := make([]string, 0, len(chain))
	reordered = append(reordered, chain[bestIdx])
	for i := 0; i < len(chain); i++ {
		if i == bestIdx {
			continue
		}
		reordered = append(reordered, chain[i])
	}
	return reordered, true
}
