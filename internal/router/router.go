// Package router picks a backing engine for each inference request based on
// model placements across the cluster. It implements the engines.Engine
// interface so the rest of the codebase doesn't need to know whether a
// request is served locally or proxied to a worker.
//
// Selection policy:
//
//  1. If the local engine has the model loaded, use local (lowest latency).
//  2. Otherwise look up all worker nodes that have the model loaded.
//  3. Among those, pick the one with the fewest in-flight requests.
//  4. If no node has the model, fall through to local — the local engine
//     will return a "model not found" error which surfaces correctly.
//
// Remote engines reuse the vLLM driver (workers expose an OpenAI-compatible
// surface, just like vLLM/MLX). Engines are cached per node so we don't
// rebuild them on every request.
package router

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hadihonarvar/flock/internal/auth"
	"github.com/hadihonarvar/flock/internal/engines"
	"github.com/hadihonarvar/flock/internal/metrics"
	"github.com/hadihonarvar/flock/internal/store"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// tracer is package-scoped so spans created here all carry the same
// instrumentation-library name; the global TracerProvider (set in
// internal/controlplane/tracing.go) decides whether they're exported
// or no-op'd.
var tracer trace.Tracer = otel.Tracer("github.com/hadihonarvar/flock/internal/router")

// FallbackChains carries the per-class fallback lists for a single
// primary model. Generic is the catalog's `fallback:`; the typed lists
// (ContextLength, ContentPolicy) come from `fallback_on_*:`. An empty
// typed list means "use Generic" — operators only fill the typed list
// when they want a class-specific target.
type FallbackChains struct {
	Generic       []string
	ContextLength []string
	ContentPolicy []string
}

// PickFor returns the chain that matches the given ErrorClass, falling
// back to Generic when the typed list is empty (the common case for
// most catalog entries).
func (c FallbackChains) PickFor(class ErrorClass) []string {
	switch class {
	case ClassContextLength:
		if len(c.ContextLength) > 0 {
			return c.ContextLength
		}
	case ClassContentPolicy:
		if len(c.ContentPolicy) > 0 {
			return c.ContentPolicy
		}
	}
	return c.Generic
}

// FallbackResolver returns the per-class fallback chains for a primary
// model. An empty FallbackChains value means "no fallback" — Router
// behaves exactly as it did before this hook existed. Typically backed
// by a closure over the catalog's `fallback*:` fields.
type FallbackResolver func(modelID string) FallbackChains

// Router implements engines.Engine by dispatching to either the local engine
// or a remote worker engine based on cluster placements.
type Router struct {
	local     engines.Engine
	store     store.Store
	localNode string // node id used for "local" placements (typically "local")

	// log emits structured fallback / pick events. Defaults to slog.Default()
	// when SetLogger isn't called; tests can swap in a discard logger.
	log *slog.Logger

	// maxFallbackAttempts caps the number of candidates the router will walk
	// before giving up. 0 means "no cap" (legacy behavior — walk the entire
	// chain). Set via SetMaxFallbackAttempts.
	maxFallbackAttempts int

	// heartbeatMaxAge declares how stale a worker's last heartbeat can be
	// before pick() refuses to route to it. 0 means "no check" (legacy).
	// Set via SetHeartbeatMaxAge.
	heartbeatMaxAge time.Duration

	// FallbackResolver is optional. When set, Chat / Embed will retry the
	// request against each fallback model in order on retriable errors
	// (anything Engine.Chat returns synchronously). Set via
	// router.SetFallbackResolver after construction.
	fallback FallbackResolver

	// latency tracks per-model rolling p95 latency and (when the threshold
	// is non-zero) preempts a slow primary by trying a faster fallback
	// first. Always non-nil after New(); the threshold defaults to 0
	// (disabled — latencies are still recorded for traces / future
	// metrics, but no reordering). Also feeds `sort: latency|throughput`.
	latency *latencyStats

	// priceFn resolves a model id to its combined $/1K-token rate for
	// `sort: price` / `:floor`. nil disables price sorting (chain is
	// left in catalog order). Set via SetPriceResolver.
	priceFn func(modelID string) float64

	// Placement cooldown ("penalty box"): a worker node that errors
	// `placementAllowedFails` times in a row is parked for
	// `placementCooldownDur` so pick() skips it instead of routing
	// fresh requests to a flaky engine. Per-node, in-memory only;
	// reset on leader restart (the next request will re-prove the
	// node). Cooldown applies only to remote workers; the local
	// engine never enters cooldown (a flaky local engine is a
	// different operational problem).
	placementAllowedFails int
	placementCooldownDur  time.Duration

	// Sticky sessions: when stickyTTL > 0 the router pins a
	// (user_id, model) tuple to the worker that served its previous
	// successful request, so multi-turn chats reuse the same node's
	// KV cache. The entry refreshes on each successful pick and
	// expires after stickyTTL of inactivity. Bypassed for requests
	// with no user_id (`auth.KeyFrom(ctx) == nil` — typically dev
	// mode without keys) and for the synthetic `auto` model id (the
	// effective resolved model may change between turns).
	stickyTTL time.Duration

	// Hedging: when > 1 the router can fire a single request to
	// hedgeReplicas least-loaded workers concurrently and return
	// whichever responds first. Opt-in per request (via
	// Overrides.Hedge). 0 / 1 disables. Hard-capped at MaxHedgeReplicas
	// in the setter.
	hedgeReplicas int

	mu          sync.RWMutex
	inflight    map[string]int            // node_id → live request count
	inflightDim map[string]int            // node_id + "|" + model → live request count (drain waits)
	remotes     map[string]engines.Engine // node_id → cached remote engine
	cooldowns   map[string]time.Time      // node_id → time the node leaves the penalty box
	failures    map[string]int            // node_id → consecutive recent failures
	stickiness  map[string]stickyEntry    // user_id|model → pinned node + expiry
}

// stickyEntry is one row of the per-(user_id, model) pin table.
type stickyEntry struct {
	NodeID    string
	ExpiresAt time.Time
}

// New constructs a Router that wraps the local engine and consults the store
// for placements + node info.
func New(local engines.Engine, st store.Store) *Router {
	return &Router{
		local:       local,
		store:       st,
		localNode:   "local",
		log:         slog.Default(),
		inflight:    make(map[string]int),
		inflightDim: make(map[string]int),
		remotes:     make(map[string]engines.Engine),
		cooldowns:   make(map[string]time.Time),
		failures:    make(map[string]int),
		stickiness:  make(map[string]stickyEntry),
		latency:     newLatencyStats(LatencyConfig{}),
	}
}

// MaxHedgeReplicas caps the per-request fan-out so a misconfigured
// client can't burn 50× the engine cost in one call. Three is enough
// to cut tail latency without the cost getting silly.
const MaxHedgeReplicas = 3

// SetHedgeReplicas enables request hedging. When a request opts in
// via Overrides.Hedge the router fires the call to the top-N
// least-loaded workers concurrently and returns whichever responds
// first; the losers' contexts are cancelled.
//
// n ≤ 1 disables hedging entirely. Values above MaxHedgeReplicas are
// silently clamped.
func (r *Router) SetHedgeReplicas(n int) {
	if n <= 1 {
		r.hedgeReplicas = 0
		return
	}
	if n > MaxHedgeReplicas {
		n = MaxHedgeReplicas
	}
	r.hedgeReplicas = n
}

// SetStickyTTL turns on per-(user_id, model) session stickiness with
// the given TTL. The router prefers the previously-picked worker for
// each tuple until the TTL elapses without activity, so multi-turn
// chats reuse the same node's KV cache.
//
// 0 (default) disables the feature — pick() behaves exactly as before.
// Recommended range: 60s–600s. Too low and the cache benefit
// disappears between turns; too high and load can stay lopsided after
// a session ends.
func (r *Router) SetStickyTTL(d time.Duration) {
	if d >= 0 {
		r.stickyTTL = d
	}
}

// SetPlacementCooldown configures the per-node circuit-breaker. After
// `allowedFails` consecutive engine errors from the same worker, pick()
// skips the node for `cooldown` before retrying. A single success after
// cooldown expires resets the counter.
//
// Both values must be > 0 to enable the feature. Either zero (the
// default) disables it — pick() behaves exactly as before.
func (r *Router) SetPlacementCooldown(allowedFails int, cooldown time.Duration) {
	if allowedFails < 0 || cooldown < 0 {
		return
	}
	r.placementAllowedFails = allowedFails
	r.placementCooldownDur = cooldown
}

// SetLogger swaps in a structured logger for fallback + pick events.
// Defaults to slog.Default() if never called.
func (r *Router) SetLogger(l *slog.Logger) {
	if l != nil {
		r.log = l
	}
}

// SetMaxFallbackAttempts caps how many candidates Chat/Embed will try
// before giving up. 0 (default) walks the entire chain.
func (r *Router) SetMaxFallbackAttempts(n int) {
	if n >= 0 {
		r.maxFallbackAttempts = n
	}
}

// SetHeartbeatMaxAge causes pick() to refuse to dispatch to a worker
// whose last heartbeat is older than `d`. 0 (default) disables the check.
// Useful for catching dead workers before the engine call timeout fires.
func (r *Router) SetHeartbeatMaxAge(d time.Duration) {
	if d >= 0 {
		r.heartbeatMaxAge = d
	}
}

// SetFallbackResolver wires a fallback chain provider into the router.
// Callers typically pass a closure over the catalog; nil disables fallback.
func (r *Router) SetFallbackResolver(f FallbackResolver) {
	r.fallback = f
}

// SetLatencyConfig configures latency-aware fallback (Bet #1). With a
// non-zero P95Threshold, when a primary model's recent p95 latency
// exceeds the threshold, the router walks the catalog fallback chain
// for a faster candidate to try FIRST. Original primary stays in the
// chain so a fast-but-degraded fallback isn't a permanent demotion.
func (r *Router) SetLatencyConfig(cfg LatencyConfig) {
	r.latency = newLatencyStats(cfg)
}

// resolveChain returns [primary, ...generic fallback]. Kept for the
// existing call paths (tests, latency reorder) that want a pre-built
// list. For class-aware routing the call sites use chainsFor + PickFor
// after classifying the primary's failure.
//
// When no resolver is set or the model has no fallback entry, returns
// just [primary]. Bounded by SetMaxFallbackAttempts when configured.
func (r *Router) resolveChain(model string) []string {
	chains := r.chainsFor(model)
	return r.applyCap(buildChain(model, chains.Generic))
}

// chainsFor returns the typed fallback chains for `model`. A zero
// FallbackChains value (no resolver configured / no chain declared) is
// returned as-is.
func (r *Router) chainsFor(model string) FallbackChains {
	if r.fallback == nil {
		return FallbackChains{}
	}
	return r.fallback(model)
}

func buildChain(primary string, fb []string) []string {
	if len(fb) == 0 {
		return []string{primary}
	}
	out := make([]string, 0, len(fb)+1)
	out = append(out, primary)
	out = append(out, fb...)
	return out
}

// applyCap trims `chain` to MaxFallbackAttempts+1 candidates (primary
// + N fallbacks). The legacy behavior (limit=0) walks the entire chain.
func (r *Router) applyCap(chain []string) []string {
	if limit := r.maxFallbackAttempts; limit > 0 && len(chain) > limit+1 {
		chain = chain[:limit+1]
		metrics.ObserveRouterFallback("chain", "cap-exhausted")
	}
	return chain
}

// Name reports the underlying local engine name so /readyz and logs stay
// useful in single-node deployments.
func (r *Router) Name() string { return r.local.Name() }

// Endpoint reports the local engine endpoint.
func (r *Router) Endpoint() string { return r.local.Endpoint() }

// Health checks the local engine (workers' health is checked separately via
// the heartbeat loop).
func (r *Router) Health(ctx context.Context) error { return r.local.Health(ctx) }

// List returns the local engine's model list. (Cluster-wide listing happens
// via the placements store and the admin API.)
func (r *Router) List(ctx context.Context) ([]string, error) { return r.local.List(ctx) }

// Pull, Delete operate on the local engine. Workers are pulled-to via their
// own `flock model add` invocations.
func (r *Router) Pull(ctx context.Context, modelID string, onProgress func(string, int64, int64)) error {
	return r.local.Pull(ctx, modelID, onProgress)
}

func (r *Router) Delete(ctx context.Context, modelID string) error {
	return r.local.Delete(ctx, modelID)
}

// Unload forwards to the local engine. Cluster-wide unload (every remote
// holding a shard) isn't implemented yet — sharded models already tear
// down via the orchestrator's process-stop path on the workers.
func (r *Router) Unload(ctx context.Context, modelID string) error {
	return r.local.Unload(ctx, modelID)
}

// Embed dispatches an embedding request, with optional fallback. Tries the
// primary model first; on retriable error, walks the fallback chain in
// order. If every candidate fails, returns the PRIMARY's error since that's
// what the operator actually asked for.
//
// Per-request overrides (router.WithOverrides on the ctx) take precedence
// over the catalog chain: a non-empty Overrides.Fallbacks replaces the
// catalog chain entirely, and Overrides.NumRetries wraps each attempt in
// an exponential-backoff loop before advancing to the next candidate.
func (r *Router) Embed(ctx context.Context, req engines.EmbedRequest) (engines.EmbedResponse, error) {
	ctx, span := tracer.Start(ctx, "router.Embed",
		trace.WithAttributes(
			attribute.String("flock.model.requested", req.Model),
		),
	)
	defer span.End()

	ov := FromContext(ctx)
	chain, source, chains := r.chainFor(req.Model, ov)
	switch {
	case ov.Sort != "":
		// Explicit per-request sort wins over the latency-pressure
		// reorder — the client asked for a specific metric.
		chain = r.sortChain(chain, ov.Sort)
		span.SetAttributes(attribute.String("flock.sort", ov.Sort))
	case source == "catalog":
		if reordered, swapped := r.latency.reorderByLatency(chain); swapped {
			chain = reordered
			span.SetAttributes(
				attribute.Bool("flock.latency.reordered", true),
				attribute.String("flock.latency.front", chain[0]),
			)
		}
	}
	span.SetAttributes(
		attribute.Int("flock.fallback.chain_length", len(chain)),
		attribute.String("flock.fallback.source", source),
	)
	if ov.NumRetries > 0 {
		span.SetAttributes(
			attribute.Int("flock.retry.num_retries", ov.NumRetries),
			attribute.Int("flock.retry.backoff_ms", ov.RetryBackoffMS),
		)
	}

	var primaryErr error
	classified := false
	for i := 0; i < len(chain); i++ {
		candidate := chain[i]
		attempt := req
		attempt.Model = candidate
		attemptStart := time.Now()

		attemptCtx, attemptSpan := tracer.Start(ctx, "router.Embed.attempt",
			trace.WithAttributes(
				attribute.Int("flock.attempt", i),
				attribute.String("flock.model.candidate", candidate),
				attribute.Bool("flock.is_fallback", i > 0),
			),
		)

		eng, nodeID, err := r.pick(attemptCtx, candidate)
		if err != nil {
			attemptSpan.SetStatus(codes.Error, "pick failed")
			attemptSpan.RecordError(err)
			attemptSpan.End()
			if i == 0 {
				primaryErr = err
			}
			continue
		}
		attemptSpan.SetAttributes(
			attribute.String("flock.engine", eng.Name()),
			attribute.String("flock.node_id", nodeID),
		)
		ee, ok := eng.(engines.EmbedEngine)
		if !ok {
			err := fmt.Errorf("engine %s does not support embeddings", eng.Name())
			attemptSpan.SetStatus(codes.Error, "engine missing Embed")
			attemptSpan.RecordError(err)
			attemptSpan.End()
			if i == 0 {
				primaryErr = err
			}
			continue
		}
		// Retry loop wraps the single candidate. Each retry produces a
		// child span so traces show the wall-clock cost cleanly.
		var lastErr error
		for retry := 0; retry <= ov.NumRetries; retry++ {
			if retry > 0 {
				if err := waitBackoff(attemptCtx, retry, ov.RetryBackoffMS); err != nil {
					lastErr = err
					break
				}
				metrics.ObserveRouterFallback("embed", "retry")
			}
			r.incInflight(nodeID, candidate)
			res, err := ee.Embed(attemptCtx, attempt)
			r.decInflight(nodeID, candidate)
			r.recordOutcome(nodeID, err == nil)
			if err == nil {
				attemptSpan.SetStatus(codes.Ok, "")
				attemptSpan.End()
				if i > 0 {
					reason := "primary-error"
					if source == "request" {
						reason = "per-request"
					}
					r.logFallback(req.Model, candidate, "embed", primaryErr)
					metrics.ObserveRouterFallback("embed", reason)
					span.SetAttributes(attribute.Int("flock.fallback.used_at", i))
				}
				span.SetAttributes(attribute.String("flock.model.served", candidate))
				r.latency.record(candidate, time.Since(attemptStart), 0)
				return res, nil
			}
			lastErr = err
		}
		attemptSpan.SetStatus(codes.Error, "embed failed")
		attemptSpan.RecordError(lastErr)
		attemptSpan.End()
		if i == 0 {
			primaryErr = lastErr
		}
		// After the first candidate fails, classify the error and swap
		// the rest of the chain for a typed list when one is configured
		// AND it differs from the generic list. Only applies to
		// catalog-driven routing; per-request overrides bypass typed
		// selection on the theory that the operator explicitly chose
		// their chain.
		if !classified && source == "catalog" {
			classified = true
			chain = r.maybeSwapTypedChain(chain, chains, lastErr, "embed", i, span)
		}
	}
	span.SetStatus(codes.Error, "all candidates failed")
	if primaryErr != nil {
		span.RecordError(primaryErr)
	}
	return engines.EmbedResponse{}, primaryErr
}

// maybeSwapTypedChain inspects `err`, classifies it, and (if the
// classifier returned a typed bucket with a non-empty list that differs
// from generic) replaces the remainder of `chain` from index i+1
// onwards with the typed list.
//
// Returns the (possibly new) chain. Emits the
// `flock.fallback.classifier` span attribute and the
// `flock_router_fallback_total{reason="content-policy|context-length"}`
// metric so operators can see which classifier branch fired.
func (r *Router) maybeSwapTypedChain(chain []string, chains FallbackChains, err error, op string, primaryIdx int, span trace.Span) []string {
	class := ClassifyError(err)
	span.SetAttributes(attribute.String("flock.fallback.classifier", class.String()))
	if class == ClassGeneric {
		return chain
	}
	typed := chains.PickFor(class)
	if len(typed) == 0 || sameOrder(typed, chains.Generic) {
		return chain
	}
	// Replace the remainder of the chain with the typed list. We do not
	// rebuild via buildChain here because the primary has already been
	// tried — typed lists are pure replacements for the *fallbacks*.
	newChain := make([]string, 0, primaryIdx+1+len(typed))
	newChain = append(newChain, chain[:primaryIdx+1]...)
	newChain = append(newChain, typed...)
	newChain = r.applyCap(newChain)
	metrics.ObserveRouterFallback(op, class.String())
	if r.log != nil {
		r.log.Info("router typed fallback",
			"op", op,
			"classifier", class.String(),
			"new_chain_len", len(newChain),
		)
	}
	return newChain
}

// inCooldown reports whether the named node is currently in the
// penalty box. Cheap, takes RLock — pick() checks this on every worker.
func (r *Router) inCooldown(nodeID string) bool {
	if r.placementCooldownDur <= 0 {
		return false
	}
	r.mu.RLock()
	until, ok := r.cooldowns[nodeID]
	r.mu.RUnlock()
	if !ok {
		return false
	}
	if time.Now().After(until) {
		// Cooldown expired — clean up so the gauge / debug view stays
		// honest. We don't reset failures here; the next successful
		// call does, so a still-flaky node re-enters cooldown on the
		// very next failure.
		r.mu.Lock()
		if until2, ok := r.cooldowns[nodeID]; ok && time.Now().After(until2) {
			delete(r.cooldowns, nodeID)
			metrics.SetRouterCooldownsActive(len(r.cooldowns))
			if r.log != nil {
				r.log.Info("router cooldown expired", "node", nodeID)
			}
		}
		r.mu.Unlock()
		return false
	}
	return true
}

// stickyPick returns the pinned node id for (user_id, model) when the
// pin is fresh, or "" when there's nothing to suggest. Bypassed for
// requests with no user_id and for the synthetic `auto` model id.
func (r *Router) stickyPick(ctx context.Context, model string) string {
	if r.stickyTTL <= 0 || model == "" || model == "auto" {
		return ""
	}
	userID := userIDFor(ctx)
	if userID == "" {
		return ""
	}
	key := userID + "|" + model
	r.mu.RLock()
	entry, ok := r.stickiness[key]
	r.mu.RUnlock()
	if !ok {
		metrics.ObserveStickyOutcome("miss")
		return ""
	}
	if time.Now().After(entry.ExpiresAt) {
		r.mu.Lock()
		// Re-check under write lock — another goroutine may have
		// already refreshed in the window.
		if e2, ok := r.stickiness[key]; ok && time.Now().After(e2.ExpiresAt) {
			delete(r.stickiness, key)
		}
		r.mu.Unlock()
		metrics.ObserveStickyOutcome("expired")
		return ""
	}
	return entry.NodeID
}

// rememberSticky refreshes the (user_id, model) → node pin on each
// successful pick. The next request for the same tuple within stickyTTL
// will land on the same node (assuming it's still healthy).
func (r *Router) rememberSticky(ctx context.Context, model, nodeID string) {
	if r.stickyTTL <= 0 || model == "" || model == "auto" || nodeID == "" {
		return
	}
	userID := userIDFor(ctx)
	if userID == "" {
		return
	}
	key := userID + "|" + model
	r.mu.Lock()
	r.stickiness[key] = stickyEntry{NodeID: nodeID, ExpiresAt: time.Now().Add(r.stickyTTL)}
	r.mu.Unlock()
}

// userIDFor returns the authenticated user id from ctx, or "" when no
// auth key is attached (dev mode with require_keys=false). Stickiness
// is disabled for the anonymous case.
func userIDFor(ctx context.Context) string {
	if k := auth.KeyFrom(ctx); k != nil {
		return k.UserID
	}
	return ""
}

// preferNode reorders `workers` so that `nodeID` is first if it's
// present. Other entries keep their existing relative order — this
// only nudges the sticky node to the front of the line, not the rest.
func preferNode(workers []store.Placement, nodeID string) []store.Placement {
	if len(workers) == 0 {
		return workers
	}
	for i, w := range workers {
		if w.NodeID == nodeID {
			if i == 0 {
				return workers
			}
			out := make([]store.Placement, 0, len(workers))
			out = append(out, workers[i])
			out = append(out, workers[:i]...)
			out = append(out, workers[i+1:]...)
			return out
		}
	}
	return workers
}

// CooldownUntil returns the time at which `nodeID` exits the penalty
// box, or a zero time if the node isn't currently in cooldown. Public
// so the admin API can decorate the Nodes list with a "🚫 cooldown"
// badge.
func (r *Router) CooldownUntil(nodeID string) time.Time {
	if r.placementCooldownDur <= 0 {
		return time.Time{}
	}
	r.mu.RLock()
	until, ok := r.cooldowns[nodeID]
	r.mu.RUnlock()
	if !ok || time.Now().After(until) {
		return time.Time{}
	}
	return until
}

// recordOutcome notes the success/failure of an engine call from a
// remote worker. On `allowedFails` consecutive failures the node enters
// cooldown for `cooldownDur`. The first success after expiry resets
// the counter, so a node that flakes once doesn't shadow itself
// forever.
//
// nodeID == localNode is a no-op — cooldown only applies to remote
// workers. (A flaky local engine is a different operational problem;
// restart it.)
func (r *Router) recordOutcome(nodeID string, ok bool) {
	if r.placementCooldownDur <= 0 || r.placementAllowedFails <= 0 {
		return
	}
	if nodeID == "" || nodeID == r.localNode || strings.HasPrefix(nodeID, "shard:") {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if ok {
		// Success: reset failure count if the node had any pending
		// strikes. Don't churn the metric — we only update the gauge
		// when entering/exiting cooldown.
		if r.failures[nodeID] > 0 {
			delete(r.failures, nodeID)
		}
		return
	}
	r.failures[nodeID]++
	if r.failures[nodeID] < r.placementAllowedFails {
		return
	}
	// Enter cooldown.
	r.cooldowns[nodeID] = time.Now().Add(r.placementCooldownDur)
	r.failures[nodeID] = 0
	metrics.SetRouterCooldownsActive(len(r.cooldowns))
	if r.log != nil {
		r.log.Warn("router placement cooldown",
			"node", nodeID,
			"allowed_fails", r.placementAllowedFails,
			"cooldown", r.placementCooldownDur,
		)
	}
}

// sameOrder returns true when two slices have identical contents in
// order. Cheap escape hatch so an entry whose typed list duplicates the
// generic list short-circuits to the existing behavior.
func sameOrder(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// chainFor builds the candidate chain for `model`, accounting for any
// per-request overrides. The returned `source` is one of:
//
//   - "request" — Overrides.Fallbacks was non-empty; catalog fallbacks
//     are ignored for this request. Surfaced as a span attribute so
//     operators can tell at-a-trace who's bypassing catalog policy.
//   - "catalog" — fell through to the catalog-driven resolver (or just
//     the primary if no resolver / no `fallback:` entry).
//
// The returned `chains` carries the typed fallback lists; callers use
// this to swap the remainder of the chain after classifying the
// primary's failure. For source=="request" the returned chains is the
// zero value — typed selection only applies to catalog-driven routing.
func (r *Router) chainFor(model string, ov Overrides) ([]string, string, FallbackChains) {
	if len(ov.Fallbacks) > 0 {
		chain := make([]string, 0, len(ov.Fallbacks)+1)
		chain = append(chain, model)
		chain = append(chain, ov.Fallbacks...)
		return chain, "request", FallbackChains{}
	}
	chains := r.chainsFor(model)
	return r.applyCap(buildChain(model, chains.Generic)), "catalog", chains
}

// waitBackoff sleeps for the backoff interval before retry attempt `n`.
// Initial backoff doubles each retry, capped at RetryBackoffCapMS.
// Respects context cancellation — returns ctx.Err() if the caller went
// away while we were waiting.
func waitBackoff(ctx context.Context, retry, initialMS int) error {
	if initialMS <= 0 {
		return nil
	}
	delay := initialMS
	for i := 1; i < retry; i++ {
		delay *= 2
		if delay > RetryBackoffCapMS {
			delay = RetryBackoffCapMS
			break
		}
	}
	t := time.NewTimer(time.Duration(delay) * time.Millisecond)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Chat dispatches a chat request, with optional fallback. Tries the primary
// model first; on synchronous error from Engine.Chat (the engine couldn't
// even start the stream), walks the fallback chain in order. Once the
// stream starts producing events, fallback is no longer possible —
// downstream errors propagate as-is.
//
// Tracing note: router.Chat starts a span at request entry. Each fallback
// attempt is a child span. The span covering the eventual successful
// candidate stays open across the streaming relay and is closed by the
// goroutine that drains the inner stream — so its duration matches actual
// time-to-completion, not just the time to start the stream.
func (r *Router) Chat(ctx context.Context, req engines.ChatRequest) (<-chan engines.StreamEvent, error) {
	// Hedging short-circuit: when the request opts in and the router
	// is configured for replicas > 1, fire concurrent calls and
	// return whichever stream opens first. Retry / fallback are
	// skipped — the operator chose the N× cost trade.
	if ov := FromContext(ctx); ov.Hedge && r.hedgeReplicas > 1 {
		if stream, err := r.chatHedged(ctx, req, r.hedgeReplicas); stream != nil || err != nil {
			return stream, err
		}
		// fall through to the normal path when hedging found no
		// eligible workers.
	}
	ctx, span := tracer.Start(ctx, "router.Chat",
		trace.WithAttributes(
			attribute.String("flock.model.requested", req.Model),
			attribute.Bool("flock.stream", req.Stream),
		),
	)
	// Note: span.End() is called in the streaming goroutine for the winning
	// candidate (so its duration covers the full streamed response), or
	// inline below if every candidate fails synchronously.

	ov := FromContext(ctx)
	chain, source, chains := r.chainFor(req.Model, ov)
	switch {
	case ov.Sort != "":
		// Explicit per-request sort (`flock.sort` / `:floor` / `:nitro`)
		// wins over the latency-pressure reorder. Applies to per-request
		// chains too — sorting an explicit fallback list is still the
		// client's stated intent.
		chain = r.sortChain(chain, ov.Sort)
		span.SetAttributes(attribute.String("flock.sort", ov.Sort))
	case source == "catalog":
		// Latency-aware reorder applies only to the catalog chain —
		// per-request overrides are operator intent and shouldn't be
		// silently rearranged.
		if reordered, swapped := r.latency.reorderByLatency(chain); swapped {
			chain = reordered
			span.SetAttributes(
				attribute.Bool("flock.latency.reordered", true),
				attribute.String("flock.latency.front", chain[0]),
			)
		}
	}
	span.SetAttributes(
		attribute.Int("flock.fallback.chain_length", len(chain)),
		attribute.String("flock.fallback.source", source),
	)
	if ov.NumRetries > 0 {
		span.SetAttributes(
			attribute.Int("flock.retry.num_retries", ov.NumRetries),
			attribute.Int("flock.retry.backoff_ms", ov.RetryBackoffMS),
		)
	}

	var primaryErr error
	classified := false
	for i := 0; i < len(chain); i++ {
		candidate := chain[i]
		attempt := req
		attempt.Model = candidate
		attemptStart := time.Now()

		attemptCtx, attemptSpan := tracer.Start(ctx, "router.Chat.attempt",
			trace.WithAttributes(
				attribute.Int("flock.attempt", i),
				attribute.String("flock.model.candidate", candidate),
				attribute.Bool("flock.is_fallback", i > 0),
			),
		)

		eng, nodeID, err := r.pick(attemptCtx, candidate)
		if err != nil {
			attemptSpan.SetStatus(codes.Error, "pick failed")
			attemptSpan.RecordError(err)
			attemptSpan.End()
			if i == 0 {
				primaryErr = err
			}
			continue
		}
		attemptSpan.SetAttributes(
			attribute.String("flock.engine", eng.Name()),
			attribute.String("flock.node_id", nodeID),
		)

		// Retry loop wraps the engine.Chat call. Retries only apply to
		// synchronous start failures (engine couldn't begin the stream).
		// Once a stream is open, mid-stream errors are NOT retried — the
		// client has already begun seeing tokens.
		//
		// streamCancel is the cancel for the cancellable child context
		// of the *successful* stream — handed to the goroutine below.
		// Failed attempts cancel their own child ctx locally so vet (and
		// future readers) can see no context.CancelFunc leaks across the
		// loop boundary.
		var inner <-chan engines.StreamEvent
		var streamCancel context.CancelFunc
		var lastErr error
		for retry := 0; retry <= ov.NumRetries; retry++ {
			if retry > 0 {
				if err := waitBackoff(attemptCtx, retry, ov.RetryBackoffMS); err != nil {
					lastErr = err
					break
				}
				metrics.ObserveRouterFallback("chat", "retry")
			}
			r.incInflight(nodeID, candidate)
			thisCtx, thisCancel := context.WithCancel(attemptCtx)
			s, err := eng.Chat(thisCtx, attempt)
			if err == nil {
				inner = s
				streamCancel = thisCancel
				r.recordOutcome(nodeID, true)
				break // stream opened — stop retrying
			}
			thisCancel()
			r.decInflight(nodeID, candidate)
			r.recordOutcome(nodeID, false)
			lastErr = err
		}
		if inner == nil {
			attemptSpan.SetStatus(codes.Error, "engine.Chat returned synchronously")
			attemptSpan.RecordError(lastErr)
			attemptSpan.End()
			if i == 0 {
				primaryErr = lastErr
			}
			if !classified && source == "catalog" {
				classified = true
				chain = r.maybeSwapTypedChain(chain, chains, lastErr, "chat", i, span)
			}
			continue
		}
		attemptSpan.SetStatus(codes.Ok, "stream started")
		attemptSpan.End()

		if i > 0 {
			reason := "primary-error"
			if source == "request" {
				reason = "per-request"
			}
			r.logFallback(req.Model, candidate, "chat", primaryErr)
			metrics.ObserveRouterFallback("chat", reason)
			span.SetAttributes(attribute.Int("flock.fallback.used_at", i))
		}
		span.SetAttributes(attribute.String("flock.model.served", candidate))

		out := make(chan engines.StreamEvent, 16)
		// Capture once for the closure — `candidate` is the loop var.
		servedModel := candidate
		go func() {
			defer streamCancel() // always release engine's ctx
			defer r.decInflight(nodeID, servedModel)
			defer close(out)
			defer span.End() // duration covers full streamed response
			var tokenCount, completionTokens int
			for ev := range inner {
				if ev.Delta != "" {
					tokenCount++
				}
				if ev.Usage != nil {
					completionTokens = ev.Usage.CompletionTokens
				}
				select {
				case out <- ev:
				case <-ctx.Done():
					span.SetStatus(codes.Error, "client disconnected")
					streamCancel()
					go drainWithTimeout(inner, 30*time.Second)
					return
				}
			}
			span.SetAttributes(attribute.Int("flock.stream.events", tokenCount))
			span.SetStatus(codes.Ok, "")
			// Latency + throughput sample for this model = full attempt-to-
			// done duration. Feeds reorderByLatency and `sort: throughput`.
			// Engine-reported completion tokens preferred; delta-event count
			// is the fallback proxy when the engine omits usage.
			if completionTokens == 0 {
				completionTokens = tokenCount
			}
			r.latency.record(servedModel, time.Since(attemptStart), completionTokens)
		}()
		return out, nil
	}
	span.SetStatus(codes.Error, "all candidates failed")
	if primaryErr != nil {
		span.RecordError(primaryErr)
	}
	span.End()
	return nil, primaryErr
}

// chatHedged fires the request to up to `replicas` least-loaded
// workers concurrently. The first goroutine whose eng.Chat returns
// without error wins; the losers' contexts are cancelled and their
// inflight counters are decremented.
//
// Returns (nil, nil) when hedging found no eligible workers — the
// caller should fall back to the normal pick path. Returns
// (nil, err) when every replica failed synchronously; the caller
// surfaces the err.
//
// Limitations:
//   - Hedging skips the catalog fallback chain, retries, latency
//     reorder, and typed fallback. The operator already accepted the
//     N× cost.
//   - Local-only deployments fall back to a single local call.
func (r *Router) chatHedged(ctx context.Context, req engines.ChatRequest, replicas int) (<-chan engines.StreamEvent, error) {
	ctx, span := tracer.Start(ctx, "router.Chat.hedged",
		trace.WithAttributes(
			attribute.String("flock.model.requested", req.Model),
			attribute.Int("flock.hedge.replicas", replicas),
		),
	)
	candidates := r.hedgePickWorkers(ctx, req.Model, replicas)
	if len(candidates) < 2 {
		span.SetAttributes(attribute.Int("flock.hedge.candidates", len(candidates)))
		span.End()
		return nil, nil // fall through
	}
	span.SetAttributes(attribute.Int("flock.hedge.candidates", len(candidates)))

	type result struct {
		stream <-chan engines.StreamEvent
		cancel context.CancelFunc
		nodeID string
		err    error
	}
	resultCh := make(chan result, len(candidates))
	for _, w := range candidates {
		w := w
		go func() {
			thisCtx, thisCancel := context.WithCancel(ctx)
			r.incInflight(w.nodeID, req.Model)
			s, err := w.engine.Chat(thisCtx, req)
			if err != nil {
				thisCancel()
				r.decInflight(w.nodeID, req.Model)
			}
			resultCh <- result{stream: s, cancel: thisCancel, nodeID: w.nodeID, err: err}
		}()
	}

	var winner result
	var firstErr error
	for i := 0; i < len(candidates); i++ {
		res := <-resultCh
		if res.err != nil {
			if firstErr == nil {
				firstErr = res.err
			}
			metrics.ObserveRouterHedge("error")
			continue
		}
		if winner.stream == nil {
			winner = res
			metrics.ObserveRouterHedge("win")
			continue
		}
		// Late arrival — cancel + drain.
		res.cancel()
		r.decInflight(res.nodeID, req.Model)
		metrics.ObserveRouterHedge("cancelled")
		go drainWithTimeout(res.stream, 30*time.Second)
	}
	if winner.stream == nil {
		span.SetStatus(codes.Error, "all hedge replicas failed")
		span.End()
		return nil, firstErr
	}
	span.SetAttributes(attribute.String("flock.hedge.winner", winner.nodeID))
	span.SetStatus(codes.Ok, "hedge winner")
	// Defer winner's cleanup to a goroutine that watches the stream
	// close. (The winning attempt's incInflight stays in effect until
	// the stream drains.)
	out := make(chan engines.StreamEvent, 16)
	go func() {
		defer winner.cancel()
		defer r.decInflight(winner.nodeID, req.Model)
		defer close(out)
		defer span.End()
		for ev := range winner.stream {
			select {
			case out <- ev:
			case <-ctx.Done():
				go drainWithTimeout(winner.stream, 30*time.Second)
				return
			}
		}
	}()
	return out, nil
}

// hedgeCandidate is a small carrier struct so the hedged path doesn't
// have to round-trip nodeIDs through the picker.
type hedgeCandidate struct {
	engine engines.Engine
	nodeID string
}

// hedgePickWorkers returns up to `n` least-loaded workers that host
// the model, skipping cooldowns + stale heartbeats. Returns the
// chosen candidates in arbitrary order.
func (r *Router) hedgePickWorkers(ctx context.Context, model string, n int) []hedgeCandidate {
	if model == "" || r.store == nil {
		return nil
	}
	placements, err := r.store.Placements().GetByModel(ctx, model)
	if err != nil || len(placements) == 0 {
		return nil
	}
	r.mu.RLock()
	sort.Slice(placements, func(i, j int) bool {
		return r.inflight[placements[i].NodeID] < r.inflight[placements[j].NodeID]
	})
	r.mu.RUnlock()

	out := make([]hedgeCandidate, 0, n)
	for _, p := range placements {
		if len(out) >= n {
			break
		}
		if p.NodeID == r.localNode {
			// Add local as one of the candidates.
			out = append(out, hedgeCandidate{engine: r.local, nodeID: p.NodeID})
			continue
		}
		node, err := r.store.Nodes().Get(ctx, p.NodeID)
		if err != nil || node == nil || node.Address == "" {
			continue
		}
		if r.heartbeatMaxAge > 0 && !node.LastHeartbeat.IsZero() &&
			time.Since(node.LastHeartbeat) > r.heartbeatMaxAge {
			continue
		}
		if r.inCooldown(node.ID) {
			continue
		}
		out = append(out, hedgeCandidate{
			engine: r.getOrCreateRemote(node.ID, node.Address, node.WorkerToken),
			nodeID: node.ID,
		})
	}
	return out
}

// drainWithTimeout consumes a stream channel for at most `d`, then
// returns. Used when the client disconnects mid-stream: we cancel the
// engine context to stop the producer, then drain whatever's already
// buffered. Without the timeout, a hung backend would leak a goroutine
// blocked on a receive that never completes.
func drainWithTimeout[T any](ch <-chan T, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-t.C:
			return
		}
	}
}

// logFallback emits a structured slog event so operators can filter
// fallback activations by model, fallback target, op, or error class.
// The metric (`flock_router_fallback_total{op, reason}`) is now bumped
// by the call site so the reason label can distinguish per-request
// overrides from catalog-driven fallbacks.
func (r *Router) logFallback(primary, used, op string, primaryErr error) {
	if r.log != nil {
		r.log.Warn("router fallback",
			"op", op,
			"primary", primary,
			"used", used,
			"err", primaryErr,
		)
	}
}

// pick returns an engine and the node id it represents.
func (r *Router) pick(ctx context.Context, model string) (engines.Engine, string, error) {
	if model == "" {
		metrics.ObserveRouterPick("local", "ok")
		return r.local, r.localNode, nil
	}

	// 0. Is this a SHARDED model? If yes, route to its coordinator (always
	//    local today) via a llamacpp engine. The coordinator handles the
	//    fan-out to rpc-server backends on workers internally.
	if eng, ok := r.shardCoordinator(ctx, model); ok {
		metrics.ObserveRouterPick("shard", "ok")
		return eng, "shard:" + model, nil
	}

	// 1. Is the model on the local node?
	localHas, _ := r.modelOnNode(ctx, r.localNode, model)
	if localHas {
		metrics.ObserveRouterPick("local", "ok")
		return r.local, r.localNode, nil
	}

	// 2. Find workers hosting it
	placements, err := r.store.Placements().GetByModel(ctx, model)
	if err != nil {
		// Fall back to local — surface its error rather than hiding ours
		metrics.ObserveRouterPick("fallback-to-local", "store-error")
		return r.local, r.localNode, nil
	}
	// Filter out local (if present) — we already checked above
	workers := placements[:0]
	for _, p := range placements {
		if p.NodeID != r.localNode {
			workers = append(workers, p)
		}
	}

	if len(workers) == 0 {
		// Nothing has it — let local try, it will return a clear error.
		metrics.ObserveRouterPick("fallback-to-local", "no-workers")
		return r.local, r.localNode, nil
	}

	// 3. Pick least-loaded worker. The snapshot we sort against is
	//    consistent under RLock, but the actual inflight increment
	//    happens in the caller AFTER we return — so two concurrent
	//    requests can both pick the same "least-loaded" node and
	//    over-route once before the counter catches up. This is a
	//    load-balancing imperfection, not a correctness bug, and
	//    self-corrects on the next request.
	r.mu.RLock()
	sort.Slice(workers, func(i, j int) bool {
		return r.inflight[workers[i].NodeID] < r.inflight[workers[j].NodeID]
	})
	r.mu.RUnlock()

	// 3a. Sticky pin: if there's a fresh (user_id, model) entry whose
	// node is still in the workers list AND not in cooldown, surface
	// it to the front of the sorted slice so it's tried before the
	// least-loaded candidate. KV-cache locality outweighs a small
	// inflight delta on the alternative.
	stickyNode := r.stickyPick(ctx, model)
	if stickyNode != "" {
		workers = preferNode(workers, stickyNode)
	}

	// Walk the sorted list: skip any worker whose heartbeat is stale
	// before falling back to local. Without this, a request to a model
	// that's still in the placements table for a dead node would wait
	// for the engine call to time out.
	for _, pick := range workers {
		node, err := r.store.Nodes().Get(ctx, pick.NodeID)
		if err != nil || node == nil || node.Address == "" {
			metrics.ObserveRouterPick("worker", "error")
			continue
		}
		if r.heartbeatMaxAge > 0 && !node.LastHeartbeat.IsZero() &&
			time.Since(node.LastHeartbeat) > r.heartbeatMaxAge {
			metrics.ObserveRouterPick("worker", "stale-heartbeat")
			if r.log != nil {
				r.log.Warn("router skipping stale worker",
					"node", pick.NodeID,
					"model", model,
					"last_heartbeat", node.LastHeartbeat,
					"max_age", r.heartbeatMaxAge,
				)
			}
			continue
		}
		if r.inCooldown(node.ID) {
			metrics.ObserveRouterPick("worker", "cooldown")
			continue
		}
		eng := r.getOrCreateRemote(node.ID, node.Address, node.WorkerToken)
		stickyOutcome := "miss"
		if stickyNode != "" && node.ID == stickyNode {
			stickyOutcome = "hit"
		}
		r.rememberSticky(ctx, model, node.ID)
		metrics.ObserveRouterPick("worker", "ok")
		metrics.ObserveStickyOutcome(stickyOutcome)
		return eng, node.ID, nil
	}
	// All workers exhausted (all dead or stale). Fall back to local — it
	// will surface its own "model not loaded" error.
	metrics.ObserveRouterPick("fallback-to-local", "all-workers-stale")
	return r.local, r.localNode, nil
}

// shardCoordinator returns the llamacpp engine pointing at the coordinator
// of a sharded model, or (nil, false) if the model isn't sharded.
func (r *Router) shardCoordinator(ctx context.Context, modelID string) (engines.Engine, bool) {
	cacheKey := "shard:" + modelID
	r.mu.RLock()
	if eng, ok := r.remotes[cacheKey]; ok {
		r.mu.RUnlock()
		return eng, true
	}
	r.mu.RUnlock()
	shards, err := r.store.Shards().GetByModel(ctx, modelID)
	if err != nil || len(shards) == 0 {
		return nil, false
	}
	for _, s := range shards {
		if s.Role == "coordinator" && s.Status == "ready" {
			eng := engines.NewLlamaCppRPC("http://" + s.Address)
			r.mu.Lock()
			r.remotes[cacheKey] = eng
			r.mu.Unlock()
			return eng, true
		}
	}
	return nil, false
}

// InvalidateModel drops any cached engine for the given model. Called by
// the orchestrator when shards are torn down so the next request rebuilds.
func (r *Router) InvalidateModel(modelID string) {
	r.mu.Lock()
	delete(r.remotes, "shard:"+modelID)
	r.mu.Unlock()
}

func (r *Router) modelOnNode(ctx context.Context, nodeID, modelID string) (bool, error) {
	ps, err := r.store.Placements().GetByNode(ctx, nodeID)
	if err != nil {
		return false, err
	}
	for _, p := range ps {
		if p.ModelID == modelID && p.Status == "ready" {
			return true, nil
		}
	}
	return false, nil
}

// getOrCreateRemote returns a cached remote engine (vLLM driver pointing at the
// worker's address) or builds + caches one.
//
// Concurrency: holds the write lock for the entire check-and-create so
// two concurrent calls for the same nodeID can't each construct a fresh
// engine and have one silently overwrite the other. The window is short
// (engines.NewVLLM is a small struct alloc, no I/O) so write-lock for
// the duration is fine.
func (r *Router) getOrCreateRemote(nodeID, address, token string) engines.Engine {
	r.mu.Lock()
	defer r.mu.Unlock()
	if eng, ok := r.remotes[nodeID]; ok {
		return eng
	}
	endpoint := address
	if !startsWithScheme(endpoint) {
		endpoint = "http://" + endpoint
	}
	eng := engines.NewVLLM(endpoint, token)
	r.remotes[nodeID] = eng
	return eng
}

func (r *Router) incInflight(nodeID, model string) {
	r.mu.Lock()
	r.inflight[nodeID]++
	r.inflightDim[nodeID+"|"+model]++
	n := r.inflight[nodeID]
	r.mu.Unlock()
	metrics.SetRouterInflight(nodeID, n)
}

func (r *Router) decInflight(nodeID, model string) {
	r.mu.Lock()
	if r.inflight[nodeID] > 0 {
		r.inflight[nodeID]--
	}
	key := nodeID + "|" + model
	if r.inflightDim[key] > 0 {
		r.inflightDim[key]--
	}
	if r.inflightDim[key] == 0 {
		delete(r.inflightDim, key) // keep the map from growing unboundedly
	}
	n := r.inflight[nodeID]
	r.mu.Unlock()
	metrics.SetRouterInflight(nodeID, n)
}

// Inflight returns a snapshot of current per-node in-flight counts (used by
// the admin /admin/v1/router endpoint).
func (r *Router) Inflight() map[string]int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]int, len(r.inflight))
	for k, v := range r.inflight {
		out[k] = v
	}
	return out
}

// InflightByModel returns a snapshot of per-(node, model) in-flight counts,
// keyed `node_id + "|" + model` (the model string is whatever the request
// carried — catalog id normally, engine-native for direct passthrough).
// Used by the lifecycle manager's eviction drain so it waits only on the
// victim's traffic, not the whole node's.
func (r *Router) InflightByModel() map[string]int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]int, len(r.inflightDim))
	for k, v := range r.inflightDim {
		out[k] = v
	}
	return out
}

// RegisterLocalModel records that the local engine has loaded a model. Called
// from cmd_model after a successful local pull, and at startup for any models
// the local engine already has.
func (r *Router) RegisterLocalModel(ctx context.Context, modelID string) error {
	return r.store.Placements().Upsert(ctx, store.Placement{
		NodeID:   r.localNode,
		ModelID:  modelID,
		Status:   "ready",
		LastSeen: time.Now(),
	})
}

func startsWithScheme(s string) bool {
	for i := 0; i+3 < len(s); i++ {
		if s[i] == ':' && s[i+1] == '/' && s[i+2] == '/' {
			return true
		}
	}
	return false
}

// ensure interface satisfaction at compile time
var _ engines.Engine = (*Router)(nil)
