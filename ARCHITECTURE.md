# Flock Architecture

Deep-dive design for contributors and maintainers. For user-facing docs, see [README.md](README.md). For the active implementation plan, see [TASKS.md](TASKS.md).

> **Doc-vs-code currency:** this document describes v0.4 (cross-node routing + sharding auto-orchestration + CLI/UI parity). The code on `main` is the source of truth — if you find a mismatch please file an issue or PR.

---

## Table of contents

- [Goals and non-goals](#goals-and-non-goals)
- [Big picture](#big-picture)
- [Process model](#process-model)
- [Control plane internals](#control-plane-internals)
- [Agent internals](#agent-internals)
- [Mesh networking](#mesh-networking)
- [Storage](#storage)
- [Protocol adapters](#protocol-adapters)
- [Router](#router)
- [Scheduler](#scheduler)
- [Engine drivers](#engine-drivers)
- [Model registry and puller](#model-registry-and-puller)
- [Authentication and authorization](#authentication-and-authorization)
- [Observability](#observability)
- [Security model](#security-model)
- [Why each technology was chosen](#why-each-technology-was-chosen)
- [Concurrency model](#concurrency-model)
- [Project layout](#project-layout)
- [Coding conventions](#coding-conventions)
- [Build from source](#build-from-source)
- [Getting started as a contributor](#getting-started-as-a-contributor)
- [How to extend Flock](#how-to-extend-flock)

---

## Goals and non-goals

### Goals

1. Run on a single laptop *and* a multi-node cluster with the same binary.
2. One-command install. Zero config to first response.
3. Drop-in compatibility with OpenAI and Anthropic APIs.
4. Mac + Linux + NVIDIA in one fleet, transparently.
5. Strong defaults; expert overrides via YAML.
6. Maintainable by junior engineers — small surface, no magic.

### Non-goals

1. Training or fine-tuning.
2. Beating frontier models. We surface them via fallback.
3. Replacing Kubernetes for general workloads.
4. Windows-native workers.

---

## Big picture

```
   CLIENTS  (Cursor · Claude Code · Aider · SDKs · curl)
                       │
                       ▼  one endpoint, one key
   ┌──────────────────────────────────────────────────┐
   │  GATEWAY (leader)                                │
   │  OpenAI + Anthropic compatible · auth · quotas   │
   │  egress dispatcher (claude-* / gpt-* → vendor)   │
   └────────────────────┬─────────────────────────────┘
                        │
   ┌────────────────────▼─────────────────────────────┐
   │  ROUTER  (internal/router)                       │
   │  model → placements → least-loaded node          │
   │  caches remote engine handles per node           │
   └────┬───────────────────────┬─────────────────────┘
        │ local                 │ remote (via worker HTTP)
        ▼                       ▼
   ┌─────────────┐   ┌─────────────────────┐   ┌──────────────────┐
   │ leader's    │   │ Worker A (Mac Mini) │   │ Worker B (NVIDIA)│
   │ local       │   │  agent.Server       │   │  agent.Server    │
   │ engine      │   │  → local Ollama     │   │  → local vLLM    │
   │ (Ollama)    │   │  (token-auth'd)     │   │  (token-auth'd)  │
   └─────────────┘   └─────────────────────┘   └──────────────────┘
                              ▲                         ▲
                              │  heartbeat every 5s     │
                              │  carries loaded_models  │
   ┌──────────────────────────┴─────────────────────────┴──────────┐
   │  CONTROL PLANE                                                │
   │  node registry · model placements · usage · audit · web UI    │
   └───────────────────────────────────────────────────────────────┘
                              ▲
                              │ mesh: LAN (v0.3) or
                              │ embedded Tailscale (v0.4)
```

Two distinct planes:

- **North-south** — clients → gateway → router → engine (local or remote). Data plane. Latency-sensitive. Per-request work; KV caches live in the chosen engine.
- **East-west** — control plane ↔ agents. Cluster management. Lower volume. Direct HTTP today (NATS pub/sub was scoped for sharded events but is not in v0.3).

A control-plane DB outage does **not** kill in-flight requests — the router keeps using its in-memory cache of node addresses + worker tokens. If a node disappears mid-stream, the next request will surface the routing error; the cache is rebuilt from the placements table once the DB is back.

---

## Process model

One binary, four modes determined by subcommand:

| Mode | What runs in-process |
|---|---|
| `flock up` | **Leader**: HTTP gateway · Router · Control plane · Web UI · embedded SQLite · local engine adapter |
| `flock join <url>?token=…` | **Worker**: agent.Loop (heartbeat with loaded_models) · agent.Server (OpenAI-compat passthrough bound to the LAN/tailnet address) · local engine adapter |
| `flock <cmd>` (e.g. `node ls`, `model add`) | One-shot CLI; reads SQLite directly or calls the leader's admin API |
| `flock doctor` | Stand-alone diagnostics — port availability, Ollama reachability, catalog count, hardware summary |
| `flock update` / `flock upgrade` | Hits `api.github.com/repos/hadihonarvar/flock/releases/latest`, downloads the matching platform tarball, verifies SHA-256 against `checksums.txt`, atomically replaces the running binary. Restarts are user-driven (`flock down && flock up`). |

The leader and worker share the same internal packages; the difference is which subsystems are wired up in `cmd/flock/main.go`.

### Process lifecycle

1. `main()` parses subcommand + flags
2. Loads config (`internal/config`)
3. Initializes telemetry (`internal/tracing`, `internal/metrics`)
4. Initializes mesh (`internal/mesh`)
5. Initializes store (`internal/store`)
6. Wires up subsystems based on mode
7. Runs until SIGINT/SIGTERM, then graceful shutdown via context cancellation

### Graceful shutdown

- Stop accepting new HTTP connections
- Wait up to `drain_timeout_s` for in-flight requests
- Detach from NATS
- Close mesh
- Flush metrics, traces, logs
- Close DB

---

## Cross-node routing (the v0.3 core)

The Router is what makes "leverage multiple machines" mean something. It implements `engines.Engine`, so handlers don't know whether a request is served locally or proxied — they just call `h.Engine.Chat(ctx, req)`.

```
   Handler.Chat(req)
        │
        ▼
   Router.pick(model)              ← internal/router/router.go
        │
        ├─ store.Placements.GetByNode("local", model) → has it? → return local engine
        │
        └─ store.Placements.GetByModel(model)
                │
                ▼
           filter: status == "ready"
                │
                ▼
           sort by router.inflight[nodeID] ASC
                │
                ▼
           pick first → store.Nodes.Get(id) → build/cached VLLM driver
                                              pointing at node.Address
                                              with node.WorkerToken
                ▼
           return remoteEngine
```

### Selection policy (v0.3)

1. **Local first.** If the leader's local engine has the model, use it. Lowest latency, no network hop.
2. **Least-loaded worker** otherwise. The router maintains an in-process `map[nodeID]int` of in-flight request counts and picks the lowest.
3. **Fall back to local** if no node has the model. Local will return a clear "model not found" the client can act on.

The router's wrapping of the engine channel decrements the in-flight counter when the upstream stream closes, so counts stay accurate without explicit acknowledgement from the caller.

### Worker HTTP server (`internal/agent/server.go`)

Each worker runs a thin OpenAI-compatible HTTP server bound to the address it reported at registration time. The server has three routes:

| Route | Behavior |
|---|---|
| `GET /healthz` | Calls `Engine.Health(ctx)`; returns 200 if the local engine is reachable. |
| `GET /v1/models` | Calls `Engine.List(ctx)` and emits the OpenAI `{"object":"list","data":[…]}` shape. |
| `POST /v1/chat/completions` | Decodes the OpenAI request, calls `Engine.Chat(ctx, req)`, re-emits as SSE (stream=true) or aggregated JSON (stream=false). |

Auth is token-only: the request must carry `Authorization: Bearer <worker_token>`. The worker_token is established at registration and stored on the leader's `nodes` row.

### Placements (`internal/store/sqlite.go → model_placements`)

```sql
CREATE TABLE model_placements (
    node_id    TEXT NOT NULL,    -- "local" for the leader, or a worker node id
    model_id   TEXT NOT NULL,    -- the engine-native model id (e.g. "llama3.2:1b")
    status     TEXT NOT NULL,    -- "ready" | "loading" | "error"
    last_seen  INTEGER NOT NULL,
    PRIMARY KEY (node_id, model_id)
);
CREATE INDEX idx_placements_model ON model_placements(model_id);
```

Worker heartbeats carry `loaded_models`; the leader calls `PlacementStore.ReplaceForNode(nodeID, …)` to reconcile atomically every 5s. Local placements (`node_id="local"`) are populated by `cmd_model.go` on add and by `cmd_up.go` on startup (it lists the leader's local engine).

### Sharding auto-orchestration (v0.4)

For models that don't fit on a single machine, `llama.cpp`'s `--rpc` mode lets the model be split across multiple nodes. **v0.4 automates the entire orchestration** — no SSHing into workers, no managing rpc-server processes by hand.

#### Components

| File | Role |
|---|---|
| `internal/agent/supervisor.go` | Process supervisor used on both leader and workers. Start/Stop/Logs with a TCP readiness probe. |
| `internal/agent/server.go` | Worker exposes `POST /v1/process/start`, `/stop`, `/list`, `/logs` — token-auth'd, calls into the supervisor. |
| `internal/scheduler/sharding.go` | Leader-side `Orchestrator.CreateSharded` / `RemoveSharded`. Picks workers, calls their process endpoints, launches the coordinator locally, persists shard rows. |
| `internal/engines/llamacpp_rpc.go` | Driver that talks OpenAI-compat to a `llama-server` (the coordinator). Same shape as vLLM/MLX. |
| `internal/router/router.go` | `shardCoordinator()` short-circuits the normal placement lookup when a sharded model is requested — points the request at the coordinator's address. |

#### Flow: `flock shard create llama-3.3-70b-sharded 2`

```
  CLI → POST /admin/v1/shards/create on the leader
            │
            ▼
   Orchestrator.CreateSharded(entry, 2):
       │
       ├─ pickWorkers(2) — ready nodes, descending RAM
       │
       ├─ for each worker i:
       │     spec = { id, command: "rpc-server", args: ["-p", port],
       │              healthPort: port }
       │     POST <worker>/v1/process/start
       │     (worker supervisor launches rpc-server,
       │      waits for TCP readiness on port, returns PID)
       │     persist Shard{role:"rpc", node_id:<worker>, address:<worker>:<port>}
       │
       ├─ leader.Supervisor.Start("llama-server",
       │     args: ["-m", <gguf>, "--rpc", "w1:port,w2:port", "--port", 9001])
       │   wait for TCP readiness on 9001
       │   persist Shard{role:"coordinator", node_id:"local", address:"127.0.0.1:9001"}
       │
       └─ Placement{node_id:"local", model_id:<id>, status:"ready"}

   Now the Router sees this placement; when a client requests the model,
   shardCoordinator() returns a llamacpp engine pointing at 127.0.0.1:9001.
```

#### Failure handling

- If any rpc-server fails to come up (readiness timeout, process exits), `Orchestrator.rollback()` stops every previously-launched process and returns the error to the CLI/UI.
- If a shard process crashes *after* CreateSharded returns, v0.4 does nothing — the model becomes unavailable until the admin re-runs the create. v0.5 will add a watcher loop that detects exited shards and restarts them.

#### Out of scope for v0.4

- Coordinator on a worker (always on the leader today).
- Automatic GGUF download to workers (the GGUF must already be on the leader at `source.path`).
- Live shard migration / rebalancing.
- Dynamic shard count change.

---

## Control plane internals

```
                       ┌──────────────────────────────────┐
                       │           HTTP Server             │
                       │   (chi router, embedded UI)       │
                       └──────────┬───────────────────────┘
                                  │
       ┌────────────┬─────────────┼────────────┬──────────────┐
       ▼            ▼             ▼            ▼              ▼
   ┌────────┐  ┌─────────┐  ┌──────────┐  ┌─────────┐  ┌──────────┐
   │  API   │  │  Admin  │  │   Auth   │  │ Metrics │  │  Web UI  │
   │adapters│  │  API    │  │ (keys,   │  │         │  │ (embed)  │
   │ OAI/   │  │         │  │  OIDC)   │  │         │  │          │
   │ Anthr  │  │         │  │          │  │         │  │          │
   └───┬────┘  └────┬────┘  └──────────┘  └─────────┘  └──────────┘
       │            │
       ▼            ▼
   ┌──────────────────────┐
   │       Router         │  ── picks a node + protocol for a request
   └────────┬─────────────┘
            │
            ▼
   ┌──────────────────────┐      ┌───────────────────┐
   │  Scheduler           │◄────►│  Node registry    │
   │  (placement, drain)  │      │  (capabilities)   │
   └────────┬─────────────┘      └───────────────────┘
            │
            ▼
   ┌──────────────────────┐      ┌───────────────────┐
   │  Model registry      │◄────►│  Model puller     │
   │  (catalog + state)   │      │  (HF, MinIO)      │
   └──────────────────────┘      └───────────────────┘

   All state above lives in SQLite via the `store` package.
   All eventing (heartbeats, assignments) flows through NATS.
```

### Subsystem responsibilities

- **HTTP server** — request routing, TLS termination, middleware stack
- **API adapters** — translate OpenAI/Anthropic requests to internal `InferenceRequest`; translate responses back
- **Admin API** — node management, model management, token issuance, usage queries
- **Auth** — API key validation, OIDC, token issuance
- **Router** — given a request, pick a target node + engine endpoint
- **Scheduler** — model placement decisions, drain operations, replication
- **Node registry** — current cluster state, heartbeat tracking
- **Model registry** — what models exist (catalog), where they live (placement), what state they're in
- **Model puller** — download weights from HF/MinIO with resume

### CLI / Admin API / Web UI contract

This is a load-bearing architectural rule, not a style preference:

**The `flock` CLI is the canonical control surface.** Every user-facing mutation — `flock model add`, `flock model remove`, `flock default <id>`, `flock shard create`, `flock node drain`, `flock token create`, etc. — is implemented as an exported Go function in `internal/control/`. The CLI command in `cmd/flock/` is a thin arg-parser that calls this function. The admin HTTP endpoint that backs the same action in the web UI is a thin request-decoder that calls the **same** function.

```
   ┌──────────────┐         ┌──────────────┐
   │   CLI cmd    │         │  Web UI POST │
   │   (cmd/flock)│         │  (internal/  │
   │              │         │   ui/*.html) │
   └──────┬───────┘         └──────┬───────┘
          │                        │
          ▼                        ▼
   ┌──────────────┐         ┌──────────────┐
   │ arg-parsing  │         │ req-decoding │
   │ + flag       │         │ + auth       │
   │ resolution   │         │ check        │
   └──────┬───────┘         └──────┬───────┘
          │                        │
          └────────────┬───────────┘
                       ▼
            ┌────────────────────┐
            │ internal/control/  │  ◄── one place mutating logic lives
            │  ModelAdd()        │
            │  ModelRemove()     │
            │  SetDefault()      │
            │  ShardCreate()     │
            │  …                 │
            └────────────────────┘
```

**Why this matters:**
- Anything you can do in the dashboard, you can do in a script. Anything you can do in a script, the dashboard can do.
- Behavior is identical across surfaces — the same audit log entry, the same validation, the same error messages.
- A web UI bug can't drift from CLI behavior (or vice versa) because there's only one implementation.
- New capabilities ship CLI-first (with `--help`), and the UI follows. This forces the developer to think about scriptability and headless operation before pixel-pushing.

See **M4-T20** in TASKS.md for the refactor that codifies this. After M4-T20 lands, `internal/api/admin_*.go` contains no mutating logic — only request decoding and a call into `internal/control/`.

### Implemented examples (the pattern in production)

As of 2026-06-05 the onboarding-and-sharing endpoints follow this pattern strictly — use them as references when writing new ones:

| CLI command | `internal/control/` function | Admin endpoint (in `internal/controlplane/`) |
|---|---|---|
| `flock connect <client>` | `control.ConnectSnippet()` + `control.Clients()` | `POST /admin/v1/connect/snippet`, `GET /admin/v1/connect/clients` (in `admin_connect.go`) |
| `flock disconnect <client>` | `control.DisconnectSnippet()` | (no HTTP endpoint — purely local string lookup; the reversal text is static per client) |
| `flock invite <name>` | `control.Invite()` | `POST /admin/v1/invite` (in `admin_invite.go`) |
| (dashboard-only) | — | `POST /admin/v1/healthcheck` (in `admin_healthcheck.go`) — calls `s.openaiH.ResolveModel()` + `s.router.Chat()` to send a tiny ping through the same path real requests take |

`internal/control/snippets/*.tmpl` are `go:embed`-ed templates — adding a new supported client is a one-file change. Existing CLI/admin pairs (model add, token create, node drain, etc.) still duplicate logic and will move into `internal/control/` as part of the rest of M4-T20.

---

## Agent internals

```
   ┌────────────────────────────────────┐
   │            Agent loop              │
   │   (one goroutine per concern)      │
   └────┬───────┬────────┬─────────┬────┘
        │       │        │         │
        ▼       ▼        ▼         ▼
   ┌────────┐┌────────┐┌────────┐┌──────────┐
   │Heart-  ││Capa-   ││Engine  ││Model     │
   │beat    ││bility  ││driver  ││puller    │
   │loop    ││report  ││(start/ ││(HF →     │
   │        ││        ││ stop/  ││ disk)    │
   │        ││        ││ health)││          │
   └────────┘└────────┘└────────┘└──────────┘
        │       │        │         │
        ▼       ▼        ▼         ▼
   ┌──────────────────────────────────────┐
   │            NATS connection           │
   └──────────────────────────────────────┘
```

The agent subscribes to `assignment.<node-id>` and reacts to messages like "load model X" or "drain". Heartbeats publish to `heartbeat.<node-id>` every 5s. Capability reports go on `capabilities.<node-id>` at startup and whenever hardware state changes.

### Capability detection

- macOS: `system_profiler SPHardwareDataType -json`, `sysctl hw.memsize`
- Linux + NVIDIA: `nvidia-smi --query-gpu=…`, `/proc/meminfo`, `/proc/cpuinfo`
- Linux + AMD: `rocm-smi`
- Generic: GOOS, GOARCH, hostname, kernel

Output: a `Capabilities{}` struct with RAM, GPUs (model, VRAM), CPU cores, OS, available engines.

---

## Mesh networking

We embed Tailscale's `tsnet` library inside the binary so each Flock process is itself a tailnet node.

### Why tsnet

- NAT traversal works without firewall config
- WireGuard noise protocol = mTLS-equivalent
- Discovery by name (`<node>.<tailnet>.ts.net`)
- Stable IPs across network changes
- Works across NATs, VPNs, Wi-Fi, LTE
- One Go import

### Boot sequence

1. On `flock up` (leader): create a tailnet (or reuse configured one), generate auth key, persist to store
2. On `flock join`: receive auth key in token, pass to `tsnet`, dial `leader.<tailnet>.ts.net`
3. tsnet exposes a `net.Listener` and `Dial(ctx, addr)` — everything sits on top

### Alternative backends

Pluggable via `internal/mesh`:

- `tailscale` — default, embedded tsnet
- `netbird` — for orgs already on NetBird
- `lan` — pure local LAN, no overlay; mDNS for discovery
- `headscale` — self-hosted Tailscale control server (for air-gapped)

---

## Storage

### SQLite (default)

- File at `~/.flock/state.db`
- WAL mode for concurrent reads with one writer
- Goose / golang-migrate for schema migrations in `internal/store/migrations/`
- sqlx for typed queries; no ORM
- Schema lives in `internal/store/schema.sql`

### Tables

```
nodes          (id, tailnet_addr, hardware_json, state, last_heartbeat, …)
models         (id, catalog_id, source, status, size_bytes, …)
placements     (model_id, node_id, status, loaded_at)
users          (id, email, oidc_sub, created_at)
api_keys       (id, user_id, hash, scopes, quota, revoked, …)
tokens         (id, kind, hash, expires_at, used_at)
audit_log      (id, ts, user_id, action, target, metadata_json)
usage          (id, ts, user_id, model, prompt_tokens, completion_tokens, …)
metrics_cache  (key, value, updated_at)
```

### Postgres (for HA — v1.0)

Same schema, swap the driver. `internal/store` exposes an interface; both backends implement it.

### Model files

Not in SQLite. Stored on each node's disk at `~/.flock/models/<sha256>/`. The model registry records which nodes have which file.

For a MinIO mirror (optional): an admin can configure `storage.models_mirror` and the puller fetches from MinIO instead of HuggingFace.

---

## Protocol adapters

### OpenAI adapter (`internal/api/openai_adapter.go`)

- Parses `/v1/chat/completions` request into `InferenceRequest`
- Streams tokens back as SSE `data: {...}\n\n`
- Handles function-call format conversion if backend uses Anthropic native tools

### Anthropic adapter (`internal/api/anthropic_adapter.go`)

- Parses `/v1/messages` request into `InferenceRequest`
- Maps `system` field → system message in internal format
- Maps Anthropic tool blocks → internal tool calls
- Translates streaming events:
  - `message_start` → opens stream
  - `content_block_start` / `content_block_delta` / `content_block_stop` per block
  - `message_delta` for usage updates
  - `message_stop` to close

### Internal request shape

```go
type InferenceRequest struct {
    Model        string
    Messages     []Message
    System       string
    Tools        []Tool
    Stream       bool
    MaxTokens    int
    Temperature  *float32
    TopP         *float32
    Stop         []string
    UserID       string
    SessionID    string  // for sticky routing
    // ...
}
```

LiteLLM is used as a reference for edge cases in protocol translation but we don't ship it; we hand-write the adapters in Go for control and zero-dep deployment.

---

## Router

Given an authenticated `InferenceRequest`, the router decides:

1. Is `model` a **proxied vendor model**? If yes → forward to vendor adapter (Anthropic / OpenAI / Bedrock) with team-scoped API key.
2. Is `model` `auto`? Apply heuristics:
   - Short prompt with code shape → coder pool
   - Long agentic context with tools → flagship pool
   - Vision input → vision pool
   - Embedding request → embedding pool
3. Otherwise look up `model` in the registry → get list of nodes serving it.
4. Apply scoring per candidate node:
   - Free queue slots (higher = better)
   - Sticky-session match by `SessionID` (huge bonus for KV reuse)
   - Recent latency (lower = better)
   - Network distance (same site = better)
5. Pick winner; open HTTP/SSE connection to its local engine.
6. Stream response back through gateway, accumulating token counts.

### Sticky sessions

- `SessionID` derived from `userID + first message hash` or explicit header `X-Session-Id`
- Bound to a node for `session_ttl_s` (default 600s)
- Soft binding: if the node is overloaded, router will move the session and absorb the cache miss

---

## Scheduler

Runs on the leader. Watches the node registry and model registry. Goals:

1. Every requested model is loaded on at least 1 node it fits on.
2. Highly-used models get replicas to handle load.
3. Drains complete without dropping requests.

### Placement algorithm (v0.2)

```
for each requested model M, sorted by priority (size desc, requests desc):
  candidates = nodes whose free RAM/VRAM >= M.size + headroom
  if candidates empty:
    if M can be sharded → try llama.cpp RPC across N nodes
    else → mark M as unschedulable, alert
  else:
    pick candidate with most free capacity (binpack=false)
    or least free capacity (binpack=true)
    issue assignment via NATS
```

### Drain algorithm

```
mark node as draining (no new sessions routed to it)
for each model M on the node:
  if M has another replica → done
  else → schedule M on another node, wait for ready
wait drain_timeout_s for in-flight requests
remove node from registry
```

### Replication

- `auto` — start with 1 replica; scheduler adds replicas when sustained queue depth > threshold for >5 min
- `always` — every model gets ≥2 replicas if hardware allows
- `never` — exactly 1 replica per model

---

## Engine drivers

Each driver is a Go package under `internal/engines/` implementing (from `internal/engines/types.go`):

```go
type Engine interface {
    Name() string
    Endpoint() string
    Health(ctx context.Context) error

    List(ctx context.Context) ([]string, error)
    Pull(ctx context.Context, modelID string, onProgress func(status string, completed, total int64)) error
    Delete(ctx context.Context, modelID string) error

    Chat(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)
}
```

### Implemented (or planned for v0.2)

- **Ollama** — easiest dev backend. Driver shells out to `ollama` CLI for pulls; talks to its HTTP API.
- **vLLM** — for NVIDIA. Driver runs the official Docker image or local install with the right `--model`, `--tensor-parallel-size`, `--max-model-len` flags.
- **MLX-LM** — for Apple Silicon. Driver runs `mlx_lm.server` in a managed subprocess.
- **llama.cpp** — universal fallback. Driver runs `llama-server` with the right `-m`, `-c`, `--rpc` flags.

### Adding a new engine

1. Implement `Engine` in `internal/engines/<name>.go`
2. Register in `internal/engines/registry.go`
3. Add capability matching: when does the scheduler pick you?
4. Tests in `<name>_test.go`

---

## Model registry and puller

### Catalog

YAML files in `catalog/<id>.yaml`:

```yaml
id: qwen3-coder
display_name: Qwen3 Coder 30B-A3B
source:
  type: huggingface
  repo: Qwen/Qwen3-Coder-30B-A3B-Instruct-AWQ
size_bytes: 21474836480
quant: awq
context_window: 262144
capabilities: [chat, tools, code]
recommended_engines: [vllm, mlx]
hardware:
  min_vram_gb: 22
  min_ram_gb: 32
tags: [coding, agent]
```

Loaded into the model registry at startup. Users add via `flock model add qwen3-coder`.

### Puller

- Downloads files in parallel chunks
- Resumes interrupted transfers
- Verifies SHA256 of each file
- Supports `hf:owner/repo`, `https://...`, `file:./local.gguf`, `s3://...`, `minio://...`
- Caches to `~/.flock/models/<sha256>/`
- Multi-node deployments can configure a MinIO mirror to avoid re-downloading per node

---

## Authentication and authorization

### API keys

- Format: `sk-orc-` + 32 url-safe random bytes
- Stored as bcrypt hashes
- Scopes: `inference`, `admin`, `node` (join token)
- Per-key quotas: daily token cap, monthly token cap
- Revocable at any time

### Token types

| Kind | Purpose | TTL |
|---|---|---|
| `api` | User keys for `/v1/...` | until revoked |
| `admin` | Cluster admin operations | until revoked |
| `node` | One-shot join token | 5 minutes |
| `invite` | OIDC invite for new user | 24 hours |

### OIDC (web UI)

- Generic OIDC: provide issuer, client ID, client secret
- Built-in: Google, GitHub, Okta presets
- Session via signed cookie
- First user becomes admin; subsequent users invited

### Authorization model

- Roles: `admin`, `user`, `viewer`
- Models can be scoped: `model.allowed_roles`
- Per-user model whitelist (optional)
- All admin actions go through `internal/auth/policy.go`

---

## Observability

### Metrics

Declared in `internal/metrics/metrics.go`. Exposed at `:9090/metrics` (configurable).

Key series:

- `flock_request_duration_seconds{model,protocol,outcome}` — histogram
- `flock_request_tokens{model,direction}` — counter
- `flock_request_ttft_seconds{model}` — histogram
- `flock_node_up{node,hardware}` — gauge
- `flock_node_gpu_util{node,gpu}` — gauge
- `flock_node_memory_used_bytes{node}` — gauge
- `flock_queue_depth{model}` — gauge
- `flock_model_loaded{model,node}` — gauge

### Traces

OpenTelemetry. Span hierarchy:

```
gateway.request
├── auth.validate
├── router.decide
├── worker.inference
│   ├── engine.send
│   └── engine.stream
└── usage.record
```

Export via OTLP. Defaults disabled; enable with `observability.otlp_endpoint`.

### Logs

`slog` to stdout in JSON. Levels: debug, info, warn, error. Request IDs propagated through context.

### Dashboards

`dashboards/` ships:

- `cluster-overview.json` — RPS, latency, GPU util, queue depth
- `per-model.json` — TTFT, tok/s, cache hit rate, errors
- `per-user.json` — calls, tokens, quota, cost equivalent

---

## Security model

### Network

- Mesh = WireGuard via Tailscale. Inter-node traffic is encrypted + authenticated.
- Gateway terminates TLS via embedded Caddy (Let's Encrypt) or user-provided certs.
- No node needs an exposed firewall port.

### Auth

- Per-user API keys, revocable.
- OIDC for the web UI.
- Admin keys are separate from user keys, never sent to workers.

### Data

- Request bodies not persisted by default — only metadata (user, model, tokens, latency).
- Opt-in full-payload logging for debugging.
- External-API fallback uses user-scoped provider keys.

### Threat model

| Threat | Mitigation |
|---|---|
| Compromised worker reads other workers' state | Workers have no admin scope; mesh is point-to-point encrypted |
| Leaked user key | One-click revoke; quota caps blast radius |
| Mesh traffic sniffed on host network | WireGuard noise protocol |
| Compromised leader | Treat leader as trust root; rotate admin keys periodically |
| Jailbroken local model | Optional gateway-level moderation hook |
| Supply chain (downloaded weights) | SHA256 verification against catalog or HF |

### Reporting vulnerabilities

`hadi.work.ca@gmail.com` (PGP key in `SECURITY.md`). 90-day disclosure.

---

## Why each technology was chosen

| Choice | Alternatives considered | Why we picked this |
|---|---|---|
| Go | Rust, Python | Single binary, fast enough, big ecosystem for networking |
| `tsnet` for mesh | libp2p, raw WireGuard, custom | Solves NAT traversal + mTLS + discovery in one import; battle-tested |
| SQLite (default) | Postgres, etcd | Embedded, file-backed, no operator; sufficient until ~1k nodes |
| Embedded NATS | Redis pub/sub, gRPC streaming | Embeds in Go cleanly; pub/sub semantics fit "broadcast model state" |
| vLLM / MLX / llama.cpp | Build our own engine | Years of perf work; we'd never catch up |
| Hand-written adapters | LiteLLM as a library | LiteLLM is Python; we want one binary. We use it as a reference. |
| Next.js + embed.FS | SPA served separately | Embedded UI = one binary |
| Chi router | gin, echo, stdlib | Minimal, idiomatic, well-typed |
| Apache 2.0 | MIT, AGPL | Permissive enough for enterprise adoption; patent grant included |

---

## Concurrency model

- The leader has a small fixed set of goroutines: HTTP server, NATS broker, scheduler tick, metrics scraper, drain workers (per drain operation).
- Each in-flight request spawns one goroutine in the gateway and one streaming connection to the worker.
- Locks are scoped to single subsystems. There is no global lock.
- All shared state is in SQLite (durable) or in-memory maps protected by per-key locks (caches).

Rules of thumb:

- Pass `context.Context` first arg, always
- Never store contexts in structs
- Use channels at boundaries between subsystems; mutexes inside one subsystem
- Avoid `sync.Map` unless profiling shows contention on `map + Mutex`
- Don't spawn goroutines without bounded lifetime — every `go` must respect a context

---

## Project layout

```
flock/
├── README.md                  # user docs
├── QUICKSTART.md              # 3-min new user landing page
├── ARCHITECTURE.md            # this file
├── TASKS.md                   # implementation plan
├── LICENSE                    # Apache 2.0
├── SECURITY.md
├── CODE_OF_CONDUCT.md
├── CONTRIBUTING.md            # short pointer to this doc
├── Makefile                   # dev shortcuts
├── go.mod, go.sum
│
├── .github/workflows/         # CI + Release workflows
├── .goreleaser.yaml           # release config
├── .golangci.yml              # lint config
│
├── cmd/flock/                 # single-binary entrypoint + every subcommand
│   ├── main.go                # dispatch + top-level help
│   ├── help.go                # helpSpec / showHelp / dieHelp / wantsHelp
│   ├── common.go              # adminCall + readLocalAdminKey + shared helpers
│   ├── cmd_{up,down,status,join,doctor,version}.go
│   ├── cmd_{node,model,shard,token,usage,audit,config}.go
│   └── …
│
├── internal/
│   ├── controlplane/          # leader HTTP server + admin API + middlewares
│   ├── agent/                 # capability detect + heartbeat loop + worker HTTP + process supervisor
│   ├── api/                   # openai.go + anthropic.go + egress.go + usage.go
│   ├── router/                # model → node dispatch, least-loaded, shard coordinator
│   ├── scheduler/             # sharding.go — orchestrator for sharded model lifecycle
│   ├── mesh/                  # mesh.go — LAN backend (tsnet planned)
│   ├── engines/               # types + ollama/vllm/mlx/llamacpp_rpc drivers + registry
│   ├── models/                # catalog parser (incl. ShardingSpec), auto-pick
│   ├── store/                 # SQLite backend (api_keys / models / nodes / placements / shards / usage / audit)
│   ├── auth/                  # API keys + scope middleware
│   ├── config/                # YAML + env loader
│   ├── metrics/               # Prometheus declarations
│   └── ui/                    # embed.go + index.html (single embedded page)
│
├── catalog/                   # YAML model catalog entries
│   ├── llama-3.2-1b.yaml
│   ├── llama-3.2-3b.yaml
│   ├── llama-3.3-70b-sharded.yaml
│   └── qwen2.5-coder-{7b,14b,32b}.yaml
│
└── installer/
    ├── install.sh             # the curl | sh script
    └── homebrew/flock.rb      # tap formula template (publishing disabled until tap repo exists)
```

*Planned dirs* (not present yet): `web/` (separate Next.js UI alternative to embed), `dashboards/` (Grafana JSON), `deploy/{launchd,systemd,docker}/`, `docs/` (RFC archive), `test/{integration,e2e}/`.

### Naming conventions

- Packages: short, lowercase, no underscores (`controlplane`, not `control_plane`)
- Files: snake_case (`openai_adapter.go`)
- Tests: same file with `_test.go` suffix
- Exported types: PascalCase, exported funcs: PascalCase
- Errors: `errFoo` for sentinel, `ErrFoo` if exported
- Context is always the first arg; never store contexts in structs

---

## Coding conventions

- **Go**: stdlib first, then well-vetted deps (chi, sqlx, nats.go, tsnet, otelgo). No frameworks.
- **Error handling**: wrap with `fmt.Errorf("operation: %w", err)`. Never swallow.
- **Logging**: `slog` only. Levels: debug (verbose), info (user-relevant), warn (degraded), error (request failed).
- **Tests**: table-driven where it fits. No mocks for stdlib. Use real SQLite, real NATS (in-process).
- **HTTP**: handlers are thin; logic lives in services. Handlers do parse → call → respond.
- **Concurrency**: prefer channels at boundaries; use mutexes for small protected state.
- **No `init()` functions** except for package-level registry registration.
- **No global mutable state** beyond metrics and the embedded UI fs.
- **Generics**: only where a type-safe alternative is impossible.
- **File length**: aim under 600 lines; split at 800.

### UI conventions

- TypeScript strict mode
- shadcn/ui components, Tailwind for styles
- No client-side state library (Zustand only if a screen genuinely needs it)
- Data fetching via `swr`; mutations via `fetch`
- Pages are React Server Components by default

---

## Build from source

### Prerequisites

- Go 1.22+
- Node.js 20+ (for the UI)
- Optional: NVIDIA Container Toolkit (for vLLM workers)

### Build

```bash
git clone https://github.com/hadihonarvar/flock
cd flock

# Build the UI first; the Go binary embeds it
(cd web && npm ci && npm run build)

# Build the binary
go build -o flock ./cmd/flock

# Smoke test
./flock version
./flock doctor
./flock up
```

### Cross-compile

```bash
GOOS=linux   GOARCH=amd64 go build -o dist/flock-linux-amd64   ./cmd/flock
GOOS=linux   GOARCH=arm64 go build -o dist/flock-linux-arm64   ./cmd/flock
GOOS=darwin  GOARCH=arm64 go build -o dist/flock-darwin-arm64  ./cmd/flock
GOOS=darwin  GOARCH=amd64 go build -o dist/flock-darwin-amd64  ./cmd/flock
```

### Release

Tag-driven via GoReleaser:

```bash
git tag v0.x.y
git push --tags        # CI builds binaries + UI, signs, publishes to GH Releases + Homebrew tap
```

---

## Getting started as a contributor

### Your first 30 minutes

```bash
git clone https://github.com/hadihonarvar/flock
cd flock
make dev               # installs deps, runs tests, starts a local cluster
make ui                # runs the Next.js UI in watch mode
```

`make dev` brings up a single-node Flock against a local Ollama and points your browser at the UI. From here, edit Go code → save → it hot-reloads.

### Finding your way around

Start with these files in order:

1. `cmd/flock/main.go` — the entrypoint; routes subcommands
2. `internal/controlplane/server.go` — the leader's HTTP server
3. `internal/agent/agent.go` — the per-node loop
4. `internal/api/openai_adapter.go` — translates OpenAI requests
5. `internal/api/anthropic_adapter.go` — translates Anthropic requests
6. `internal/scheduler/scheduler.go` — model placement decisions
7. `internal/engines/vllm.go` — example engine driver

Each file has a comment block at the top explaining what it owns. No file exceeds 600 lines; if you add to one and it crosses 800, split it.

### Common contributor tasks

| Task | Touch these files |
|---|---|
| Add a new inference engine | `internal/engines/<name>.go`, register in `internal/engines/registry.go` |
| Add a new model to the catalog | `catalog/<id>.yaml` |
| Add a new API surface (e.g. Cohere) | `internal/api/<name>_adapter.go` |
| Change scheduler policy | `internal/scheduler/policy_*.go` |
| Add a UI page | `web/src/pages/<name>.tsx` |
| Add a CLI command | `cmd/flock/cmd_<name>.go` |
| Add a metric | declare in `internal/metrics/metrics.go`, increment where relevant |

### Tests

```bash
make test            # unit
make test-integration  # spins up an in-process cluster
make test-e2e        # full cluster across goroutines, real Ollama
```

Every PR runs all three in CI.

### Submitting a PR

1. Open an issue first if the change is non-trivial
2. Branch: `feat/<short-name>` or `fix/<short-name>`
3. One change per PR
4. Update tests + docs (the same PR)
5. `make check` must pass (lint, test, build)
6. Two maintainer reviews to merge

### Communication

- **GitHub Discussions** — design questions, RFCs
- **GitHub Issues** — bugs, feature requests
- **Maintainer** — [Hadi Honarvar Nazari](https://www.linkedin.com/in/hadi-honarvar-nazari/) (`hadi.work.ca@gmail.com`)

---

## How to extend Flock

### Add a new inference engine

1. Read `internal/engines/ollama.go` as the simplest example.
2. Implement the `Engine` interface in `internal/engines/<name>.go`.
3. Register in `internal/engines/registry.go` — declare what hardware you support.
4. Add tests against a fake binary (don't require a real GPU in CI).
5. Document required system packages in `README.md → Installation`.

### Add a new client protocol

E.g. supporting Cohere's API:

1. Read `internal/api/openai_adapter.go` as the simplest example.
2. Create `internal/api/cohere_adapter.go` implementing translation in both directions.
3. Wire the routes in `internal/controlplane/routes.go`.
4. Document in `README.md → Supported clients`.

### Add a new mesh backend

E.g. supporting plain WireGuard without Tailscale:

1. Read `internal/mesh/tailscale.go` for the interface.
2. Create `internal/mesh/wireguard.go`.
3. Add a config option `mesh.backend: wireguard`.

### Add a new storage backend

1. Read `internal/store/sqlite.go`.
2. Create `internal/store/<name>.go` implementing the `Store` interface.
3. Add migration runner for that backend.
4. Add `storage.type` enum value.

### Add a new model to the catalog

Just add `catalog/<id>.yaml`. The catalog is loaded at startup; no code change needed.
