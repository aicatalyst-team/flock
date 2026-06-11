# Changelog

What's shipped in Flock, organized by area. For the per-release diff see
[Releases](https://github.com/hadihonarvar/flock/releases) — every `feat:` /
`fix:` commit on `main` cuts a new tag automatically (semver-bumped by
conventional-commit footers). For what's coming next see
[ROADMAP.md](ROADMAP.md) and [TASKS.md](TASKS.md).

## Core (single-node, works today)

- Single binary (`go build ./cmd/flock` → ~25 MB) — no Python or Docker required
- **OpenAI-compatible** API (`/v1/chat/completions`, `/v1/embeddings`, `/v1/models`) — Cursor, Aider, Continue, Zed, OpenAI SDK
- **Anthropic-compatible** API (`/v1/messages`, `/v1/messages/count_tokens`) — Claude Code, Anthropic SDK
- Streaming (SSE) for both protocols, with proper client-disconnect handling (no goroutine leaks; bounded drain on cancel)
- **Hybrid fallback** — requests for `claude-*` or `gpt-*` transparently proxy to the real Anthropic / OpenAI API (set `ANTHROPIC_API_KEY` / `OPENAI_API_KEY`); protocol mismatch (e.g., Claude model on OpenAI route) returns a clear 400
- Engine drivers: **Ollama**, **vLLM**, **MLX-LM**, **llama.cpp** (single-node *and* RPC mode; `llama-server` is **auto-spawned** when the catalog entry has `source.repo` set — no manual `llama-server` step)
- Engine endpoints + API keys configurable per engine via env (`FLOCK_VLLM_ENDPOINT`, `VLLM_API_KEY`, …)
- Hardware auto-detection (mac + linux + NVIDIA) and auto-pick a default model
- Catalog with **37 curated model entries** spanning Llama, Qwen, Gemma, MiMo, DeepSeek, GPT-OSS, Mistral, Phi, Kimi, GLM, Nemotron, StepFun, Moondream, Pixtral families — with `released:` dates and license metadata enforced by CI
- **Non-catalog installs (4 paths)** — `flock model add hf:owner/repo`, `ollama:tag`, or `file:/abs/path.gguf` skip the catalog and pull anything the configured engine supports; `flock model add --from <my.yaml>` installs a user-written entry and persists it into `~/.flock/catalog/`; users can also drop YAML files directly into `~/.flock/catalog/<id>.yaml` and `flock model add <id>` treats them like curated entries. Dashboard's "Add custom model" input + `POST /admin/v1/models` accept the scheme prefixes too.
- **Per-key model allowlist** — `flock token create alice --models qwen-coder-7b,gpt-4o-mini` (or `--models 'claude-*'` for vendor families) restricts an API key to specific model ids. Unauthorized requests return HTTP 403 `model_not_allowed` with the allowed list, and refusals land in the audit log. Edit via `flock token edit <id> --add-model X / --remove-model Y / --set-models a,b,c / --clear-models`, dashboard Tokens tab, or `PATCH /admin/v1/tokens/{id}`. Existing keys (NULL allowlist) keep working unchanged.
- **Per-request fallbacks + retries** — `/v1/chat/completions` and `/v1/messages` accept a `flock` block in the request body (`flock.fallbacks`, `flock.num_retries`, `flock.retry_backoff_ms`) or the equivalent `X-Flock-Fallbacks` / `X-Flock-Num-Retries` / `X-Flock-Retry-Backoff-Ms` headers. A per-request fallback list replaces the catalog chain for that one call; the retry count wraps each candidate in an exponential-backoff loop (cap 5 retries, 5 s backoff). Trace spans carry `flock.fallback.source = catalog | request`, the audit log captures every override, and `flock_router_fallback_total{reason}` splits `catalog` / `per-request` / `retry`. CLI: `flock connect curl --retries 3` bakes the header into the snippet.
- **Typed fallback lists** — catalog entries can declare class-specific fallback chains: `fallback_on_context_length: [longctx-a]` (prompt too long) and `fallback_on_content_policy: [permissive-a]` (vendor refusal). The router classifies the primary's failure (sentinel `errors.Is` first, then heuristic substring match — covers llama.cpp's `n_ctx`, OpenAI's `maximum context length`, Anthropic refusals, Bedrock guardrails) and switches the remainder of the chain to the matching typed list. Generic `fallback:` still wins when no typed list matches. Span attribute `flock.fallback.classifier = generic | context-length | content-policy`; metric `flock_router_fallback_total{reason}` adds the same labels. `flock model info` renders all three chains.
- **Placement cooldown (circuit breaker)** — a worker that returns `placement_allowed_fails` consecutive engine errors gets parked for `placement_cooldown_seconds`. The router's `pick()` skips it until the cooldown expires; a single success after expiry resets the counter. Per-node, in-memory (rebuilt on restart). Env: `FLOCK_PLACEMENT_ALLOWED_FAILS`, `FLOCK_PLACEMENT_COOLDOWN_SECONDS`. New gauge `flock_router_cooldowns_active`; admin `/admin/v1/nodes` decorates each row with `cooldown_until`; dashboard Nodes tab renders a 🚫 cooldown badge with seconds-remaining. Both knobs must be > 0 to enable — either zero preserves the historical "always try the worker" behavior.
- **Sticky sessions (KV-cache locality)** — when `router.sticky_session_ttl_seconds > 0`, the router pins each (user_id, model) tuple to the worker that served its previous successful request, so multi-turn chats reuse the same node's KV cache between turns. The pin refreshes on each successful pick and expires after the TTL of inactivity. Bypassed for anonymous requests (no auth key) and for `model=auto` (auto-resolution may change between turns). Sticky pick is overridden when the previously-pinned node is in cooldown or fails the heartbeat check — falls through to the normal least-loaded pick. Env: `FLOCK_STICKY_SESSION_TTL_SECONDS`. New counter `flock_router_sticky_hits_total{outcome=hit|miss|expired}`.
- **RPM/TPM rate limits** — per-key `--rpm` (requests/min) and `--tpm` (tokens/min) ceilings backed by in-memory leaky buckets. On admit the middleware deducts 1 RPM token + an upfront prompt-size estimate from TPM; on overflow returns HTTP 429 `rate_limited` with `Retry-After` reflecting bucket refill time. After the response, `recordUsage` reconciles the estimate against actual prompt+completion tokens (refunds on over-estimate, deducts on under-estimate). CLI: `flock token create alice --rpm 60 --tpm 100000`, `flock token edit k_abc --rpm 30 --tpm 50000`. Dashboard: per-row RPM/TPM cells + inline edit prompt + create-form inputs. Buckets are per-process; reset on leader restart (acceptable for v1).
- **Time-bucketed usage breakdown** — new `GET /admin/v1/usage/breakdown?bucket=day&since=YYYY-MM-DD&group_by=user,model` returns aggregated prompt/completion/request counts. Bucket ∈ {`hour`, `day`, `month`, `total`}; `group_by` is any comma-separated subset of {`user`, `model`, `protocol`, `outcome`}. Defaults: last 30 days, day buckets. CLI: `flock usage --by user,model --bucket day --since 2026-05-01`. Dashboard Usage tab adds a Breakdown panel with bucket/group/since controls and a totals footer. New indexes on `usage(ts)` and `usage(model, ts)` keep the query under 100 ms on tens of thousands of rows.
- **Per-call $ cost tracking** — every usage row now stores a `cost_usd` snapshot computed at write time, so historical totals stay correct when vendor pricing changes. Pricing comes from (1) the catalog entry's optional `price_prompt_usd_per_1k`/`price_completion_usd_per_1k` fields, then (2) a built-in vendor table (`internal/models/vendor_pricing.go`) covering current Claude and OpenAI rates with longest-prefix matching. Open-weight catalog entries default to `$0` (no cost tracked). Surfaced in: `/admin/v1/usage/summary` (`cost_usd_total`, `cost_usd_today`, per-model `cost_usd`), `/admin/v1/usage/breakdown` (`cost_usd` column + total), CLI `flock usage --summary` ($ spent line) and `flock model info` (pricing row), and the dashboard's new "$ today" KPI card.
- **Monthly + dollar budgets** — per-key spend caps that compose with AND semantics. A key with `$10/day AND $100/month AND 1M tokens/day` is refused as soon as any single budget hits its limit. Windows: `day`, `week`, `month` (UTC boundaries). Units: `tokens` or `usd`. Lazy reset at admission time means there's no cron — the next request after window expiry rolls the counter. Overflow returns HTTP 429 with `error.type=budget_exceeded` and `{unit, window, current, limit, reset_at, retry_after}` so clients can show a meaningful "you've spent $100 this month" message; every refusal lands in the audit log. CLI: `flock token budget add k_abc --window month --limit 100 --unit usd`, `flock token budget ls k_abc` (shows utilization %), `flock token budget rm k_abc 4`. Admin: `GET/POST /admin/v1/tokens/{id}/budgets`, `DELETE /admin/v1/tokens/{id}/budgets/{bid}`. Dashboard Tokens tab gains a "budgets…" action with utilization bars + an add form.
- **Standard rate-limit response headers** — every `/v1/*` response now carries OpenAI-style `X-RateLimit-Limit-Requests`, `X-RateLimit-Remaining-Requests`, `X-RateLimit-Reset-Requests` and the `*-Tokens` counterparts (when the key has RPM/TPM configured), plus an always-emitted `X-Flock-Request-Id` correlation token. Budget overflow adds `X-Flock-Budget-Reset-At`. The request id is propagated into the audit log's metadata column for `model_not_allowed`, `budget_exceeded`, and `router.override` rows so `flock audit --json | jq` can correlate response → audit entry.
- **OpenAI-compatible hosted gateway adapters** — passthrough to OpenRouter, Groq, Together, Fireworks, Cohere, Mistral La Plateforme, and Perplexity. Slash-namespaced prefixes (`openrouter/<model>`, `groq/<model>`, `cohere/<model>`, etc.) are recognized by `Vendor()` and routed through a shared OpenAI-shape proxy that strips the prefix from the request body's `model` field before forwarding. Env: `OPENROUTER_API_KEY`, `GROQ_API_KEY`, `TOGETHER_API_KEY`, `FIREWORKS_API_KEY`, `COHERE_API_KEY`, `MISTRAL_API_KEY`, `PERPLEXITY_API_KEY`. URLs default to each vendor's public endpoint; override per-vendor via `router.fallback.{openrouter,groq,together,fireworks,cohere,mistral,perplexity}_url`. Vendor pricing entries seeded for Cohere (Command R+, Command R), Mistral (Large/Medium/Small/Codestral/Ministral), and Perplexity (Sonar Pro/Reasoning/Sonar). **Vertex full body translation** (OpenAI/Anthropic → `generateContent`) is the missing half — the existing ADC-probe stub still returns 501 with an actionable hint for `gemini-*` ids.
- **Observability callbacks (webhooks + Langfuse)** — usage and audit events fan out to external sinks configured under `observability.callbacks` in `config.yaml`. Each sink runs in its own goroutine with a bounded queue (default 100); a slow receiver is non-blocking on the hot path — overflow events are dropped and counted on `flock_callback_sent_total{outcome="dropped"}`. Webhook driver signs every payload with HMAC-SHA256 (`X-Flock-Signature: sha256=<hex>`) when a `secret` is set; events filter (`events: [usage, audit]`) lets a sink opt out per kind. Langfuse driver maps usage events into a "generation" observation against `/api/public/ingestion`. New gauge `flock_callback_queue_depth{sink}`. Admin: `GET /admin/v1/callbacks` lists the configured sinks; `POST /admin/v1/callbacks/test[?sink=name]` fires a synthetic `test` event so an operator can verify wiring before real traffic. Fallback events are not yet wired (router→callbacks dep wasn't worth the layering); only usage and audit ship today.
- **`/v1/rerank` + `/v1/audio/*` endpoint shells** — `/v1/rerank` proxies to a llama-server instance (which has native rerank support since b3580) and emits the same Cohere-shape response clients expect. `/v1/audio/transcriptions` and `/v1/audio/speech` are wired routes that proxy to optional `FLOCK_WHISPER_ENDPOINT` / `FLOCK_PIPER_ENDPOINT` configured engines; unconfigured returns HTTP 501 with the exact env var to set + project link (whisper.cpp, piper). Each endpoint records a usage row with `protocol = rerank | audio-asr | audio-tts` so the usage breakdown groups them naturally.
- **Response cache (embeddings)** — deterministic embedding requests are cached so repeat queries (RAG retrieval, batch evals) skip the engine entirely. Two drivers: in-memory LRU (default, bounded by `max_entries`) and SQLite-backed (survives leader restart, reuses `~/.flock/state.db`). Cache key is a sha256 of the canonicalized request body — JSON object keys sorted alphabetically, ephemeral fields like `user` stripped — so byte-different but semantically-equal requests collide. Per-request opt-out via `Cache-Control: no-cache` / `no-store`. Per-tenant scoping via `flock.cache.namespace` in the request body. Response carries `X-Flock-Cache: hit | miss`. Metrics: `flock_cache_hits_total{path}`, `flock_cache_misses_total{path}`. Admin: `GET /admin/v1/cache/stats`, `DELETE /admin/v1/cache?namespace=X` or `?all=1`. Config: `observability.response_cache: { enabled, driver, max_entries, default_ttl_seconds }`. **Chat completion caching** is deferred — properly replaying a streamed response from cache deserves its own design pass.
- **Guardrails (pre-call hook + webhook driver)** — `observability.guardrails` in `config.yaml` chains synchronous content checks against an external service before the engine sees the request. Each row picks a mode (`pre` blocks/rewrites; `logging_only` observes) and a driver (`webhook` today). The webhook POSTs the request body and reads `{"action":"allow|block|rewrite|flag","reason":"…","replacement":<new body>}`. Pre-mode block returns HTTP 403 `guardrail_blocked` with the guardrail name + reason; rewrite swaps the body and the chain continues. `fail_open: true|false` chooses Allow vs Block on guardrail unreachable. Metric `flock_guardrail_action_total{name,action}`; every block / flag lands in the audit log under `guardrail.<action>`. Post-call mode is wired in config but not yet evaluated — that needs careful streaming-response handling and is queued for follow-up. Specific drivers (Presidio, Bedrock Guardrails) work today by writing a thin shim that translates between their APIs and the documented webhook contract.
- **Key expiration (TTL)** — API keys can carry an `expires_at` timestamp. The auth middleware returns HTTP 401 `key_expired` once the timestamp is past. CLI: `flock token create alice --ttl 7d` (Go-style durations with `d` extension for days, or `--expires-at YYYY-MM-DD` for absolute dates), `flock token expire k_abc [--in 1h]` to flip immediately or in the near future, `flock token renew k_abc --ttl 30d` to extend. Admin: `POST /admin/v1/tokens` accepts `expires_at` or `ttl_seconds`; `PATCH /admin/v1/tokens/{id}` accepts both (use `expires_at: null` to clear). Dashboard Tokens tab shows a per-row badge: green "active · 14d", amber "expires in 3d" (≤ 7 days), red "expired" / "revoked". New "expiry…" action opens a modal with extend/set-date/clear options.
- **Vision** via `image_url` content blocks on `/v1/chat/completions` (Ollama path); **embeddings** via `/v1/embeddings`
- **Typed `engine_unreachable` errors** with engine name, endpoint, and the exact command to start it (`ollama serve`, `mlx_lm.server …`, etc.) when the upstream engine isn't responding
- **Engine health watchdog** on auto-spawned engines (force-restart after 3 consecutive failures, covers hung `llama-server`)

## CLI ergonomics

- Interactive picker (`flock model add|info|remove`, `flock connect` with no ID launches a fuzzy-filter picker — ↑↓ / enter)
- Shell completion (`flock completion bash|zsh|fish`)
- Colored output (auto-detects TTY; respects `NO_COLOR` / `FLOCK_NO_COLOR`)
- `--json` on every read command (`model search/ls/info`, `status`, `usage`, `audit`) for scripting
- `flock usage --summary` / `flock audit --summary` aggregate views (top models, p50/p95/p99, error rate, unicode-block sparkline) — same data as the dashboard home view
- First-run wizard on `flock up` (picker-driven starter-model install; skip with `--no-wizard`)
- Real progress bar on `flock model add` with bytes/sec + ETA (smoothed over a 1 s window)
- `--dry-run` on `flock model add` (preview download size, engine, RAM check, ETA without pulling weights)
- Confirmation prompt on `flock model remove` / `flock node remove` / `flock shard remove` (skip with `--yes`)
- `flock model unload <id>` drops a model from engine RAM without deleting weights (Ollama; other engines return a soft warning)
- Did-you-mean for top-level subcommand typos (Damerau-Levenshtein over the registered subcommand list)

## Multi-node + sharding

- `flock token create --node` issues a worker join token
- `flock join <leader>?token=…` registers + starts a worker HTTP server bound to the LAN / tailnet address
- Workers run their own engine (Ollama / vLLM / MLX); leader proxies inference requests to them
- **Router** picks the right node per request: local-preferred if the model is loaded locally, otherwise least-loaded worker that has the model
- **Heartbeat-freshness pre-dispatch check** — pick() skips workers whose last heartbeat exceeds `SetHeartbeatMaxAge` (configurable) and falls back to local rather than waiting for the engine call to time out
- **Heartbeat carries loaded models** every 5 s; leader reconciles the placements table automatically
- Agent handles auth errors gracefully (401 → exit, 404 → re-register, transient → exponential backoff)
- **Sharding auto-orchestration** — `flock shard create <model> <N>` picks N workers, launches `rpc-server` on each via the worker process-supervisor API, launches the coordinator `llama-server --rpc <list>`, registers the placement, and the Router routes requests to the coordinator transparently. Web UI exposes the same in the Shards tab.
- **Auto-distribution of GGUF weights** from leader to shard hosts (sha256-verified)
- **Coordinator placement on the strongest worker** (not just leader); override via `FLOCK_COORDINATOR_NODE`
- Process supervisor (`internal/agent/supervisor.go`) — Start/Stop/Logs with TCP-port readiness probe, used by the leader for the coordinator and by workers for `rpc-server`
- Shard crash auto-restart (up to 5 attempts with exponential backoff) before declaring `crashloop`

## Routing intelligence

- **Catalog fallback chains** — any catalog entry can declare `fallback: [next-id, …]`; the router walks the chain on engine error / 5xx / timeout / model-not-loaded. Transparent to clients; logged via structured slog; counted in `flock_router_fallback_total`
- **Latency-aware fallback** (Bet #1) — opt-in via `router.latency_fallback_p95_seconds` (env `FLOCK_LATENCY_P95_SECONDS`). When a model's recent p95 exceeds the threshold, the router walks the fallback chain for a faster candidate to try first. O(1) ring-buffer per model for the rolling window.
- **`SetMaxFallbackAttempts`** caps how many candidates the router will walk before giving up; `flock_router_fallback_total{reason=cap-exhausted}` for visibility
- **Bedrock SigV4 signing** for `anthropic.*` family (non-streaming); **Vertex ADC probe** wired (body translation pending)

## Multi-tenancy

- Per-user API keys with scopes (admin / user / node), daily token quotas, full audit log
- Usage metering — every request recorded with model / protocol / tokens / latency
- **HMAC-SHA256 signatures** on control-plane traffic so the worker token isn't sent on the wire after the initial join. Set `FLOCK_REJECT_BEARER=1` on workers to refuse the bearer-fallback path entirely.
- OIDC for the web UI — **planned**; the UI uses a pasted admin key (or the localhost auto-bootstrap) for now

## Observability

- Prometheus metrics at `/metrics`:
  - `flock_requests_total{model,protocol,outcome}`, `flock_request_duration_seconds`, `flock_request_tokens_total{model,direction}`
  - `flock_model_loaded{model,node}`, `flock_node_up{node,hostname}`
  - **Router subsystem**: `flock_router_picks_total{path,outcome}`, `flock_router_inflight{node}`, `flock_router_fallback_total{op,reason}`, `flock_router_attempt_duration_seconds{model,outcome}`
- **OpenTelemetry / OTLP traces** end-to-end (HTTP → router → engine) across all four drivers, with prompt / completion token counts as span attributes. Set `FLOCK_OTLP_ENDPOINT` to a collector URL; empty disables tracing with zero overhead.
- Structured slog events for fallback decisions and stale-heartbeat skips
- Reference Grafana dashboards in [`dashboards/`](dashboards/) — cluster overview, per-model, per-node

## Web UI

Embedded as a single HTML file (Tailwind via CDN). Tabs: Dashboard, Connect, Playground, Nodes, Models, Shards, Tokens, Usage, Audit, Settings.

- **Top-bar chips** on every view: role (leader/worker), engine reachability, node count, model count — polled every 5 s
- **Home tab**: 4 KPI cards (nodes / models / requests / tokens) + latency p50/p95/p99 + tier-colored error-rate + top-model + full-width SVG sparkline (requests per minute, last 60 min) + recent-activity strip
- **Models tab**: installed table with per-row **test** (opens Playground pre-wired), **unload** (drop from engine RAM, keep weights on disk), and **remove** buttons; **filterable catalog browser** (search, sort by size / newest / id, hide-installed toggle, color-coded license badge, per-row Install button)
- **Nodes tab**: list + status; **Add a worker** modal generates a one-time node-scope token and shows both an install-and-join curl one-liner and a `flock join` command for boxes that already have the binary; per-row drain / remove with confirmation
- **Playground**: chat with images (`image_url` content blocks) + embeddings + tool-call inspection
- **Live updates via SSE** — `/admin/v1/events` pushes `models` / `nodes` / `shards` events; the active tab re-fetches instantly. 15 s polling fallback runs underneath as a safety net (pauses when the browser tab is hidden).
- **Toast notifications** (bottom-right, 3 s auto-dismiss) for adds / removes / errors
- **Keyboard shortcuts** (vim-style leader sequence): `g d / c / p / n / m / h / t / u / a / s` to jump between tabs; `?` opens a cheatsheet; `Esc` closes modals
- **Localhost auto-login** via `/admin/v1/bootstrap-key` so same-host users skip the paste-key step

## Connect snippets

`flock connect <client>` prints copy-paste config (URL + token already filled in) for **19 supported clients**: claude-code, cursor, aider, continue, zed, cline, qwen-code, hermes, openclaw, opencode, open-webui, open-notebook, goose, plandex, openhands, codex-cli, openai-sdk, anthropic-sdk, curl. `flock connect --list` for the live roster; `flock disconnect <client>` for reversal steps. `flock invite <name>` creates a user-scope token + share card with the same per-client snippets.

## Release + ops

- GitHub Actions CI on every push (vet, race tests, build)
- Auto-release workflow: every `feat:` / `fix:` commit on `main` runs `goreleaser` and cuts a new tag (`BREAKING CHANGE:` footer triggers a major bump; otherwise `feat` → minor, `fix` → patch)
- GoReleaser builds darwin/linux × arm64/amd64 + `.deb` + `.rpm` packages; checksums published
- One-line install (`curl -fsSL https://raw.githubusercontent.com/hadihonarvar/flock/main/installer/install.sh | sh`) pulls the latest release for the host's platform and verifies the SHA-256 against the published `checksums.txt`
- `flock update` performs an in-place binary swap (or stages next to the existing binary for sudo installs)
- Two-node verification: in-process E2E test (`internal/controlplane/two_node_e2e_test.go`) + manual walkthrough (`docs/TWO_NODE_VERIFICATION.md`) + a 30-second smoke script (`scripts/two-node-smoke.sh`)

## Verified to work

- `go build ./cmd/flock` — clean on go 1.25 / darwin-arm64
- `go vet ./...` — clean
- `flock up` boots, bootstraps admin key, starts gateway
- `flock up` → `curl /v1/models` returns the auto-picked model
- `curl /v1/chat/completions` reaches Ollama and translates errors back as proper OpenAI shape

> ⚠️ Apple Silicon heads-up: the Homebrew `ollama` formula is missing the internal `llama-server` binary on some versions — chat returns `500: llama-server binary not found`. Use the cask (`brew install --cask ollama`) or the official installer instead. `flock doctor` detects this and warns you.
