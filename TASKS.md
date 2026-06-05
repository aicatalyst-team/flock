# Flock — Implementation Plan

Concrete task breakdown for the team building Flock. Each milestone ships a usable product; each task is sized to fit one PR (≤2 days for a single dev).

For user-facing docs see [README.md](README.md). For design rationale see [ARCHITECTURE.md](ARCHITECTURE.md).

## Current shipped state (as of this drop)

- **M0 — foundations**: done (Go module, Makefile, gitignore, LICENSE, OSS docs, web embed scaffold).
- **M1 — single-node MVP**: done (CLI, OpenAI API + streaming, Ollama driver, hardware detect, catalog, install.sh; web UI shipped as a single embedded HTML page rather than the Next.js scaffold planned).
- **M2 — multi-node**: code-complete except the Tailscale `tsnet` backend (LAN backend ships instead; tsnet interface is defined). Anthropic adapter live, Claude Code verified-by-construction. vLLM + MLX drivers ship. Node register/heartbeat live. Cross-node *inference routing* (leader → worker engine) deferred.
- **M3 — multi-tenant + observability**: per-user keys with scopes/quotas/audit, usage metering, Prometheus metrics, hybrid fallback to Anthropic + OpenAI — all live. OIDC deferred.
- **M4 — polish**: minimal embedded UI shipped; LoRA / vision / Whisper / live migration deferred to v0.4.
- **Release tooling**: CI workflow, GoReleaser config, Homebrew formula, install.sh — all live.

What's still open: tsnet mesh, OIDC, cross-node inference routing, vision endpoint, embedded Whisper, LoRA adapter loading, full Postgres backend for HA, AMD ROCm path.

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

## Milestone 4 — Polish + public beta (Weeks 13–16)

Goal: launch publicly. Quality bar high enough to keep stars and adopt PRs.

### M4-T01 — Web UI: in-browser playground

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
- Acceptance: Renders README + ARCHITECTURE + per-page guides; hosted at `flock.dev/docs` via Cloudflare Pages.

### M4-T10 — Marketing site

- Owner: Docs · Effort: M · Depends on: M4-T09
- Files: `marketing/` (or merge into docs site)
- Acceptance: Landing page with hero, demo gif, "install" CTA, FAQ.

### M4-T11 — HN / Reddit / X launch

- Owner: Marketing/Docs · Effort: S · Depends on: M4-T09, M4-T10, M3-T18
- Acceptance: Posts queued and published in same hour; team monitors comments first 24 hours.

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

A milestone is done when **all** tasks are checked **and** a recorded demo (asciinema or video) shows the milestone's headline capability working end-to-end.
