# Flock — Implementation Plan

Concrete task breakdown for the team building Flock. Each milestone ships a usable product; each task is sized to fit one PR (≤2 days for a single dev).

For user-facing docs see [README.md](README.md). For design rationale see [ARCHITECTURE.md](ARCHITECTURE.md).

## Current shipped state

**Verified build**: `go build ./cmd/flock` clean on go 1.25 / darwin-arm64. `go vet ./...` clean. `flock up` boots, prints admin key, gateway responds.

- **M0 — foundations**: ✅ done.
- **M1 — single-node MVP**: ✅ done. CLI, OpenAI API + streaming, Ollama driver, hardware detect, catalog, install.sh. Web UI shipped as a single embedded HTML page (Tailwind via CDN) rather than the Next.js scaffold originally planned.
- **M2 — multi-node**: ✅ **routing now ships**. `flock join` registers + starts a worker HTTP server. Heartbeat carries loaded models; leader reconciles placements. **Router** picks node per request (local-preferred, then least-loaded worker). Anthropic adapter live. vLLM + MLX + llama.cpp-RPC drivers ship. Tailscale `tsnet` mesh still deferred — LAN backend ships in v0.3.
- **M2.5 — sharding auto-orchestration (v0.4)**: ✅ `flock shard create <model> <N>` picks workers, launches `rpc-server` on each via the worker process-supervisor API, launches the coordinator `llama-server --rpc <list>` locally, persists shards + placement, Router routes to coordinator. Web UI "Shards" tab provides the same workflow. Failure rollback included.
- **M3 — multi-tenant + observability**: ✅ done. Per-user keys / scopes / daily quotas / audit log / usage metering / Prometheus metrics / hybrid fallback to Anthropic + OpenAI. OIDC deferred to v0.4. **Onboarding track (M3-T20 → M3-T26) shipped 2026-06-05** — `flock connect`, `flock invite`, dashboard Connect + Playground + Invite tabs, `/admin/v1/healthcheck` endpoint, all wired through `internal/control/` per the CLI-source-of-truth rule. New admin endpoints in `internal/controlplane/admin_{connect,invite,healthcheck}.go` are the reference implementation of the M4-T20 pattern (admin handler is decode + auth + delegate to `internal/control/`).
- **M2-T21 race fix**: ✅ shipped 2026-06-05. Router `getOrCreateRemote` TOCTOU window closed (held write lock through check-and-create); regression test under `-race` confirms exactly one engine cached per nodeID across 64 concurrent callers.
- **M4 — polish**: ✅ minimal embedded UI shipped. **Vision (M4-T03) shipped 2026-06-07** — image_url content blocks on `/v1/chat/completions` (Ollama path); `gemma4-12b`, `gemma4-26b`, `llama-4-scout`, `step-3.7-flash-sharded` carry the `vision` capability. **Embeddings (M4-T05) shipped 2026-06-07** — `POST /v1/embeddings` (Ollama path); `nomic-embed-text` is the default catalog entry. **Failure-based fallback chain shipped 2026-06-07** — catalog YAML `fallback: [next-id, …]`; router retries the chain on engine error / 5xx / timeout / model-not-loaded; transparent to clients; audit-logged. **Hardware-floor refusal shipped 2026-06-07** — `flock model add` checks `min_ram_gb` / `min_vram_gb`; `--force` overrides. **`flock disconnect <client>` shipped 2026-06-05** as the reversal counterpart to `flock connect`. LoRA / Whisper / live migration still deferred to v0.5+.
- **Memory lifecycle (planning/27) shipped 2026-06-11** — installed ≠ resident: admission control against live Ollama residency (`/api/ps`) so a node is never overcommitted; `flock model load [--swap] [--pin] [--priority N]` with LRU evict-and-drain (placement `draining` status unroutes via the existing `GetByModel` ready-filter; in-flight requests get `placement.drain_timeout_seconds`); desired placements (new SQLite table) restored on `flock up`; `flock model ps` + `GET /admin/v1/memory` + `POST /admin/v1/models/{id}/load` (typed `needs_swap`/`blocked_by_pinned`/`impossible` errors); dashboard Engine-memory card; `flock down` unloads by default (`--no-unload` opts out); `flock up --exclusive` for one-model-per-machine. New packages: `internal/lifecycle`; engine optional interfaces `ResidentLister`/`Loader`. Worker-side enforcement + shard-create free-RAM awareness deferred (see planning/27 "Out of scope").
- **Release tooling**: ✅ CI workflow, GoReleaser config, Homebrew formula, install.sh.
- **Fixes**: ✅ 15 code-review findings addressed in commit `70ad076` (engine routing per backend, streaming goroutine leaks, audit log content, vLLM/MLX token accounting, Anthropic tool-block preservation, agent 401/404 handling, more).

### What's still open

These are the gaps between marketing copy and what the binary actually does today. Anything claimed on the website or README must either map to shipped code or appear here.

**Networking / cluster**

- **Tailscale `tsnet` mesh** — interface defined, LAN backend ships meanwhile. Plug a `tsnet` backend into `internal/mesh/` to support cross-network workers. (tracked as M5-T09 below)
- **NetBird mesh backend** — same shape as Tailscale, different overlay; deferred to v1.0. (tracked as M5-T10)
- ~~**Shard crash recovery**~~ — ✅ shipped 2026-06-07. Supervisor auto-restarts `rpc-server` (and the coordinator `llama-server`) up to 5 times with exponential backoff before declaring `crashloop`. See `internal/agent/supervisor.go` + `internal/scheduler/sharding.go`.
- ~~**Coordinator on a worker**~~ — ✅ shipped 2026-06-07. `internal/scheduler/sharding.go` picks the highest-RAM host (default: strongest worker, single-machine falls back to leader). Override via `FLOCK_COORDINATOR_NODE`. Remote coordinator launches via the same `/v1/process/start` path as `rpc-server`.
- **Auto-rebalancing sharding** — shard count is currently picked by the admin (`flock shard create <model> <N>`). v1.0 should pick `N` automatically from worker count, model size, and free VRAM. (tracked as M5-T11)
- ~~**Automatic GGUF distribution**~~ — ✅ M5-T12 **shipped end-to-end**. For `source.type=file` and `source.type=huggingface` entries, `CreateSharded` first resolves the local GGUF (downloading from HuggingFace to `storage.models_dir/<filename>` when needed), then fans it out to every shard host via `/v1/process/file` HEAD + `/v1/process/upload` POST (sha256-verified). No more manual `wget` to leader or `scp` to workers.
- ~~**`rpc-server` binary bundling**~~ — ✅ shipped 2026-06-07 (M4-T14, brew route). `installer/homebrew/flock.rb` declares `depends_on "llama.cpp" => :recommended` so a brew install picks up both `rpc-server` and `llama-server` automatically. `flock doctor` warns when either binary is missing on the PATH so operators discover the gap before `flock shard create` fails. apt/yum users still need a one-line install of llama.cpp from upstream — documented in the doctor output.
- ~~**Catalog smoke-test CI**~~ — ✅ shipped 2026-06-07 (M4-T15). Two layers: per-PR parse + filename-matches-id (in the existing drift test), and a daily upstream HEAD probe (`.github/workflows/catalog-live.yml` runs `CATALOG_LIVE_CHECK=1 go test -run TestCatalogSourcesReachable ./cmd/flock/`).

**API surface**

- **Anthropic extended thinking** — `/v1/messages` accepts text + tool_use blocks; `thinking` blocks not yet supported. (tracked as M4-T12)
- **Anthropic computer use** — `computer_20241022` / `bash_20241022` / `text_editor_20241022` tool types not yet handled. (tracked as M4-T13)
- **Vision on Anthropic adapter** — image content blocks on `/v1/messages` not yet wired (OpenAI shape works via the Ollama path; Anthropic shape pending).
- **Whisper transcription** — `/v1/audio/transcriptions` not shipped. (tracked as M4-T04)
- **Rerank** — `/v1/rerank` deferred to v0.6 (needs cross-encoder backend; see ROADMAP).

**Security / auth**

- **OIDC** for the web UI — currently the UI takes a pasted admin key. Explicitly killed in [ROADMAP](ROADMAP.md#explicitly-killed-or-sibling-projected-scope) — out of scope for OSS.
- ~~**Worker token security**~~ — ✅ shipped 2026-06-07. HMAC-SHA256 over (v1, method, path, ts) keyed by the per-node token. Token now stays in the DB; only signatures travel. 5-min replay window. Bearer fallback retained for one transition release (disable with `FLOCK_REJECT_BEARER=1` on workers). See `internal/auth/hmac.go`.

**Operations / hardware**

- **LoRA, live model migration** — both v0.5. (M4-T02, M4-T07)
- **Postgres backend** for HA control plane — v1.0.
- **AMD ROCm engine path** — v1.0.

---

## Table of contents

- [How to use this document](#how-to-use-this-document)
- [Task metadata](#task-metadata)
- [Milestone 0 — Foundations (Week 0)](#milestone-0--foundations-week-0)
- [Milestone 1 — Single-node MVP (Weeks 1–4)](#milestone-1--single-node-mvp-weeks-14)
- [Milestone 2 — Multi-node cluster (Weeks 5–8)](#milestone-2--multi-node-cluster-weeks-58)
- [Milestone 3 — Multi-tenant + observability (Weeks 9–12)](#milestone-3--multi-tenant--observability-weeks-912)
- [Milestone 4 — Polish + public beta (Weeks 13–16)](#milestone-4--polish--public-beta-weeks-1316)
- [Milestone 5 — v1.0 production](#milestone-5--v10-production)
- [Parallelization map](#parallelization-map)
- [Definition of done](#definition-of-done)

---

## How to use this document

- Each task has an ID like `M1-T07`. Reference these in PR titles and issue threads.
- Mark tasks complete by changing `[ ]` to `[x]` in PRs that finish them.
- If a task turns out to need more than one PR, split it and add `M1-T07a`, `M1-T07b`. Don't keep working in one PR.
- If a task is bigger than 2 days, talk to a maintainer to split it before starting.
- Dependencies are listed under "Depends on". Don't start a task whose deps are open unless you're sure the interfaces won't change.

---

## Task metadata

Each task entry has:

- **Owner** — suggested role (BE = backend, UI = frontend, DevOps, Docs)
- **Effort** — S (≤0.5d), M (0.5–2d), L (3–5d, split if possible)
- **Depends on** — task IDs that must be complete first
- **Files** — where the work primarily lives
- **Acceptance** — one or two concrete criteria the task is done

---

## Milestone 0 — Foundations (Week 0)

Goal: bootstrap repo + CI so any subsequent task can land cleanly.

### M0-T01 — Initialize Go module and repo structure

- Owner: BE · Effort: S · Depends on: —
- Files: `go.mod`, top-level dirs, `.gitignore`, `LICENSE` (Apache 2.0), `CODE_OF_CONDUCT.md`, `SECURITY.md`
- Acceptance: `go build ./...` succeeds on an empty stub `main.go`. Repo has all canonical OSS files.

### M0-T02 — Makefile with `dev`, `build`, `test`, `lint`, `check`

- Owner: DevOps · Effort: S · Depends on: M0-T01
- Files: `Makefile`
- Acceptance: `make check` runs lint + test + build. Each target prints what it's about to do.

### M0-T03 — GitHub Actions CI

- Owner: DevOps · Effort: S · Depends on: M0-T02
- Files: `.github/workflows/ci.yml`
- Acceptance: CI runs on push and PR; matrix: macos-14, ubuntu-22.04, ubuntu-24.04. Caches Go modules + npm.

### M0-T04 — Lint setup

- Owner: BE · Effort: S · Depends on: M0-T01
- Files: `.golangci.yml`
- Acceptance: golangci-lint runs clean on stub; includes `errcheck`, `govet`, `staticcheck`, `revive`.

### M0-T05 — Initialize web/ workspace

- Owner: UI · Effort: S · Depends on: M0-T01
- Files: `web/package.json`, Next.js scaffold, Tailwind, shadcn/ui setup
- Acceptance: `cd web && npm run build` produces `web/dist/`.

### M0-T06 — `//go:embed` UI bundle

- Owner: BE · Effort: S · Depends on: M0-T05
- Files: `internal/ui/embed.go`
- Acceptance: A Go test loads index.html from the embedded fs.

### M0-T07 — README, ARCHITECTURE, TASKS scaffolding

- Owner: Docs · Effort: S · Depends on: —
- Files: `README.md`, `ARCHITECTURE.md`, `TASKS.md` (this), `CONTRIBUTING.md` (pointer)
- Acceptance: All docs render cleanly on GitHub; cross-links work.

---

## Milestone 1 — Single-node MVP (Weeks 1–4)

Goal: a junior can `curl | sh`, `flock up`, and curl an OpenAI-compatible chat response from a local Ollama. No cluster yet. Demo to manager at the end of M1.

### M1-T01 — `flock` binary skeleton + subcommand routing

- Owner: BE · Effort: M · Depends on: M0-T01
- Files: `cmd/flock/main.go`, `cmd/flock/cmd_*.go`
- Acceptance: `flock version`, `flock --help` work. Unknown subcommands return a helpful error.

### M1-T02 — Config loader (YAML + env)

- Owner: BE · Effort: M · Depends on: M1-T01
- Files: `internal/config/config.go`, `internal/config/config_test.go`
- Acceptance: Loads `~/.flock/config.yaml`; env vars `FLOCK_*` override; defaults fill in missing values; unit tests cover precedence.

### M1-T03 — SQLite store (open, migrate, schema)

- Owner: BE · Effort: M · Depends on: M1-T01
- Files: `internal/store/sqlite.go`, `internal/store/schema.sql`, `internal/store/migrations/`
- Acceptance: `Store{}` interface defined; SQLite impl opens at `data_dir/state.db`; schema migrates idempotently; integration test creates DB and queries.

### M1-T04 — Logging + slog setup

- Owner: BE · Effort: S · Depends on: M1-T01
- Files: `internal/logging/log.go`
- Acceptance: Global `slog` handler emits JSON; respects `log_level` config; request IDs flow through context.

### M1-T05 — HTTP server scaffold

- Owner: BE · Effort: M · Depends on: M1-T02, M1-T04
- Files: `internal/controlplane/server.go`, `internal/controlplane/routes.go`
- Acceptance: chi router on configurable port; `/healthz` returns 200; graceful shutdown on SIGINT; logged structured access logs.

### M1-T06 — API key auth middleware

- Owner: BE · Effort: M · Depends on: M1-T03, M1-T05
- Files: `internal/auth/api_keys.go`, `internal/auth/middleware.go`
- Acceptance: Bearer-token middleware validates against store; returns 401 with correct shape; admin scope bypasses user-only routes.

### M1-T07 — Initial admin key bootstrap

- Owner: BE · Effort: S · Depends on: M1-T06
- Files: `internal/auth/bootstrap.go`
- Acceptance: On first `flock up`, generates `sk-orc-…`, stores hash in DB, prints plain key to stdout once.

### M1-T08 — Ollama engine driver

- Owner: BE · Effort: M · Depends on: M1-T04
- Files: `internal/engines/ollama.go`
- Acceptance: Detects if Ollama is installed; can start it if not running; can pull a model; reports its endpoint; health checks pass; basic unit tests with a fake Ollama HTTP server.

### M1-T09 — Engine registry

- Owner: BE · Effort: S · Depends on: M1-T08
- Files: `internal/engines/registry.go`, `internal/engines/types.go`
- Acceptance: `Engine` interface defined; Ollama registered; registry lookup by name returns the right driver.

### M1-T10 — Hardware detection (Mac + Linux)

- Owner: BE · Effort: M · Depends on: M1-T04
- Files: `internal/agent/capability.go`, `_darwin.go`, `_linux.go`
- Acceptance: Returns `Capabilities{RAM, CPU, GPUs, OS, Engines}`. Tested on M-chip Mac and a Linux box (CI runs Linux test; manual on Mac).

### M1-T11 — Auto-model selection logic

- Owner: BE · Effort: S · Depends on: M1-T10
- Files: `internal/models/auto_pick.go`
- Acceptance: Given a `Capabilities{}`, picks the largest model that fits with 4GB headroom from a hard-coded short list (Qwen2.5-Coder-7B for <32GB, 14B for <48GB, 32B for >=48GB).

### M1-T12 — Catalog YAML format + seed entries

- Owner: BE · Effort: M · Depends on: M1-T03
- Files: `catalog/*.yaml`, `internal/models/catalog.go`
- Acceptance: Schema documented; parser loads all entries at startup; first 5 entries (Qwen-Coder 7B/14B/32B, Llama 3.2 3B, BGE-M3) load cleanly.

### M1-T13 — `/v1/models` endpoint (OpenAI)

- Owner: BE · Effort: S · Depends on: M1-T05, M1-T12
- Files: `internal/api/openai_models.go`
- Acceptance: Returns OpenAI-shaped `data: [{id, object, ...}]` for installed models.

### M1-T14 — `/v1/chat/completions` adapter (non-streaming)

- Owner: BE · Effort: M · Depends on: M1-T08, M1-T09
- Files: `internal/api/openai_adapter.go`, `internal/api/openai_adapter_test.go`
- Acceptance: Posts to Ollama, returns OpenAI-shaped response. Integration test against a fake engine.

### M1-T15 — SSE streaming for `/v1/chat/completions`

- Owner: BE · Effort: M · Depends on: M1-T14
- Files: `internal/api/openai_stream.go`
- Acceptance: `stream: true` returns proper SSE chunks ending with `data: [DONE]`. Tested with curl.

### M1-T16 — `flock up` command

- Owner: BE · Effort: M · Depends on: M1-T05, M1-T07, M1-T11
- Files: `cmd/flock/cmd_up.go`
- Acceptance: Starts server; detects hardware; picks default model; pulls if missing; prints next-action block (web URL, API URL, key, curl example).

### M1-T17 — `flock down` command

- Owner: BE · Effort: S · Depends on: M1-T16
- Files: `cmd/flock/cmd_down.go`
- Acceptance: Sends SIGTERM to running flock process (via PID file); waits for graceful shutdown.

### M1-T18 — `flock status` command

- Owner: BE · Effort: S · Depends on: M1-T16
- Files: `cmd/flock/cmd_status.go`
- Acceptance: Hits leader's `/healthz` + `/admin/v1/nodes`; prints cluster table.

### M1-T19 — `flock model add/ls/remove`

- Owner: BE · Effort: M · Depends on: M1-T08, M1-T12
- Files: `cmd/flock/cmd_model.go`
- Acceptance: `flock model add qwen-coder-7b` triggers Ollama pull; `flock model ls` shows table; `flock model remove` unloads.

### M1-T20 — `flock token create/ls/revoke`

- Owner: BE · Effort: S · Depends on: M1-T06
- Files: `cmd/flock/cmd_token.go`
- Acceptance: Issues new API keys; lists; revokes.

### M1-T21 — `flock doctor`

- Owner: BE · Effort: M · Depends on: M1-T10
- Files: `cmd/flock/cmd_doctor.go`
- Acceptance: Checks port availability, Ollama installation, disk space, RAM; prints actionable fixes for each failure.

### M1-T22 — Web UI: dashboard page

- Owner: UI · Effort: M · Depends on: M0-T05, M1-T05
- Files: `web/src/app/page.tsx`, `web/src/components/StatusCards.tsx`
- Acceptance: Shows: cluster up indicator, current model, requests today, link to API.

### M1-T23 — Web UI: models page

- Owner: UI · Effort: M · Depends on: M1-T22, M1-T19
- Files: `web/src/app/models/page.tsx`
- Acceptance: Lists installed models; "Add model" button opens a modal with catalog browse + add.

### M1-T24 — Web UI: API key + connection snippets

- Owner: UI · Effort: M · Depends on: M1-T22, M1-T20
- Files: `web/src/app/settings/page.tsx`, `web/src/components/ConnectSnippet.tsx`
- Acceptance: Shows admin key; per-tool tabs (Cursor / Claude Code / Continue / curl) with copy-paste snippets containing real URL + key.

### M1-T25 — install.sh script

- Owner: DevOps · Effort: M · Depends on: M0-T03, M1-T16
- Files: `installer/install.sh`
- Acceptance: Detects OS+arch; downloads matching binary from GH Releases; installs to `/usr/local/bin`; idempotent.

### M1-T26 — GoReleaser config + release flow

- Owner: DevOps · Effort: M · Depends on: M0-T03
- Files: `.goreleaser.yaml`, `.github/workflows/release.yml`
- Acceptance: Tag push builds binaries for darwin-arm64, linux-amd64, linux-arm64; UI is built and embedded; release notes auto-generated.

### M1-T27 — Homebrew tap formula

- Owner: DevOps · Effort: S · Depends on: M1-T26
- Files: `installer/homebrew/flock.rb`
- Acceptance: `brew install hadihonarvar/tap/flock` works after release.

### M1-T28 — launchd + systemd unit files

- Owner: DevOps · Effort: S · Depends on: M1-T25
- Files: `deploy/launchd/dev.flock.plist`, `deploy/systemd/flock.service`
- Acceptance: install.sh registers Flock to start at boot.

### M1-T29 — End-to-end smoke test

- Owner: BE · Effort: M · Depends on: M1-T14, M1-T15, M1-T16
- Files: `test/e2e/smoke_test.go`
- Acceptance: CI test spins up Flock + a stub Ollama, posts a chat request, asserts response shape + streaming.

### M1-T30 — M1 demo recording

- Owner: Docs · Effort: S · Depends on: M1-T16, M1-T24, M1-T29
- Files: `docs/demo/m1.cast`, `README.md` (link in)
- Acceptance: 60-second asciinema recording showing install → up → curl → streamed response; embedded in README.

---

## Milestone 2 — Multi-node cluster (Weeks 5–8)

Goal: real cluster. `flock join` works across two machines. Anthropic API surface so Claude Code connects to local Qwen.

### M2-T01 — Embed NATS broker

- Owner: BE · Effort: M · Depends on: M1-T05
- Files: `internal/messaging/nats.go`
- Acceptance: Leader starts an embedded NATS server; workers connect; pub/sub topics tested.

### M2-T02 — tsnet integration

- Owner: BE · Effort: L · Depends on: M1-T05
- Files: `internal/mesh/tailscale.go`, `internal/mesh/types.go`
- Acceptance: Leader auto-creates a tailnet (or reuses configured one); persists auth state; exposes `net.Listener` and `Dial`. Tested with two processes on one machine via tsnet's loopback.

### M2-T03 — Node token issuance

- Owner: BE · Effort: M · Depends on: M2-T02, M1-T20
- Files: `internal/auth/node_tokens.go`
- Acceptance: `flock token create --type=node` produces a single-use JWT including tailnet auth key + leader URL; expires in 5min.

### M2-T04 — `flock join` command

- Owner: BE · Effort: M · Depends on: M2-T02, M2-T03
- Files: `cmd/flock/cmd_join.go`, `internal/agent/join.go`
- Acceptance: Parses URL+token; joins tailnet; dials leader; registers capabilities; writes node state to local `~/.flock/node.yaml`.

### M2-T05 — Agent loop (heartbeat + assignment subscriber)

- Owner: BE · Effort: L · Depends on: M2-T01, M2-T04
- Files: `internal/agent/agent.go`, `internal/agent/heartbeat.go`, `internal/agent/assignment.go`
- Acceptance: Heartbeats every 5s; subscribes to `assignment.<node-id>`; reacts to load/unload/drain messages; integration test with in-process NATS.

### M2-T06 — Node registry on leader

- Owner: BE · Effort: M · Depends on: M2-T05
- Files: `internal/controlplane/node_registry.go`
- Acceptance: Tracks all nodes; marks stale after 3 missed heartbeats; updates SQLite + in-memory cache.

### M2-T07 — vLLM engine driver

- Owner: BE · Effort: L · Depends on: M1-T09
- Files: `internal/engines/vllm.go`, `internal/engines/vllm_test.go`
- Acceptance: Launches vLLM via Docker (`docker run --gpus all …`) or bare; passes correct flags for the assigned model; health-checks endpoint; tested on a real GPU box (manual gate).

### M2-T08 — MLX-LM engine driver

- Owner: BE · Effort: M · Depends on: M1-T09
- Files: `internal/engines/mlx.go`
- Acceptance: Launches `mlx_lm.server` subprocess; passes model path; health checks; tested on M-chip Mac.

### M2-T09 — HuggingFace model puller (resume + verify)

- Owner: BE · Effort: L · Depends on: M2-T05
- Files: `internal/models/puller.go`, `internal/models/puller_test.go`
- Acceptance: Downloads multi-file repos in parallel; resumes interrupted downloads; verifies SHA256; emits progress events.

### M2-T10 — Scheduler v1 (bin-packing)

- Owner: BE · Effort: L · Depends on: M2-T06, M2-T09
- Files: `internal/scheduler/scheduler.go`, `internal/scheduler/policy_spread.go`
- Acceptance: Given list of requested models + node registry, emits assignments via NATS; respects free RAM/VRAM; unit tests with mock registry.

### M2-T11 — Cross-node request routing

- Owner: BE · Effort: M · Depends on: M2-T02, M2-T06
- Files: `internal/router/router.go`
- Acceptance: Gateway opens an HTTP connection to the worker's local engine over tailnet; streams response back; falls back to "model not available" on no candidate.

### M2-T12 — Anthropic `/v1/messages` adapter

- Owner: BE · Effort: L · Depends on: M1-T14, M1-T15
- Files: `internal/api/anthropic_adapter.go`, `internal/api/anthropic_stream.go`, `internal/api/anthropic_adapter_test.go`
- Acceptance: Translates Anthropic Messages format to internal; emits Anthropic-format SSE events (`message_start`, `content_block_*`, `message_delta`, `message_stop`); tested against real Anthropic SDK and against a recorded Claude request fixture.

### M2-T13 — Anthropic tool-call translation

- Owner: BE · Effort: M · Depends on: M2-T12
- Files: `internal/api/anthropic_tools.go`
- Acceptance: Bidirectional translation between Anthropic `tool_use` / `tool_result` content blocks and internal tool-call format; tested with Qwen3-Coder tool-call output.

### M2-T14 — Anthropic `/v1/messages/count_tokens` endpoint

- Owner: BE · Effort: S · Depends on: M2-T12
- Files: `internal/api/anthropic_count.go`
- Acceptance: Returns `input_tokens` count; uses tiktoken/tokenizer matching the routed model.

### M2-T15 — Claude Code end-to-end verification

- Owner: BE · Effort: M · Depends on: M2-T12, M2-T13, M2-T11
- Files: `test/e2e/claude_code_test.go`
- Acceptance: Test sets `ANTHROPIC_BASE_URL` to local Flock, invokes Claude Code CLI in a sample repo, asserts it calls Read + Edit tools successfully.

### M2-T16 — Web UI: nodes page

- Owner: UI · Effort: M · Depends on: M2-T06, M1-T22
- Files: `web/src/app/nodes/page.tsx`
- Acceptance: Lists nodes with hardware, models, status, recent requests.

### M2-T17 — Web UI: add-node wizard

- Owner: UI · Effort: M · Depends on: M2-T03, M2-T16
- Files: `web/src/app/nodes/add/page.tsx`
- Acceptance: Generates a node token; shows the `curl | sh -s -- join …` command with QR code.

### M2-T18 — `flock node ls / show / drain / remove`

- Owner: BE · Effort: M · Depends on: M2-T06
- Files: `cmd/flock/cmd_node.go`
- Acceptance: All four subcommands work against a live cluster.

### M2-T19 — Heterogeneous worker test: Mac + Linux

- Owner: BE · Effort: M · Depends on: M2-T07, M2-T08, M2-T11
- Files: `test/integration/heterogeneous_test.go`
- Acceptance: Manual test docs + scripted integration test (with mock engines) showing a Mac + Linux pair serving two models behind one gateway.

### M2-T20 — M2 demo recording

- Owner: Docs · Effort: S · Depends on: M2-T15, M2-T17
- Files: `docs/demo/m2.cast`
- Acceptance: 90-second recording: install on machine 1 → install + join on machine 2 → both nodes show in UI → Claude Code calls local Qwen3-Coder via Flock.

### M2-T21 — Fix router `getOrCreateRemote` race condition

- Owner: BE · Effort: S (½d) · Depends on: —
- Files: `internal/router/router.go` (around lines 218–236)
- Acceptance: TOCTOU window in `getOrCreateRemote()` closed — currently RLock is released between cache-miss check and engine construction, so two concurrent requests for the same node can each construct their own remote engine and one is dropped on the floor. Fix with either (a) hold the write Lock throughout construction, or (b) use a `sync.Map` with `LoadOrStore`, or (c) a per-nodeID `sync.Once`. Add a unit test that spawns N goroutines hitting the same nodeID and asserts `engines.NewWithAuth` is called exactly once. Found by code review on 2026-06-05.

---

## Milestone 3 — Multi-tenant + observability (Weeks 9–12)

Goal: ready for an actual team of 10. Per-user keys, quotas, OIDC, full observability stack, hybrid fallback.

### M3-T01 — Multi-user API keys + scopes

- Owner: BE · Effort: M · Depends on: M1-T06
- Files: `internal/auth/user_keys.go`
- Acceptance: Keys tied to user_id; scopes enforced per route; revocation immediate (no cache TTL).

### M3-T02 — Per-key quotas

- Owner: BE · Effort: M · Depends on: M3-T01
- Files: `internal/auth/quotas.go`, `internal/auth/quotas_test.go`
- Acceptance: Daily + monthly token caps; rate-limit response shape matches OpenAI/Anthropic standards; reset job at UTC midnight.

### M3-T03 — OIDC integration

- Owner: BE · Effort: L · Depends on: M3-T01
- Files: `internal/auth/oidc.go`, `internal/auth/session.go`
- Acceptance: Generic OIDC works; Google preset works end-to-end; first user becomes admin; subsequent invited.

### M3-T04 — Audit log

- Owner: BE · Effort: M · Depends on: M3-T01
- Files: `internal/auth/audit.go`
- Acceptance: Every admin action + every API request recorded with user, action, target, metadata; queryable via admin API.

### M3-T05 — Usage metering

- Owner: BE · Effort: M · Depends on: M3-T01
- Files: `internal/metrics/usage.go`, `internal/controlplane/usage_api.go`
- Acceptance: Records `prompt_tokens`, `completion_tokens`, model, latency per request; admin API `/admin/v1/usage` returns rollups by user/model/day.

### M3-T06 — Cost-equivalent calculation

- Owner: BE · Effort: S · Depends on: M3-T05
- Files: `internal/metrics/cost.go`, `catalog/pricing.yaml`
- Acceptance: For each request, compute "what this would have cost at OpenAI/Anthropic public pricing"; show monthly "saved $X" on dashboard.

### M3-T07 — Sticky-session router

- Owner: BE · Effort: M · Depends on: M2-T11
- Files: `internal/router/sticky.go`
- Acceptance: Sessions identified by user_id + first-message hash (or `X-Session-Id`); bound to a node for TTL; soft-rebalances under load.

### M3-T08 — Anthropic egress adapter (fallback)

- Owner: BE · Effort: M · Depends on: M2-T11
- Files: `internal/api/egress_anthropic.go`
- Acceptance: When model name matches Anthropic IDs, proxies to api.anthropic.com using configured key; logs usage; respects per-user quotas.

### M3-T09 — OpenAI egress adapter (fallback)

- Owner: BE · Effort: M · Depends on: M2-T11
- Files: `internal/api/egress_openai.go`
- Acceptance: Same as M3-T08 for OpenAI.

### M3-T10 — Fallback policy engine

- Owner: BE · Effort: M · Depends on: M3-T08, M3-T09
- Files: `internal/router/fallback.go`
- Acceptance: Config-driven rules: "if local overloaded → vendor X", "if user opts in → vendor Y"; unit tests.

### M3-T11 — Prometheus metrics endpoint

- Owner: BE · Effort: M · Depends on: M2-T11
- Files: `internal/metrics/metrics.go`, `internal/controlplane/metrics_route.go`
- Acceptance: `/metrics` exposes all series listed in ARCHITECTURE.md → Observability; scrape from a real Prometheus passes.

### M3-T12 — Grafana dashboards

- Owner: DevOps · Effort: M · Depends on: M3-T11
- Files: `dashboards/cluster-overview.json`, `dashboards/per-model.json`, `dashboards/per-user.json`
- Acceptance: Imports cleanly into Grafana 11+; all panels populate when CI generates fake metrics.

### M3-T13 — OpenTelemetry tracing

- Owner: BE · Effort: M · Depends on: M2-T11
- Files: `internal/tracing/tracer.go`
- Acceptance: Spans for gateway.request → router.decide → worker.inference; OTLP exporter configurable; tested against Tempo locally.

### M3-T14 — Web UI: usage page

- Owner: UI · Effort: M · Depends on: M3-T05, M3-T06
- Files: `web/src/app/usage/page.tsx`
- Acceptance: Charts: requests/day, tokens/day, top users, top models, "saved vs API".

### M3-T15 — Web UI: users page + invite flow

- Owner: UI · Effort: M · Depends on: M3-T03
- Files: `web/src/app/users/page.tsx`, `web/src/app/users/invite/page.tsx`
- Acceptance: Admin can list users, invite via OIDC, revoke keys, set quotas.

### M3-T16 — Web UI: settings (OIDC + fallback providers)

- Owner: UI · Effort: M · Depends on: M3-T03, M3-T10
- Files: `web/src/app/settings/oidc/page.tsx`, `web/src/app/settings/fallback/page.tsx`
- Acceptance: Configure OIDC, set vendor API keys, define fallback policy.

### M3-T17 — Web UI: logs live tail

- Owner: UI · Effort: M · Depends on: M3-T13
- Files: `web/src/app/logs/page.tsx`
- Acceptance: Server-sent stream of recent requests with user/model/latency; filterable.

### M3-T18 — Public beta announcement materials

- Owner: Docs · Effort: M · Depends on: M3-T14, M3-T15, M2-T20
- Files: `docs/launch/`, README updates, blog post draft
- Acceptance: 30-second hero GIF; HN post draft; reddit post draft; X thread.

### M3-T19 — Security review of multi-tenant code

- Owner: BE · Effort: M · Depends on: M3-T01, M3-T03, M3-T04
- Files: review notes in `docs/security/m3-review.md`
- Acceptance: Checklist of OWASP top 10 against the gateway; admin-vs-user privilege boundaries audited.

---

### Onboarding-and-sharing track (M3-T20 → M3-T26) — ✅ shipped 2026-06-05

The whole point of Flock is your team's existing AI tools (Claude Code, Cursor, Aider, …) working against your hardware. If wiring those up isn't trivially easy, nothing else matters. These seven tasks turn the post-install experience from "go read README on GitHub" into "the dashboard shows you exactly what to paste, the CLI prints exact env vars, and inviting a teammate is one command."

Implementation rule (per Definition of Done): each task ships as a CLI command first; web UI invokes the same Go function the CLI invokes.

**Status:** all seven tasks shipped 2026-06-05. CLI: `flock connect <client>`, `flock invite <name>`, updated `flock up` banner. Dashboard: Connect tab, Playground tab, Invite-from-UI modal in Tokens tab. All UI actions invoke `internal/control/` functions — the same Go code the CLI uses — via three new admin endpoints (`/admin/v1/connect/clients`, `/admin/v1/connect/snippet`, `/admin/v1/invite`, `/admin/v1/healthcheck`).

### M3-T20 — `flock connect <client>` CLI

- Owner: BE · Effort: S (1d) · Depends on: M3-T01
- Files: `cmd/flock/cmd_connect.go` (new), `internal/control/connect.go` (new), `internal/connect/snippets/` (new — one Go template per client)
- Acceptance: `flock connect <client>` prints exact, ready-to-paste configuration for the named client, with the user's base URL and token already substituted. `flock connect --list` lists supported clients. Initial set (10): `claude-code`, `cursor`, `aider`, `continue`, `zed`, `cline`, `qwen-code`, `openai-sdk`, `anthropic-sdk`, `curl`. Per-client snippets live in `internal/connect/snippets/*.tmpl` so adding a new client is one file. Token defaults to "current admin token" but `--token <key>` overrides for scripting.

### M3-T21 — `flock invite <name>` + shareable config card

- Owner: BE · Effort: S (1d) · Depends on: M3-T20, M3-T01
- Files: `cmd/flock/cmd_invite.go` (new), `internal/control/invite.go` (new)
- Acceptance: `flock invite <name>` creates a user-scope token (default quota from config), prints a complete share card containing base URL, token, and `flock connect <client>` output for all 10 supported clients. `--quota <n>` and `--clients <list>` flags. Token is shown once. Logged in audit. Output is paste-into-Slack-friendly markdown by default; `--format json` for scripting.

### M3-T22 — `flock up` next-step banner

- Owner: BE · Effort: S (½d) · Depends on: M3-T20
- Files: `cmd/flock/cmd_up.go`
- Acceptance: The end-of-boot banner is replaced with an explicit next-steps list pointing to the Connect tab in the dashboard and to `flock connect <client>` for the three most-used clients (Claude Code, Cursor, curl). The banner detects whether an admin token was just created (first-run) vs. existing setup, and tunes wording accordingly. First-run also nudges `flock invite` for inviting teammates.

### M3-T23 — Dashboard "Connect" tab (wraps M3-T20)

- Owner: BE/UI · Effort: M (2d) · Depends on: M3-T20, M4-T20 (admin API wraps CLI)
- Files: `internal/ui/index.html`, `internal/api/admin_connect.go` (new)
- Acceptance: Top-level tab in the dashboard (between Tokens and Usage). Dropdown to pick a client; the pre-filled config block appears below with a Copy button and a one-line "what this does" caption. Behind the scenes the tab GETs an admin endpoint that invokes `internal/control/connect.go` — the exact same function `flock connect` calls. Token used in the snippet is the session token (admin) by default, with a sub-dropdown to swap in any user-scope token from the Tokens tab.

### M3-T24 — "Test connection" health-check + button

- Owner: BE/UI · Effort: S (½d) · Depends on: M3-T23
- Files: `internal/api/admin_healthcheck.go` (new), `internal/ui/index.html`
- Acceptance: New admin endpoint `/admin/v1/healthcheck` sends a 5-token chat completion through the gateway using the supplied token, and returns `{ok, latency_ms, model, engine, error?}`. The Connect tab's Test button calls this and shows ✅/❌ inline with the latency. Lets users prove the wiring works without leaving the dashboard.

### M3-T25 — Dashboard "Playground" tab (elevated from M4-T01)

- Owner: BE/UI · Effort: M (2d) · Depends on: M3-T23
- Files: `internal/ui/index.html`, `internal/api/admin_playground.go` (new)
- Acceptance: New tab. Model picker (populated from `/v1/models`), system-prompt textarea, user-message textarea, Send button, streaming response panel. Uses `/v1/chat/completions` via the user's session token. Lets a non-technical user verify "yes, the gateway and model work" in 10 seconds. **Supersedes M4-T01** — pulled forward because it's part of the headline onboarding flow.

### M3-T26 — Invite-from-UI flow (supersedes M3-T15)

- Owner: BE/UI · Effort: M (1–2d) · Depends on: M3-T21, M3-T23
- Files: `internal/ui/index.html`, `internal/api/admin_invite.go` (new)
- Acceptance: Tokens tab grows an "Invite teammate" button. Opens a small form (name, quota, optional email). Submitting calls the admin endpoint that wraps `internal/control/invite.go` (same code path as `flock invite`). Returns the full share card (token + per-client snippets) in-modal with a one-click Copy-as-markdown and a "Copy share URL" option. **Supersedes M3-T15** — that task referenced the Next.js scaffold (`web/src/app/users/...`) which was never built; the real embedded-HTML UI lives in `internal/ui/index.html`.

---

## Milestone 4 — Polish + public beta (Weeks 13–16)

Goal: launch publicly. Quality bar high enough to keep stars and adopt PRs.

### M4-T01 — Web UI: in-browser playground · **superseded by M3-T25**

- Owner: UI · Effort: M · Depends on: M2-T12
- Files: `web/src/app/playground/page.tsx`
- Acceptance: Chat with any installed model; tool calls visualized; copy-as-curl button.

### M4-T02 — LoRA adapter loading

- Owner: BE · Effort: L · Depends on: M2-T09
- Files: `internal/models/adapters.go`, `internal/engines/vllm.go` updates
- Acceptance: `flock model adapter add` works; `model=base+adapter` in request loads the adapter; tested with a small Qwen LoRA.

### M4-T03 — Vision support

- Owner: BE · Effort: M · Depends on: M2-T12
- Files: `internal/api/vision.go`
- Acceptance: Image input via OpenAI + Anthropic message formats; routed to vision-capable model; tested with Qwen2.5-VL.

### M4-T04 — Whisper transcription endpoint

- Owner: BE · Effort: M · Depends on: M2-T11
- Files: `internal/api/whisper.go`, `internal/engines/whisper.go`
- Acceptance: `/v1/audio/transcriptions` works; uses faster-whisper backend; tested with a sample wav.

### M4-T05 — Embeddings endpoint

- Owner: BE · Effort: M · Depends on: M2-T11
- Files: `internal/api/embeddings.go`
- Acceptance: `/v1/embeddings` batched; routed to embeddings pool; tested with BGE-M3.

### M4-T06 — Catalog expansion: top 30 models

- Owner: BE/Docs · Effort: M · Depends on: M1-T12
- Files: `catalog/*.yaml`
- Acceptance: All models listed in README → Supported models have working YAML entries; each tested manually for at least loading.

### M4-T07 — Live model migration

- Owner: BE · Effort: L · Depends on: M2-T10
- Files: `internal/scheduler/migration.go`
- Acceptance: Drain command keeps existing requests serving while new model boots elsewhere; integration test.

### M4-T08 — `flock doctor` deep checks

- Owner: BE · Effort: M · Depends on: M1-T21
- Files: `cmd/flock/cmd_doctor.go` updates
- Acceptance: Checks: tailscale reachability, model file integrity, engine subprocess health, disk space trends, OIDC config validity.

### M4-T09 — Docs site

- Owner: Docs · Effort: L · Depends on: —
- Files: `docs-site/` (Nextra or Docusaurus)
- Acceptance: Renders README + ARCHITECTURE + per-page guides; hosted at `flockllm.com/docs` (or via Cloudflare Pages on a subdomain).

### M4-T10 — Marketing site

- Owner: Docs · Effort: M · Depends on: M4-T09
- Files: `marketing/` (or merge into docs site)
- Acceptance: Landing page with hero, demo gif, "install" CTA, FAQ.

### M4-T11 — HN / Reddit / X launch

- Owner: Marketing/Docs · Effort: S · Depends on: M4-T09, M4-T10, M3-T18
- Acceptance: Posts queued and published in same hour; team monitors comments first 24 hours.

### M4-T12 — Anthropic extended thinking blocks

- Owner: BE · Effort: M · Depends on: M3-T16
- Files: `internal/api/anthropic.go`
- Acceptance: `/v1/messages` accepts and returns `thinking` blocks in request/response; engines that don't natively reason are gracefully no-op'd (the response just omits the block). Test: Claude Code's extended-thinking flow round-trips through Flock without errors.

### M4-T13 — Anthropic computer use tool blocks

- Owner: BE · Effort: M · Depends on: M4-T12
- Files: `internal/api/anthropic.go`
- Acceptance: `/v1/messages` parses `computer_20241022`, `bash_20241022`, `text_editor_20241022` tool definitions and passes them through to engines that support tool-calling; tool_result blocks of these shapes round-trip correctly. Local engines without computer-use capability return an explicit "not supported by this model" error rather than silently dropping the tool.

### M4-T14 — Bundle `rpc-server` binary in Flock release

- Owner: DevOps · Effort: S · Depends on: M2.5
- Files: `Makefile`, `.github/workflows/release.yml`, `installer/install.sh`
- Acceptance: Every Flock release tarball includes a prebuilt `rpc-server` for darwin/arm64, darwin/amd64, linux/arm64, linux/amd64. `flock doctor` finds the bundled binary instead of asking the user to `git clone llama.cpp && cmake`. `flock shard create` works on a fresh cluster with zero manual llama.cpp compilation. Why this matters: until this lands, "shard a 70B across two Macs" is a 30-minute setup, not a one-command UX.

### M4-T15 — Catalog smoke-test harness (CI)

- Owner: BE/DevOps · Effort: M · Depends on: M1-T12
- Files: `.github/workflows/catalog-smoke.yml`, `internal/catalog/smoke_test.go`
- Acceptance: A GitHub Actions job (self-hosted runner with at least 24 GB RAM) iterates every YAML in `catalog/`, boots Flock with `FLOCK_DEFAULT_MODEL=<id>`, sends one chat completion via `/v1/chat/completions` and one via `/v1/messages`, and fails the job if any model errors or returns empty. Runs on every PR that touches `catalog/` and nightly on `main`. Prevents catalog entries from drifting into "aspirational" claims like the pre-2026-06 README. Should land before **M4-T06** (catalog expansion).

---

### Easy-model-switching track (M4-T16 → M4-T20)

These five tasks together implement the **"adding or switching a model is one action"** product principle. Each step in today's "add a new model" flow that requires a human decision becomes a Flock decision with a CLI override flag. The web UI never reimplements logic — it invokes the CLI commands these tasks define.

### M4-T16 — Auto-YAML from HuggingFace model card

- Owner: BE · Effort: M (2d) · Depends on: M1-T12
- Files: `internal/catalog/hf_resolver.go`, `cmd/flock/cmd_model.go`
- Acceptance: `flock model add hf:<owner>/<repo>` (no other args) inspects the HF model card via the HF API (or local cache), reads `architectures`, `parameters`, file list (GGUF/AWQ/safetensors variants), and writes a working catalog entry to `~/.flock/catalog/<id>.yaml` automatically. Fields populated: `id`, `display_name`, `source`, `size_bytes`, `quant`, `context_window`, `capabilities` (from `pipeline_tag`), `recommended_engines` (from architecture rules), `hardware.min_ram_gb` (from params × bytes-per-param-at-quant). The generated YAML is identical in shape to a hand-written one — `flock model add <id>` works against it the next time. Override flags `--engine`, `--quant`, `--id` respected.

### M4-T17 — Hardware-aware engine + quant selection

- Owner: BE · Effort: M (1–2d) · Depends on: M4-T16, M1-T09 (hardware detect)
- Files: `internal/catalog/picker.go`, `internal/hwdetect/`
- Acceptance: Given a model size in params and the current node's detected hardware (CPU vendor, GPU vendor, total VRAM, total RAM), Flock picks an engine and quant according to a documented rule table:
  - Apple Silicon + model fits in unified memory → MLX-LM (Q4 default, Q5/Q8 if RAM allows)
  - NVIDIA + ≥16 GB VRAM → vLLM (AWQ if available, else Q4_K_M GGUF)
  - Anything else / fallback → Ollama (Q4_K_M)
  - Model size > one node's RAM → llama.cpp-RPC with sharding (delegates to M5-T11)
  The picker's choice is logged at INFO; user can override with `--engine` and `--quant`. Picker also rejects impossible combos (e.g. MLX on Linux) with a clear error.

### M4-T18 — `flock default <id>` with pre-warming

- Owner: BE · Effort: S (1d) · Depends on: M2-T08 (placements)
- Files: `cmd/flock/cmd_default.go` (new), `internal/scheduler/warmup.go`
- Acceptance: New CLI command `flock default <id>` replaces the current "edit config + restart" flow. It (1) pre-loads the target model on the best-fit worker, (2) waits for first inference to confirm the model is hot, (3) atomically updates the default-model pointer, (4) returns. `flock default` (no arg) prints the current default. Zero downtime: requests for the old default keep working until the new one is hot. Logged in audit.

### M4-T19 — Web UI: Add Model search + progress (invokes CLI)

- Owner: BE/UI · Effort: M (2d) · Depends on: M4-T16, M4-T17, M4-T20
- Files: `internal/ui/index.html`, `internal/api/admin_models.go`
- Acceptance: The Models tab gets an **Add** button that opens a search box with HF model-card autocomplete (typeahead via the public HF API, no API key). Selecting a result POSTs to an admin endpoint that internally invokes the same code path as `flock model add hf:<repo>` (no reimplementation — see M4-T20). Progress bar shows GGUF download bytes + per-worker distribution. ETA visible. Cancel button. On completion the new model appears in the catalog list with status `ready`.

### M4-T20 — Refactor: admin API wraps CLI command paths

- Owner: BE · Effort: M (1–2d) · Depends on: —
- Files: `internal/api/admin_*.go`, `internal/control/cli.go` (new shared package)
- Acceptance: Every admin HTTP endpoint that mutates state (add model, remove model, set default, create shard, drain node, create/revoke token) is refactored to invoke the same exported Go function the CLI command in `cmd/flock/` uses. New shared package `internal/control/` holds these functions. CLI code in `cmd/flock/` becomes a thin arg-parsing layer. **No mutating logic lives in `internal/api/` after this lands.** Unit tests confirm: for each affected action, calling the CLI and calling the HTTP endpoint produce identical store state. Documents the rule in `ARCHITECTURE.md`.

---

## Milestone 5 — v1.0 production

Goal: production-grade for orgs running this in serious environments.

### M5-T01 — Postgres storage backend

- Owner: BE · Effort: L · Depends on: M1-T03
- Files: `internal/store/postgres.go`
- Acceptance: All `Store` interface methods implemented; migration parity with SQLite; integration test against real Postgres.

### M5-T02 — HA leader (consensus)

- Owner: BE · Effort: L · Depends on: M5-T01
- Files: `internal/controlplane/ha.go`
- Acceptance: Two leaders configured; failover via Postgres advisory lock; tested with kill-9 scenarios.

### M5-T03 — llama.cpp RPC heterogeneous sharding

- Owner: BE · Effort: L · Depends on: M2-T10
- Files: `internal/engines/llamacpp_rpc.go`, `internal/scheduler/shard.go`
- Acceptance: 70B model sharded across 2 Mac Minis serves correctly; scheduler decides when sharding is needed.

### M5-T04 — AMD ROCm path

- Owner: BE · Effort: M · Depends on: M2-T07
- Files: `internal/engines/vllm.go` updates
- Acceptance: vLLM-ROCm container variant used for AMD GPUs; tested with one MI300 box (or community-validated).

### M5-T05 — Per-team quotas + chargeback

- Owner: BE · Effort: L · Depends on: M3-T02
- Files: `internal/auth/teams.go`
- Acceptance: Teams group users; team quotas roll up; cost report exportable per team.

### M5-T06 — Hardened security review

- Owner: BE · Effort: L · Depends on: all M3
- Files: `docs/security/v1-audit.md`
- Acceptance: External (or community) security review; all findings rated; criticals fixed before tag.

### M5-T07 — Headscale support

- Owner: BE · Effort: M · Depends on: M2-T02
- Files: `internal/mesh/headscale.go`
- Acceptance: Air-gapped deployment using self-hosted Headscale; documented in deployment guide.

### M5-T08 — v1.0 release

- Owner: DevOps · Effort: S · Depends on: M5-T06
- Files: tag, release notes
- Acceptance: Tag pushed, binaries shipped, Homebrew bumped, blog post live.

### M5-T09 — Tailscale `tsnet` mesh backend

- Owner: BE · Effort: M · Depends on: M2-T02
- Files: `internal/mesh/tailscale.go`
- Acceptance: Workers joining over a Tailscale tailnet auto-discover the leader and register without manual IP config; cross-network (multi-LAN) cluster works end-to-end with one worker on a different physical network than the leader.

### M5-T10 — NetBird mesh backend

- Owner: BE · Effort: M · Depends on: M5-T09
- Files: `internal/mesh/netbird.go`
- Acceptance: Same UX as Tailscale backend, but routed through a NetBird overlay (self-hostable). Documented in deployment guide.

### M5-T11 — Auto-rebalancing sharded models

- Owner: BE · Effort: L · Depends on: M2.5 sharding code, M5-T03
- Files: `internal/scheduler/sharding.go`, `internal/scheduler/placement.go`
- Acceptance: `flock shard create <model>` (no explicit N) computes the right shard count from worker count, model size in GB, and free VRAM per worker; reshards (drains + re-creates) if a worker joins or leaves and the existing split is no longer optimal. Manual override (`--shards N`) still respected.

### M5-T12 — Automatic GGUF distribution

- Owner: BE · Effort: L · Depends on: M4-T14 (rpc-server bundling), M2-T05 (worker process agent)
- Files: `internal/scheduler/distribute.go`, `internal/models/fetch.go`, `cmd/flock/cmd_model.go`
- Acceptance: `flock model add hf:owner/repo` downloads the chosen GGUF to the leader and streams it (or makes it fetchable via leader HTTP) to every worker that will host a shard, with checksum verification. `flock shard create <id>` no longer requires the GGUF to be pre-placed by hand. Progress is visible in `flock status` and the web UI. Resume on interrupted transfer. Why this matters: this is the single biggest UX gap for large open-weight models (Qwen3-72B, Llama-3.3-70B, DeepSeek-V3, MiniMax, Nemotron) — today users `wget` GGUFs onto every node manually.

---

## Parallelization map

Within a milestone, these tracks can run in parallel:

### M1 parallel tracks

- **BE-API track** — M1-T01 → T02 → T05 → T13 → T14 → T15 → T29
- **BE-Engine track** — M1-T08 → T09 → T10 → T11 → T12 → T19
- **BE-Auth track** — M1-T03 → T06 → T07 → T20
- **UI track** — M0-T05 → M0-T06 → M1-T22 → T23 → T24
- **DevOps track** — M0-T02 → M0-T03 → M0-T04 → M1-T25 → T26 → T27 → T28
- **Docs track** — M0-T07 → M1-T30

A team of 3 (1 BE, 1 UI, 1 DevOps moonlighting on docs) can clear M1 in 3–4 weeks.

### M2 parallel tracks

- **Mesh + agent** — M2-T01 → T02 → T03 → T04 → T05 → T06
- **Engines** — M2-T07 and M2-T08 in parallel
- **Anthropic adapter** — M2-T12 → T13 → T14 → T15
- **Scheduler** — depends on M2-T06 + M2-T09 → M2-T10 → M2-T11
- **UI** — M2-T16 → T17

### M3 parallel tracks

- **Auth + multi-tenant** — M3-T01 → T02 → T03 → T04
- **Metering + fallback** — M3-T05 → T06; M3-T08 + T09 → T10
- **Observability** — M3-T11 → T12 → T13
- **UI** — M3-T14 → T15 → T16 → T17

---

## Definition of done

A task is done when **all** of these are true:

- [ ] Code merged to `main`
- [ ] Unit tests for new public functions
- [ ] Integration test if the task crosses a subsystem boundary
- [ ] Doc updates in same PR (README, ARCHITECTURE, or in-code comments)
- [ ] Manual test run on at least one platform (Mac or Linux)
- [ ] No new `golangci-lint` warnings
- [ ] CI green
- [ ] Task checkbox flipped in `TASKS.md`
- [ ] **If the task adds a user-facing capability**: it ships as a `flock` CLI command first, with `--help` text. Any web UI for the same capability is a thin wrapper that POSTs to an admin endpoint, which in turn invokes the **same Go function the CLI invokes**. No mutating logic lives in the admin API layer. (See [M4-T20](#m4-t20--refactor-admin-api-wraps-cli-command-paths) for the canonical implementation pattern.)

A milestone is done when **all** tasks are checked **and** a recorded demo (asciinema or video) shows the milestone's headline capability working end-to-end.
