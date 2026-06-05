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

	// Launch the coordinator locally.
	coordID := fmt.Sprintf("s-%s-coord", safeID(entry.ID))
	coordSpec := agent.ProcessSpec{
		ID:      coordID,
		Command: "llama-server",
		Args: []string{
			"-m", entry.Source.Path,
			"--rpc", strings.Join(rpcEndpoints, ","),
			"--port", strconv.Itoa(coordPort),
			"--host", "127.0.0.1",
		},
		HealthPort: coordPort,
		HealthHost: "127.0.0.1",
	}
	o.Log.Info("starting coordinator", "model", entry.ID, "port", coordPort, "shards", len(rpcEndpoints))
	if _, err := o.Supervisor.Start(ctx, coordSpec); err != nil {
		o.rollback(ctx, created)
		return fmt.Errorf("launch coordinator: %w", err)
	}
	coordRec := store.Shard{
		ID: coordID, ModelID: entry.ID, Role: "coordinator",
		NodeID: "local", Address: fmt.Sprintf("127.0.0.1:%d", coordPort),
		ProcessID: coordID, Status: "ready",
		CreatedAt: time.Now(), LastSeen: time.Now(),
	}
	if err := o.Store.Shards().Create(ctx, coordRec); err != nil {
		// coordinator running but not persisted; try to stop + return error
		_ = o.Supervisor.Stop(coordID)
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
			if err := o.Supervisor.Stop(s.ProcessID); err != nil {
				o.Log.Warn("coordinator stop failed", "id", s.ID, "err", err)
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
			_ = o.Supervisor.Stop(s.ProcessID)
		case "rpc":
			node, err := o.Store.Nodes().Get(ctx, s.NodeID)
			if err == nil && node != nil {
				_ = o.callWorkerStop(ctx, *node, s.ProcessID)
			}
		}
		_ = o.Store.Shards().Delete(ctx, s.ID)
	}
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
