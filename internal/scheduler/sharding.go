// Package scheduler is the leader-side orchestration logic for sharded
// models. CreateSharded picks workers, asks each one to start an rpc-server
// for its piece, then launches the coordinator llama-server locally and
// stitches everything together via a placement row that the router resolves.
//
// Current scope:
//   - manual shard-count override (`--shards=N`) or catalog default
//   - simple bin-pack on free RAM (highest free first)
//   - coordinator always runs on the leader
//   - no automatic restart on shard crash (admin re-runs the create)
//   - no replacement-node logic if a worker disappears mid-stream
//
// All of those are clean follow-ups; the orchestrator + supervisor + store
// types are designed to support them without changing the wire shape.
package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hadihonarvar/flock/internal/agent"
	"github.com/hadihonarvar/flock/internal/models"
	"github.com/hadihonarvar/flock/internal/store"
)

// Orchestrator owns the shard-lifecycle operations on the leader.
type Orchestrator struct {
	Store      store.Store
	Supervisor *agent.Supervisor // leader's own supervisor (coordinator runs here)
	Log        *slog.Logger
	HTTP       *http.Client
}

// New returns a configured orchestrator.
func New(st store.Store, sup *agent.Supervisor, log *slog.Logger) *Orchestrator {
	if log == nil {
		log = slog.Default()
	}
	return &Orchestrator{
		Store:      st,
		Supervisor: sup,
		Log:        log,
		HTTP:       &http.Client{Timeout: 60 * time.Second},
	}
}

// CreateSharded launches all the processes needed to serve one sharded model.
// Steps:
//  1. Validate the catalog entry's ShardingSpec.
//  2. Pick N worker nodes (descending free-RAM bin-pack).
//  3. POST /v1/process/start to each worker to launch `rpc-server -p <port>`.
//  4. Launch `llama-server -m <model> --rpc <list> --port <coord>` locally.
//  5. Persist N+1 Shard rows and a single Placement row pointing at "local".
//
// On any failure, all previously-launched processes are stopped via Rollback
// before returning.
func (o *Orchestrator) CreateSharded(ctx context.Context, entry models.Entry, shardCount int) error {
	if !entry.Sharding.Required {
		return fmt.Errorf("model %s is not configured for sharding", entry.ID)
	}
	if shardCount <= 0 {
		shardCount = entry.Sharding.DefaultShards
	}
	if shardCount < 2 {
		return fmt.Errorf("sharding requires at least 2 shards (got %d)", shardCount)
	}
	if entry.Source.Path == "" {
		return fmt.Errorf("sharded model %s requires source.path (local GGUF path)", entry.ID)
	}

	workers, err := o.pickWorkers(ctx, shardCount)
	if err != nil {
		return fmt.Errorf("pick workers: %w", err)
	}

	rpcPortBase := entry.Sharding.RPCPortBase
	if rpcPortBase == 0 {
		rpcPortBase = 50052
	}
	coordPort := entry.Sharding.CoordinatorPort
	if coordPort == 0 {
		coordPort = 9001
	}

	created := make([]store.Shard, 0, shardCount+1)
	rpcEndpoints := make([]string, 0, shardCount)

	// For source.type=file (sharded GGUFs), make sure every shard host has
	// the file locally. Skips upload when the file is already present with
	// matching sha. Was a v0.5 ask — without this, an admin had to scp the
	// GGUF onto every machine before `flock shard create` could succeed.
	if entry.Source.Type == "file" && entry.Source.Path != "" {
		if err := o.ensureGGUFOnAllWorkers(ctx, workers, entry.Source.Path); err != nil {
			return fmt.Errorf("distribute GGUF: %w", err)
		}
	}

	// Launch one rpc-server per worker.
	for i, w := range workers {
		port := rpcPortBase + i
		shardID := fmt.Sprintf("s-%s-rpc-%d", safeID(entry.ID), i)
		spec := agent.ProcessSpec{
			ID:         shardID,
			Command:    "rpc-server",
			Args:       []string{"-p", strconv.Itoa(port), "-H", "0.0.0.0"},
			HealthPort: port,
			// Worker probes via 127.0.0.1; the leader will dial via worker's address below.
			HealthHost: "127.0.0.1",
			// If the rpc-server dies mid-stream the model goes unavailable —
			// auto-restart up to 5 times (1s, 2s, 4s, 8s, 16s backoffs) so
			// an admin doesn't have to re-run `flock shard create` for a
			// transient OOM or crash. After 5 the process enters "crashloop"
			// and the operator needs to intervene.
			Restart:        true,
			MaxRestarts:    5,
			RestartBackoff: time.Second,
		}
		o.Log.Info("starting rpc shard", "model", entry.ID, "node", w.ID, "port", port)
		if _, err := o.callWorkerStart(ctx, w, spec); err != nil {
			o.rollback(ctx, created)
			return fmt.Errorf("launch rpc on %s: %w", w.ID, err)
		}
		wHost, _, sErr := net.SplitHostPort(w.Address)
		if sErr != nil {
			wHost = w.Address
		}
		endpoint := fmt.Sprintf("%s:%d", wHost, port)
		rpcEndpoints = append(rpcEndpoints, endpoint)
		rec := store.Shard{
			ID: shardID, ModelID: entry.ID, Role: "rpc",
			NodeID: w.ID, Address: endpoint, ProcessID: shardID,
			Status:    "ready",
			CreatedAt: time.Now(), LastSeen: time.Now(),
		}
		if err := o.Store.Shards().Create(ctx, rec); err != nil {
			o.rollback(ctx, append(created, rec))
			return fmt.Errorf("persist shard %s: %w", shardID, err)
		}
		created = append(created, rec)
	}

	// Pick the coordinator host. Default: whichever node (leader or worker)
	// has the most RAM, so the strongest box owns the cross-node aggregation
	// instead of always pinning to the leader. Override via env
	// FLOCK_COORDINATOR_NODE=<node_id> (use "local" for the leader).
	coordHost := o.pickCoordinatorHost(ctx, workers)

	// Launch the coordinator. Two branches: on the leader we use the local
	// supervisor; on a worker we POST /v1/process/start exactly like rpc-server.
	coordID := fmt.Sprintf("s-%s-coord", safeID(entry.ID))
	coordHostBind := "127.0.0.1"
	if !coordHost.local {
		// Worker coordinator must bind on the network so the leader can dial.
		coordHostBind = "0.0.0.0"
	}
	coordSpec := agent.ProcessSpec{
		ID:      coordID,
		Command: "llama-server",
		// Coordinator also benefits from restart-on-crash — without it,
		// llama-server dying takes the model offline even if every rpc-server
		// is fine.
		Restart:        true,
		MaxRestarts:    5,
		RestartBackoff: time.Second,
		Args: []string{
			"-m", entry.Source.Path,
			"--rpc", strings.Join(rpcEndpoints, ","),
			"--port", strconv.Itoa(coordPort),
			"--host", coordHostBind,
		},
		HealthPort: coordPort,
		HealthHost: "127.0.0.1", // worker probes itself via 127.0.0.1
	}
	o.Log.Info("starting coordinator",
		"model", entry.ID, "port", coordPort, "shards", len(rpcEndpoints),
		"host", coordHost.nodeID, "local", coordHost.local)

	if coordHost.local {
		if _, err := o.Supervisor.Start(ctx, coordSpec); err != nil {
			o.rollback(ctx, created)
			return fmt.Errorf("launch coordinator (local): %w", err)
		}
	} else {
		if _, err := o.callWorkerStart(ctx, *coordHost.node, coordSpec); err != nil {
			o.rollback(ctx, created)
			return fmt.Errorf("launch coordinator on %s: %w", coordHost.nodeID, err)
		}
	}

	// Address the *leader's router* will use to dial the coordinator.
	// Local: loopback. Remote: the worker's mesh address + the coord port.
	coordAddr := fmt.Sprintf("127.0.0.1:%d", coordPort)
	coordNodeID := "local"
	if !coordHost.local {
		coordNodeID = coordHost.nodeID
		host, _, sErr := net.SplitHostPort(coordHost.node.Address)
		if sErr != nil {
			host = coordHost.node.Address
		}
		coordAddr = fmt.Sprintf("%s:%d", host, coordPort)
	}

	coordRec := store.Shard{
		ID: coordID, ModelID: entry.ID, Role: "coordinator",
		NodeID: coordNodeID, Address: coordAddr,
		ProcessID: coordID, Status: "ready",
		CreatedAt: time.Now(), LastSeen: time.Now(),
	}
	if err := o.Store.Shards().Create(ctx, coordRec); err != nil {
		// coordinator running but not persisted; try to stop + return error
		if coordHost.local {
			_ = o.Supervisor.Stop(coordID)
		} else {
			_ = o.callWorkerStop(ctx, *coordHost.node, coordID)
		}
		o.rollback(ctx, created)
		return fmt.Errorf("persist coordinator: %w", err)
	}
	// (The `created` slice isn't read after this point — we're past the
	// rollback window. Successful return follows.)

	// Register a placement so the Router knows local hosts this model.
	if err := o.Store.Placements().Upsert(ctx, store.Placement{
		NodeID: "local", ModelID: entry.ID, Status: "ready", LastSeen: time.Now(),
	}); err != nil {
		o.Log.Warn("placement upsert failed for sharded model", "model", entry.ID, "err", err)
	}

	// Persist the model row too so /v1/models reflects the new model.
	_ = o.Store.Models().Upsert(ctx, store.Model{
		ID: entry.ID, CatalogID: entry.ID,
		Source: "llamacpp:" + entry.Source.Path,
		Status: "ready", SizeBytes: entry.SizeBytes,
		InstalledAt: time.Now(),
	})

	o.Log.Info("sharded model ready", "model", entry.ID, "shards", shardCount, "coordinator", coordRec.Address)
	return nil
}

// RemoveSharded tears down every shard for the given model: stops the
// coordinator locally, asks each worker to stop its rpc-server, deletes
// all shard + placement + model rows.
func (o *Orchestrator) RemoveSharded(ctx context.Context, modelID string) error {
	shards, err := o.Store.Shards().GetByModel(ctx, modelID)
	if err != nil {
		return err
	}
	for _, s := range shards {
		switch s.Role {
		case "coordinator":
			if s.NodeID == "" || s.NodeID == "local" {
				if err := o.Supervisor.Stop(s.ProcessID); err != nil {
					o.Log.Warn("coordinator stop failed", "id", s.ID, "err", err)
				}
			} else {
				node, err := o.Store.Nodes().Get(ctx, s.NodeID)
				if err != nil || node == nil {
					o.Log.Warn("coordinator's node not found", "id", s.ID, "node", s.NodeID)
					continue
				}
				if err := o.callWorkerStop(ctx, *node, s.ProcessID); err != nil {
					o.Log.Warn("remote coordinator stop failed", "id", s.ID, "err", err)
				}
			}
		case "rpc":
			node, err := o.Store.Nodes().Get(ctx, s.NodeID)
			if err != nil || node == nil {
				o.Log.Warn("rpc shard's node not found", "id", s.ID, "node", s.NodeID)
				continue
			}
			if err := o.callWorkerStop(ctx, *node, s.ProcessID); err != nil {
				o.Log.Warn("rpc shard stop failed", "id", s.ID, "err", err)
			}
		}
	}
	if err := o.Store.Shards().DeleteByModel(ctx, modelID); err != nil {
		return err
	}
	if err := o.Store.Placements().Delete(ctx, "local", modelID); err != nil {
		o.Log.Warn("placement delete failed", "err", err)
	}
	_ = o.Store.Models().Delete(ctx, modelID)
	return nil
}

// pickWorkers selects N nodes ordered by descending RAM. Future revisions
// will incorporate GPU memory, current load, and same-site preference.
func (o *Orchestrator) pickWorkers(ctx context.Context, n int) ([]store.Node, error) {
	all, err := o.Store.Nodes().List(ctx)
	if err != nil {
		return nil, err
	}
	ready := make([]store.Node, 0, len(all))
	for _, nd := range all {
		if nd.State == "ready" && nd.Address != "" {
			ready = append(ready, nd)
		}
	}
	if len(ready) < n {
		return nil, fmt.Errorf("need %d ready workers, have %d", n, len(ready))
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i].RAMGB > ready[j].RAMGB })
	return ready[:n], nil
}

// rollback stops every successfully-launched process so failed creates
// don't leave orphan rpc-servers running on workers.
func (o *Orchestrator) rollback(ctx context.Context, created []store.Shard) {
	for _, s := range created {
		switch s.Role {
		case "coordinator":
			if s.NodeID == "" || s.NodeID == "local" {
				_ = o.Supervisor.Stop(s.ProcessID)
			} else if node, err := o.Store.Nodes().Get(ctx, s.NodeID); err == nil && node != nil {
				_ = o.callWorkerStop(ctx, *node, s.ProcessID)
			}
		case "rpc":
			node, err := o.Store.Nodes().Get(ctx, s.NodeID)
			if err == nil && node != nil {
				_ = o.callWorkerStop(ctx, *node, s.ProcessID)
			}
		}
		_ = o.Store.Shards().Delete(ctx, s.ID)
	}
}

// coordinatorChoice describes who'll run the llama-server coordinator.
type coordinatorChoice struct {
	nodeID string      // "local" for leader, else node row id
	local  bool        // true when coordinator runs on the leader's supervisor
	node   *store.Node // populated when local==false; the worker to dial
}

// pickCoordinatorHost picks the strongest host (by RAM) among workers + the
// leader to run the llama-server coordinator. Operators can override via
// env FLOCK_COORDINATOR_NODE=<node_id> ("local" forces leader).
//
// Why this exists: the coordinator does the actual layer aggregation across
// rpc-servers. Pinning it to the leader was the v0.4 default and wasted
// capacity when a worker had more RAM than the leader.
func (o *Orchestrator) pickCoordinatorHost(ctx context.Context, workers []store.Node) coordinatorChoice {
	if override := os.Getenv("FLOCK_COORDINATOR_NODE"); override != "" {
		if override == "local" {
			return coordinatorChoice{nodeID: "local", local: true}
		}
		for i := range workers {
			if workers[i].ID == override {
				w := workers[i]
				return coordinatorChoice{nodeID: w.ID, node: &w}
			}
		}
		o.Log.Warn("FLOCK_COORDINATOR_NODE not in shard worker set — falling back to default", "want", override)
	}

	// Default policy: pick the highest-RAM worker; only fall back to the
	// leader when there are no workers (single-machine sharding test).
	// Operators who want the leader can set FLOCK_COORDINATOR_NODE=local.
	if len(workers) == 0 {
		return coordinatorChoice{nodeID: "local", local: true}
	}
	best := workers[0]
	for _, w := range workers[1:] {
		if w.RAMGB > best.RAMGB {
			best = w
		}
	}
	return coordinatorChoice{nodeID: best.ID, node: &best}
}

// ---- HTTP calls to worker process endpoints ----

func (o *Orchestrator) callWorkerStart(ctx context.Context, node store.Node, spec agent.ProcessSpec) (*agent.ProcessInfo, error) {
	body, _ := json.Marshal(spec)
	url := workerURL(node.Address) + "/v1/process/start"
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+node.WorkerToken)
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s: %s", resp.Status, string(b))
	}
	var info agent.ProcessInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decode info: %w", err)
	}
	return &info, nil
}

func (o *Orchestrator) callWorkerStop(ctx context.Context, node store.Node, processID string) error {
	body, _ := json.Marshal(map[string]string{"id": processID})
	url := workerURL(node.Address) + "/v1/process/stop"
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+node.WorkerToken)
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: %s", resp.Status, string(b))
	}
	return nil
}

func workerURL(address string) string {
	if strings.HasPrefix(address, "http://") || strings.HasPrefix(address, "https://") {
		return strings.TrimRight(address, "/")
	}
	return "http://" + address
}

func safeID(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			out = append(out, c)
		} else if c >= 'A' && c <= 'Z' {
			out = append(out, c+32)
		} else {
			out = append(out, '-')
		}
	}
	return string(out)
}
