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
	"io"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/hadihonarvar/flock/internal/engines"
	"github.com/hadihonarvar/flock/internal/store"
)

// stderrTarget is a package-level writer so tests can capture fallback log
// lines without touching the real os.Stderr.
var stderrTarget io.Writer = os.Stderr

// FallbackResolver returns the ordered list of fallback model IDs for a
// primary model. Empty slice (or nil) means no fallback — Router behaves
// exactly as it did before this hook existed. Typically backed by the
// catalog's `fallback:` field.
type FallbackResolver func(modelID string) []string

// Router implements engines.Engine by dispatching to either the local engine
// or a remote worker engine based on cluster placements.
type Router struct {
	local     engines.Engine
	store     store.Store
	localNode string // node id used for "local" placements (typically "local")

	// FallbackResolver is optional. When set, Chat / Embed will retry the
	// request against each fallback model in order on retriable errors
	// (anything Engine.Chat returns synchronously). Set via
	// router.SetFallbackResolver after construction.
	fallback FallbackResolver

	mu       sync.RWMutex
	inflight map[string]int            // node_id → live request count
	remotes  map[string]engines.Engine // node_id → cached remote engine
}

// New constructs a Router that wraps the local engine and consults the store
// for placements + node info.
func New(local engines.Engine, st store.Store) *Router {
	return &Router{
		local:     local,
		store:     st,
		localNode: "local",
		inflight:  make(map[string]int),
		remotes:   make(map[string]engines.Engine),
	}
}

// SetFallbackResolver wires a fallback chain provider into the router.
// Callers typically pass a closure over the catalog; nil disables fallback.
func (r *Router) SetFallbackResolver(f FallbackResolver) {
	r.fallback = f
}

// resolveChain returns [primary, ...fallback]. When no resolver is set or
// the model has no fallback entry, returns just [primary].
func (r *Router) resolveChain(model string) []string {
	if r.fallback == nil {
		return []string{model}
	}
	fb := r.fallback(model)
	if len(fb) == 0 {
		return []string{model}
	}
	chain := make([]string, 0, len(fb)+1)
	chain = append(chain, model)
	chain = append(chain, fb...)
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

// Embed dispatches an embedding request, with optional fallback. Tries the
// primary model first; on retriable error, walks the fallback chain in
// order. If every candidate fails, returns the PRIMARY's error since that's
// what the operator actually asked for.
func (r *Router) Embed(ctx context.Context, req engines.EmbedRequest) (engines.EmbedResponse, error) {
	chain := r.resolveChain(req.Model)
	var primaryErr error
	for i, candidate := range chain {
		attempt := req
		attempt.Model = candidate

		eng, nodeID, err := r.pick(ctx, candidate)
		if err != nil {
			if i == 0 {
				primaryErr = err
			}
			continue
		}
		ee, ok := eng.(engines.EmbedEngine)
		if !ok {
			if i == 0 {
				primaryErr = fmt.Errorf("engine %s does not support embeddings", eng.Name())
			}
			continue
		}
		r.incInflight(nodeID)
		res, err := ee.Embed(ctx, attempt)
		r.decInflight(nodeID)
		if err == nil {
			if i > 0 {
				logFallback(req.Model, candidate, "embed", primaryErr)
			}
			return res, nil
		}
		if i == 0 {
			primaryErr = err
		}
	}
	return engines.EmbedResponse{}, primaryErr
}

// Chat dispatches a chat request, with optional fallback. Tries the primary
// model first; on synchronous error from Engine.Chat (the engine couldn't
// even start the stream), walks the fallback chain in order. Once the
// stream starts producing events, fallback is no longer possible —
// downstream errors propagate as-is.
func (r *Router) Chat(ctx context.Context, req engines.ChatRequest) (<-chan engines.StreamEvent, error) {
	chain := r.resolveChain(req.Model)
	var primaryErr error
	for i, candidate := range chain {
		attempt := req
		attempt.Model = candidate

		eng, nodeID, err := r.pick(ctx, candidate)
		if err != nil {
			if i == 0 {
				primaryErr = err
			}
			continue
		}
		r.incInflight(nodeID)
		inner, err := eng.Chat(ctx, attempt)
		if err != nil {
			r.decInflight(nodeID)
			if i == 0 {
				primaryErr = err
			}
			continue
		}
		if i > 0 {
			logFallback(req.Model, candidate, "chat", primaryErr)
		}

		out := make(chan engines.StreamEvent, 16)
		go func() {
			defer r.decInflight(nodeID)
			defer close(out)
			for ev := range inner {
				select {
				case out <- ev:
				case <-ctx.Done():
					go func() {
						for range inner {
						}
					}()
					return
				}
			}
		}()
		return out, nil
	}
	return nil, primaryErr
}

// logFallback emits a stderr line so operators see the fallback in flock up
// logs without rolling their own log aggregation. Kept terse so it doesn't
// clutter steady-state output — fallback should be the exception.
func logFallback(primary, used, op string, primaryErr error) {
	// We deliberately use stderr rather than the typed slog logger here:
	// the Router doesn't take a logger dep, and adding one for this single
	// line isn't worth the constructor churn. Operators tail flock up
	// stderr; that's where this surface belongs.
	fmt.Fprintf(stderrTarget, "[router] %s fallback: %s → %s (primary error: %v)\n",
		op, primary, used, primaryErr)
}

// pick returns an engine and the node id it represents.
func (r *Router) pick(ctx context.Context, model string) (engines.Engine, string, error) {
	if model == "" {
		return r.local, r.localNode, nil
	}

	// 0. Is this a SHARDED model? If yes, route to its coordinator (always
	//    local today) via a llamacpp engine. The coordinator handles the
	//    fan-out to rpc-server backends on workers internally.
	if eng, ok := r.shardCoordinator(ctx, model); ok {
		return eng, "shard:" + model, nil
	}

	// 1. Is the model on the local node?
	localHas, _ := r.modelOnNode(ctx, r.localNode, model)
	if localHas {
		return r.local, r.localNode, nil
	}

	// 2. Find workers hosting it
	placements, err := r.store.Placements().GetByModel(ctx, model)
	if err != nil {
		// Fall back to local — surface its error rather than hiding ours
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
		return r.local, r.localNode, nil
	}

	// 3. Pick least-loaded worker
	r.mu.RLock()
	sort.Slice(workers, func(i, j int) bool {
		return r.inflight[workers[i].NodeID] < r.inflight[workers[j].NodeID]
	})
	r.mu.RUnlock()

	pick := workers[0]
	node, err := r.store.Nodes().Get(ctx, pick.NodeID)
	if err != nil || node == nil || node.Address == "" {
		return nil, "", fmt.Errorf("router: node %s unreachable", pick.NodeID)
	}

	eng := r.getOrCreateRemote(node.ID, node.Address, node.WorkerToken)
	return eng, node.ID, nil
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

func (r *Router) incInflight(nodeID string) {
	r.mu.Lock()
	r.inflight[nodeID]++
	r.mu.Unlock()
}

func (r *Router) decInflight(nodeID string) {
	r.mu.Lock()
	if r.inflight[nodeID] > 0 {
		r.inflight[nodeID]--
	}
	r.mu.Unlock()
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
