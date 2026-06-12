# Flock

> **Self-hosted AI for your team. One endpoint. Your hardware.**

[![License](https://img.shields.io/github/license/hadihonarvar/flock?color=blue)](LICENSE)
[![Go](https://img.shields.io/github/go-mod/go-version/hadihonarvar/flock)](go.mod)
[![Release](https://img.shields.io/github/v/release/hadihonarvar/flock?sort=semver)](https://github.com/hadihonarvar/flock/releases/latest)
[![CI](https://github.com/hadihonarvar/flock/actions/workflows/ci.yml/badge.svg)](https://github.com/hadihonarvar/flock/actions/workflows/ci.yml)
[![Auto-release](https://github.com/hadihonarvar/flock/actions/workflows/auto-release.yml/badge.svg)](https://github.com/hadihonarvar/flock/actions/workflows/auto-release.yml)

[**flockllm.com**](https://flockllm.com) · [GitHub](https://github.com/hadihonarvar/flock) · Maintained by [Hadi Honarvar Nazari](https://www.linkedin.com/in/hadi-honarvar-nazari/) · Apache-2.0

> Flock is the **self-hosted control plane for LLMs**. One Go binary turns your Macs and Linux boxes into a private inference cluster — multi-machine routing, per-user keys, daily quotas, full audit log, and a built-in admin dashboard, behind one endpoint that speaks both the **OpenAI** and **Anthropic** APIs.
>
> Engine-agnostic: bring **Ollama**, **vLLM**, **MLX-LM**, or **llama.cpp-RPC**. Run open-weight models (Qwen, Llama, DeepSeek, …) on your own hardware, shard a giant model across several machines via llama.cpp-RPC, and transparently fall back to paid Claude / GPT only when you choose.
>
> Point Cursor, Claude Code, Aider, Continue, or any OpenAI/Anthropic SDK at Flock. It just works.

## 🗺️ Where Flock sits

```
           ┌──────────────────────────────────────────────────────────────┐
           │                       YOUR USE CASES                         │
           │             (the tools your team already uses)               │
           └──────────────────────────────────────────────────────────────┘
                  │           │          │             │            │
                  ▼           ▼          ▼             ▼            ▼
            ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐
            │  Cursor  │ │  Claude  │ │  Aider   │ │  Custom  │ │   curl   │
            │          │ │   Code   │ │          │ │ Python   │ │  scripts │
            │          │ │          │ │          │ │   SDK    │ │          │
            └────┬─────┘ └────┬─────┘ └────┬─────┘ └────┬─────┘ └────┬─────┘
                 │  OpenAI    │ Anthropic  │  OpenAI    │  Either    │  HTTP
                 └────────────┴────────────┴────────────┴────────────┘
                                          │
                                          │   ONE URL · ONE API KEY
                                          ▼
      ╔══════════════════════════════════════════════════════════════════════╗
      ║                  ⬢ ⬢ ⬢   FLOCK   ⬢ ⬢ ⬢                              ║
      ║                  (this is what we built)                             ║
      ║  ════════════════════════════════════════════════════════════════    ║
      ║  Gateway     OpenAI + Anthropic on /v1/chat/completions              ║
      ║              per-user keys · daily quotas · full audit log           ║
      ║              admin dashboard at :8080                                ║
      ║                                                                      ║
      ║  Router      Same model on N nodes  → load-balance                   ║
      ║              Different models per node → route by placement          ║
      ║              Model bigger than any node → split via llama.cpp-RPC    ║
      ║              Claude / GPT requested → proxy to vendor                ║
      ║              Engine error or timeout  → retry catalog fallback chain ║
      ╚═════════════════════════════╤════════════════════════════════════════╝
                                    │
              ┌─────────────────────┼─────────────────────┐
              ▼                     ▼                     ▼
       ┌─────────────┐       ┌─────────────┐       ┌─────────────┐
       │   Engines   │       │   Engines   │       │   Egress    │
       │  (any mix)  │       │  (any mix)  │       │   proxy     │
       │  • Ollama   │       │  • Ollama   │       │             │
       │  • vLLM     │       │  • vLLM     │       │ api.anthro- │
       │  • MLX-LM   │       │  • MLX-LM   │       │ pic.com     │
       │  • llama.cpp│       │  • llama.cpp│       │ api.openai  │
       └──────┬──────┘       └──────┬──────┘       │ .com        │
              │                     │              └──────┬──────┘
              ▼                     ▼                     ▼
      ┌──────────────────────────────────────────────────────────────────────┐
      │                    UNDERLYING LLMs / WEIGHTS                         │
      │                                                                      │
      │   YOUR HARDWARE                              VENDOR APIs             │
      │   • Mac Studio · Mac Mini                    • Claude (Anthropic)    │
      │   • Linux + RTX GPU                          • GPT, o3, o4 (OpenAI)  │
      │                                                                      │
      │   37 curated catalog models (Qwen 3.6,        Each request routed   │
      │   gpt-oss, Llama 4, Gemma 4, DeepSeek V4,     to EITHER your hard-  │
      │   Kimi K2.6, Nemotron 3 Ultra, vision +       ware OR a vendor —    │
      │   embedding models)                           you pay vendors only  │
      │   + any HuggingFace or Ollama model.          when YOU chose to.    │
      └──────────────────────────────────────────────────────────────────────┘
```

**One-sentence version:** Flock is the layer that lets your tools talk to *any* LLM — open-weight on your hardware, or hosted Claude / GPT — through **one URL and one API key**, with the team controls (quotas, audit, per-user keys) that the raw vendor APIs don't give you.

---

## 🚀 Try it in 60 seconds

Flock is engine-agnostic. The quickest path uses **Ollama** as the local engine — but vLLM, MLX-LM, and llama.cpp-RPC all work. See [Choose your engine](#choose-your-engine) below for the alternatives.

### 🍎 macOS (Apple Silicon — M1/M2/M3/M4)

```bash
# 1. install Flock
curl -fsSL https://raw.githubusercontent.com/hadihonarvar/flock/main/installer/install.sh | sh
export PATH="$HOME/.local/bin:$PATH"   # if the installer says so

# 2. install an engine (pick one) — Ollama is the simplest default
brew install --cask ollama && open -a Ollama
# alternatives: pip install mlx-lm  ·  or run llama.cpp's llama-server  ·  or run vLLM in Docker

# 3. start Flock with a tiny model (~1 GB, fast download)
FLOCK_DEFAULT_MODEL=llama-3.2-1b flock up
```

### 🐧 Linux (x86_64 or arm64) — including Raspberry Pi, NAS, edge boxes

**Option A — `.deb` / `.rpm` package** (recommended for Debian / Ubuntu / Raspbian / QNAP / Asustor / Fedora / RHEL):

```bash
# Debian / Ubuntu / Raspbian (arm64 example — also amd64)
curl -LO https://github.com/hadihonarvar/flock/releases/latest/download/flock_VERSION_linux_arm64.deb
sudo dpkg -i flock_VERSION_linux_arm64.deb
# Binary at /usr/bin/flock, catalog at /usr/share/flock/catalog
# Recommends llama.cpp for sharding — install via apt if you want it.

# Fedora / RHEL / CentOS
sudo rpm -i https://github.com/hadihonarvar/flock/releases/latest/download/flock_VERSION_linux_amd64.rpm
```

(Replace `VERSION` with the latest from [Releases](https://github.com/hadihonarvar/flock/releases). The package version stays current via your distro's normal upgrade path — `flock update` also works as an in-place binary swap for non-package installs.)

**Option B — install.sh** (works everywhere; drops binary in `~/.local/bin/` and catalog in `~/.flock/catalog/`):

```bash
# 1. install Flock
curl -fsSL https://raw.githubusercontent.com/hadihonarvar/flock/main/installer/install.sh | sh
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.bashrc && source ~/.bashrc

# 2. install an engine (pick one) — Ollama is the simplest default
curl -fsSL https://ollama.com/install.sh | sh && sudo systemctl enable --now ollama
# alternatives: vLLM in Docker for NVIDIA  ·  llama.cpp's llama-server  ·  MLX-LM (Apple Silicon only)

# 3. start Flock with a tiny model (~1 GB, fast download)
FLOCK_DEFAULT_MODEL=llama-3.2-1b flock up
```

> 💡 Not sure which engine to install? Run `flock doctor` after step 1 — it inspects your hardware and tells you the single command to run.

### What you should see (both platforms)

Flock prints something like:

```
✔ default model: llama-3.2-1b
✔ engine: ollama at http://127.0.0.1:11434
  Flock is ready.
  API:    http://localhost:8080/v1
  Admin API key:   sk-orc-xK9p…
```

**Every command supports `--help`** — `flock <cmd> --help` prints usage, flags, and examples.

**Copy that admin key.** In another terminal:

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-orc-xK9p…" \
  -d '{"model":"auto","messages":[{"role":"user","content":"hi in 5 words"}]}'
```

You should see a JSON response with a 5-word reply. 🎉

**Or use the web dashboard**: open `http://localhost:8080` and paste the admin key.

**Or wire up Claude Code**: in any terminal where you use Claude Code, set:

```bash
export ANTHROPIC_BASE_URL=http://localhost:8080
export ANTHROPIC_AUTH_TOKEN=sk-orc-xK9p…
claude
```

…and Claude Code talks to your local model instead of paying for the API.

**If something breaks**, run `flock doctor` — it tells you exactly what to fix. Common issues are in the [Troubleshooting installation](#troubleshooting-installation) section.

---

| | |
|---|---|
| **Status** | Beta — single-node verified end-to-end (curl, dashboard, CLI); multi-node routing has in-process E2E coverage (`internal/controlplane/two_node_e2e_test.go`); real two-machine verification via the [30-sec smoke script](scripts/two-node-smoke.sh) + [manual walkthrough](docs/TWO_NODE_VERIFICATION.md). Auto-released on every `feat:` / `fix:` commit (see [Releases](https://github.com/hadihonarvar/flock/releases)). |
| **License** | Apache 2.0 |
| **Language** | Go (orchestrator + embedded HTML UI) |
| **Platforms** | macOS (Apple Silicon), Linux (x86_64, arm64) |

## What's shipped

See [CHANGELOG.md](CHANGELOG.md) for the full feature inventory, grouped by area (core, CLI ergonomics, multi-node + sharding, routing intelligence, multi-tenancy, observability, web UI, connect snippets, release + ops). For the per-release diff see [Releases](https://github.com/hadihonarvar/flock/releases) — every `feat:` / `fix:` commit on `main` cuts a new tag automatically.

**For new users**: see [QUICKSTART.md](QUICKSTART.md) — 3-minute install + first chat completion.
**For full usage docs**: keep reading this file.
**For contributors**: see [ARCHITECTURE.md](ARCHITECTURE.md).
**For the dev team's roadmap**: see [TASKS.md](TASKS.md).

---

## Table of contents

- [Why Flock?](#why-flock)
- [60-second quick start](#60-second-quick-start)
- [Who is this for?](#who-is-this-for)
- [Architecture overview](#architecture-overview)
- [Features](#features)
- [Supported models](#supported-models)
- [Supported clients](#supported-clients)
- [Hardware recommendations](#hardware-recommendations)
- [Installation](#installation)
- [Configuration](#configuration)
- [Cluster operations](#cluster-operations)
- [Managing models](#managing-models)
- [Connecting clients](#connecting-clients)
- [API reference](#api-reference)
- [CLI reference](#cli-reference)
- [Web UI](#web-ui)
- [Troubleshooting](#troubleshooting)
- [FAQ](#faq)
- [License](#license)

---

## Why Flock?

AI coding tools are the new dev tax. Cursor, Claude Code, Copilot, custom agents — every team uses them, and the bill grows with usage. A single engineer running modern agentic tools heavily can burn $200–500/month in API tokens. For a team of 10 that's $30–60k a year, and rising. Every request also sends proprietary code to a third party.

There are excellent open-weight models now — Qwen3-Coder, Llama 3.3, DeepSeek-V3 — that match or exceed paid APIs for most coding work. But running them across a few machines, exposing them through one API, routing traffic intelligently, and making it all feel as easy as `pip install` is *not* solved.

**Flock is the orchestration layer.** It does for self-hosted LLMs what Kubernetes did for web services — minus the YAML. One binary. One install command. Auto-discovery. Auto-placement. Drop-in compatibility with every tool you already use.

### Design principles

1. **One binary, zero dependencies.** Static Go executable. No Python, no Docker (unless you want it), no virtualenv. Curl it down and run.
2. **Zero config to first response.** Smart defaults everywhere. Hardware auto-detected. Model auto-picked. Network auto-meshed.
3. **The UI tells you the next step.** Every state in the web UI has a clear, copy-pasteable next action. Juniors should never stare at a blank prompt.
4. **Heterogeneous is invisible.** Mac, NVIDIA, AMD — the user picks models, not hardware.
5. **OpenAI- and Anthropic-compatible from day one.** Same endpoint serves both protocols.
6. **Permissive open source.** Apache 2.0. No open-core gotchas.
7. **The CLI is the source of truth.** Every user-facing capability ships as a `flock` CLI command first. The web UI is a thin wrapper — it invokes the same Go functions the CLI invokes, never reimplements logic. If you can do it in the UI, you can do it in CI / scripts / SSH sessions, and vice versa.
8. **Adding or switching a model is one action.** No hand-written YAML, no manual GGUF downloads, no separate worker-side setup. `flock model add hf:owner/repo` does the rest — picks engine, picks quant, shards if needed, distributes weights, warms the model. The default model is auto-picked from hardware on first `flock up`; to change it later, set `router.default_model` in `~/.flock/config.yaml` and restart, or `FLOCK_DEFAULT_MODEL=<id> flock up`.

---

## 60-second quick start

### On the first machine (becomes the leader)

```bash
curl -fsSL https://raw.githubusercontent.com/hadihonarvar/flock/main/installer/install.sh | sh
flock up
```

You'll see:

```
▶ detected darwin/arm64 · 24 GB RAM · 8 cores
✔ default model: qwen-coder-7b
✔ engine: ollama at http://127.0.0.1:11434
▶ pulling qwen-coder-7b · downloading [████████████████████] 4.7/4.7 GB · 85 MB/s · ETA 0:00
✔ model ready: qwen-coder-7b

  Flock is ready.

  Dashboard: http://localhost:8080
  API:    http://localhost:8080/v1
  Key:    sk-orc-xK9p…  (also in UI)

  Add another machine:
    curl -fsSL https://raw.githubusercontent.com/hadihonarvar/flock/main/installer/install.sh | sh -s -- join flock-7f3a.ts.net?token=…
```

### On any additional machine

```bash
curl -fsSL https://raw.githubusercontent.com/hadihonarvar/flock/main/installer/install.sh | sh -s -- join flock-7f3a.ts.net?token=…
```

The agent auto-joins the mesh, registers its capabilities, and the leader assigns it a model. You don't pick anything; you don't open any firewall ports.

### Test it from your terminal

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-orc-xK9p…" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "auto",
    "messages": [{"role":"user","content":"write fizzbuzz in rust"}]
  }'
```

### Use it from Claude Code

```bash
export ANTHROPIC_BASE_URL=http://localhost:8080
export ANTHROPIC_AUTH_TOKEN=sk-orc-xK9p…
claude
```

Claude Code is now talking to your local Qwen-Coder. Same UX, your hardware.

---

## Who is this for?

| You are… | Flock helps you… |
|---|---|
| A **10–50 person dev team** spending $30k+/yr on Claude/GPT APIs | Run the same workflows on hardware that pays for itself in <6 months |
| A **regulated org** (legal, health, defense) that can't send code to third parties | Keep 100% of inference on-prem; optional opt-in fallback to vendor APIs |
| An **AI/ML lab** with mixed-spec workstations and lab Macs | Pool all of it into one cluster behind one API |
| A **solo developer** who wants one endpoint covering their laptop, home server, and lab GPU | Use Cursor/Claude Code anywhere with the same key |
| A **classroom or research group** | Give every student a real LLM endpoint without per-seat costs |
| An **MSP or platform team** | Offer "internal Claude" as a service to product teams without lock-in |

### Non-goals

- **Training or fine-tuning** — Flock serves inference. Use Axolotl / Unsloth / torchtune for training, import the adapter.
- **Replacing real Claude Opus** — open models won't match Anthropic's frontier for long agentic runs. Flock makes the hybrid clean, not the choice unnecessary.
- **A SaaS product** — Flock is the software you run. The OSS is always complete.

---

## Architecture overview

```
   CLIENTS  (Cursor · Claude Code · Aider · SDKs · curl)
                       │
                       ▼  one endpoint, one key
   ┌──────────────────────────────────────────────────┐
   │  GATEWAY      OpenAI + Anthropic compatible      │
   │               auth · routing · streaming · log   │
   └────────────────────┬─────────────────────────────┘
                        │
        ┌───────────────┼──────────────────┐
        ▼               ▼                  ▼
   ┌────────────┐ ┌────────────┐    ┌──────────────────┐
   │ Worker A   │ │ Worker B   │    │ External APIs    │
   │ Linux+GPU  │ │ Mac Mini   │    │ (Claude, GPT…    │
   │ vLLM       │ │ MLX-LM     │    │  fallback)       │
   └────────────┘ └────────────┘    └──────────────────┘
        ▲               ▲
        │               │  heartbeats, assignments
   ┌────┴───────────────┴──────────────────────────────┐
   │  CONTROL PLANE                                    │
   │  node registry · model registry · scheduler · UI  │
   └───────────────────────────────────────────────────┘
                        ▲
                        │ embedded Tailscale mesh
                        │ (mTLS, NAT-traversed)
```

See [ARCHITECTURE.md](ARCHITECTURE.md) for the full design.

---

## Features

### Inference

- OpenAI-compatible API (`/v1/chat/completions`, `/v1/embeddings`, `/v1/models`, `/v1/rerank`)
- Anthropic-compatible API (`/v1/messages`, `/v1/messages/count_tokens`)
- Audio endpoints (`/v1/audio/transcriptions`, `/v1/audio/speech`) — proxies to optional `FLOCK_WHISPER_ENDPOINT` / `FLOCK_PIPER_ENDPOINT`; returns HTTP 501 with setup hint when unconfigured
- Rerank endpoint passes through to llama-server's native `/v1/rerank` (b3580+); Cohere-shape response
- SSE streaming with proper client-disconnect handling (no goroutine leaks; bounded drain on cancel)
- Tool / function calling (pass-through for capable models)
- Vision (image input) on multimodal models — `image_url` content blocks on `/v1/chat/completions` route through the Ollama engine path
- Structured output (JSON schema)
- `model=auto` smart routing
- **Response cache** — embeddings cached against a sha256 of the canonicalized request body (object keys sorted; ephemeral fields stripped). Two drivers: in-memory LRU (default) and SQLite-backed (persists across leader restart). Per-request opt-out via `Cache-Control: no-cache` / `no-store`; per-tenant scoping via `flock.cache.namespace` body field. `X-Flock-Cache: hit | miss` response header.
- Typed `engine_unreachable` errors with engine name, endpoint, and start-hint (e.g. `ollama serve`) when the upstream engine isn't responding
- Engine health watchdog on auto-spawned engines (force-restart after 3 consecutive failures, covers hung llama-server)
- LoRA adapter hot-loading (planned)
- Chat completion caching with streaming replay + semantic cache (planned)

### Cluster

- Auto-discovery — a node joins by running one command with a token
- Auto-placement — scheduler picks which node(s) host which model
- **Memory lifecycle** — admission control against live engine residency (a machine is never overcommitted), `flock model load --swap` with LRU evict-and-drain, `--pin` to protect a model, desired placements restored on restart, `flock down` releases engine memory by default, `--exclusive` for one-model-per-machine
- Heterogeneous sharding via llama.cpp RPC for models larger than any single node — `flock shard create <model> <N>` orchestrates the coordinator + every rpc-server end-to-end
- Live model migration (planned)
- Cross-platform workers: Mac (MLX), Linux+NVIDIA (vLLM), Linux+AMD (vLLM ROCm — planned), CPU (llama.cpp fallback)
- HA leader (planned)

### Multi-tenancy

- Per-user API keys with revocation, scopes (admin / user / node), and **TTL expiry** (`--ttl 7d`, `--expires-at 2026-07-01`, `flock token renew/expire`)
- Daily token quotas per key with usage metering
- **Per-key RPM + TPM rate limits** — leaky-bucket admission control; HTTP 429 with `Retry-After` + `X-RateLimit-Limit/Remaining/Reset-*` headers (OpenAI shape). Reconciles upfront token estimate against actual completion tokens after the response.
- **Per-key dollar + token budgets** — multiple budgets compose with AND semantics (`$10/day AND $100/month AND 1M tokens/day`). Windows: `day` / `week` / `month` (UTC). HTTP 429 `budget_exceeded` with `X-Flock-Budget-Reset-At` + audit row.
- **Per-call $ cost tracking** — every usage row stores a `cost_usd` snapshot computed at write time from a built-in vendor pricing table (current Claude + OpenAI rates) or catalog-override fields. `flock usage --summary` shows $ spent; `/admin/v1/usage/breakdown` aggregates by user/model/protocol.
- **Per-key model allowlist** — pin a key to specific model ids (or vendor families via `claude-*` / `gpt-*` globs); unauthorized models return 403 `model_not_allowed` and the refusal is audit-logged
- Standard `X-RateLimit-*` headers on every `/v1/*` response + always-on `X-Flock-Request-Id` correlation token (also embedded in audit rows for traceability)
- Audit log of every admin mutation + middleware refusals (`model_not_allowed`, `budget_exceeded`, `router.override`, `guardrail.block`)
- OIDC login for the web UI (Google, GitHub, Okta) — **planned**; the UI currently uses a pasted admin key

```bash
flock token create alice --models qwen-coder-7b,qwen3-14b   # restrict at creation
flock token create bob   --models 'claude-*,gpt-*'          # vendor families via glob
flock token create dave  --rpm 60 --tpm 100000 --ttl 30d    # rate-limited + expiring
flock token budget add k_abc --window month --limit 100 --unit usd  # $100/month cap
flock token edit k_abc --add-model gpt-4o-mini              # extend
flock token edit k_abc --remove-model qwen3-14b             # tighten
flock token renew k_abc --ttl 30d                           # extend expiry
```

### Hybrid local + cloud

- Built-in egress adapters for Anthropic + OpenAI; vendor model IDs (`claude-*`, `gpt-*`) transparently proxy upstream when `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` is set
- **7 OpenAI-compatible hosted gateways** — `openrouter/<model>`, `groq/<model>`, `together/<model>`, `fireworks/<model>`, `cohere/<model>`, `mistral/<model>`, `perplexity/<model>` route to the matching vendor (set the corresponding `*_API_KEY`). The slash-namespaced prefix is stripped before forwarding so the upstream sees its native id (e.g. `openrouter/anthropic/claude-3-haiku` → `anthropic/claude-3-haiku` on OpenRouter; `mistral/mistral-large-latest` → `mistral-large-latest` on Mistral). Vendor pricing entries seeded for the headline models on each.
- Failure-based fallback chain: any catalog entry can declare `fallback: [next-id, …]` and the router will try the chain in order on engine errors, 503s, or timeouts (transparent to the client)
- **Typed fallback chains** — catalog entries can declare `fallback_on_context_length` (prompt too long → long-context variant) and `fallback_on_content_policy` (vendor refused → permissive open-weight). The router classifies the primary's error (sentinel `errors.Is` then heuristic substring) and switches the rest of the chain to the matching typed list. Generic `fallback:` is the default when no typed list matches.
- **Per-request overrides** — clients can override the catalog chain for a single call. Body block (`flock.fallbacks`, `flock.num_retries`, `flock.retry_backoff_ms`, `flock.hedge`) or `X-Flock-*` headers; the router walks the request chain instead of the catalog one and retries each candidate with exponential backoff (cap 5 retries, 5 s backoff). Traces tag `flock.fallback.source = catalog | request` so operators can see who's overriding policy.
- **Request hedging** — opt-in per-request (`router.hedge_replicas: 2` in config + `flock.hedge: true` or `X-Flock-Hedge: 1` per call) fires the request to the top-N least-loaded workers concurrently and returns whichever stream opens first; losers are cancelled. Tail-latency win at the cost of ~2× engine load.
- **Sticky sessions** — when `router.sticky_session_ttl_seconds > 0`, the router pins (user_id, model) to its last worker so multi-turn chats reuse the same node's KV cache. Falls through when the pinned node is in cooldown or stale.
- **Placement cooldown (circuit breaker)** — after `router.placement_allowed_fails` consecutive engine errors, a worker is parked for `placement_cooldown_seconds`. `pick()` skips it until expiry; a single success after expiry resets the counter. Dashboard's Nodes tab shows a 🚫 cooldown badge with seconds remaining.

  ```bash
  curl -s http://localhost:8080/v1/chat/completions \
    -H "Authorization: Bearer sk-orc-..." \
    -H "X-Flock-Num-Retries: 3" \
    -H "X-Flock-Hedge: 1" \
    -d '{
      "model": "qwen3-14b",
      "messages": [{"role":"user","content":"hi"}],
      "flock": {"fallbacks": ["qwen3-8b", "llama-3.2-3b"], "retry_backoff_ms": 250}
    }'
  ```

- **AWS Bedrock**: SigV4 signing for `anthropic.*` models (non-streaming). Streaming body translation for other families pending.
- **GCP Vertex**: ADC auth probe wired. Body translation for `generateContent` pending.

### Policy + content checks

- **Guardrails framework** — `observability.guardrails` in `config.yaml` chains synchronous content checks against external services before the engine sees the request. Drivers: `webhook` (today; works as a thin shim for Presidio + Bedrock Guardrails + custom in-house policy). Modes: `pre` (block / rewrite / flag), `logging_only` (observe). On block: HTTP 403 `guardrail_blocked` with the guardrail name + reason; audit row recorded. `fail_open: true|false` chooses Allow vs Block on guardrail unreachable.

  ```yaml
  observability:
    guardrails:
      - name: redact-pii
        kind: webhook
        mode: pre
        url: "http://presidio.lan:8080/v1/check"
        fail_open: false
  ```

- **Observability callbacks** — usage + audit events fan out to external sinks. Drivers: `webhook` (HMAC-SHA256 signed payloads), `langfuse` (maps usage to `generation-create` against `/api/public/ingestion`). Each sink runs on its own goroutine with a bounded queue — a slow receiver is non-blocking on the hot path; overflow events are dropped and counted on `flock_callback_sent_total{outcome=dropped}`. Admin `GET /admin/v1/callbacks` lists sinks; `POST /admin/v1/callbacks/test[?sink=name]` fires a synthetic event for wiring verification.

  ```yaml
  observability:
    callbacks:
      - kind: webhook
        url: "https://hooks.example.com/flock"
        events: [usage, audit]
        secret: "${WEBHOOK_SECRET}"
      - kind: langfuse
        public_key: "${LANGFUSE_PUBLIC_KEY}"
        secret_key: "${LANGFUSE_SECRET_KEY}"
  ```

### Observability

- Prometheus metrics endpoint (`/metrics`) — per-model RPS, latency, tokens, errors
- Per-call usage records (model, protocol, tokens, latency, outcome) via `flock usage` and the Usage tab
- Admin audit log via `flock audit` and the Audit tab
- Reference Grafana dashboards in [`dashboards/`](dashboards/) — `cluster-overview.json`, `per-model.json`, `per-node.json`. Import any of them into Grafana 10+ and point at your Prometheus scrape of Flock's `/metrics`.
- OpenTelemetry / OTLP traces. Set `observability.otlp_endpoint` (or `FLOCK_OTLP_ENDPOINT`) to your collector — e.g. `http://localhost:4318` — and Flock emits a full span hierarchy per request: `http.request` → `router.Chat` (covers the whole stream) → `router.Chat.attempt` (one per fallback retry) → `<engine>.Chat` (engine call with prompt/completion token counts). All four engine drivers (ollama, vllm, mlx, llamacpp) export the same span shape. W3C `traceparent` propagation is always on so Flock participates correctly between two services that both export. Empty endpoint = no-op (zero overhead beyond the NoopTracerProvider).

### Developer experience

- One-line install (`curl | sh`)
- One-line model add (`flock model add qwen3.6-27b`) with a real progress bar and `--dry-run` preview
- One-line client config (UI generates per-tool snippets)
- Interactive picker for `flock model add|info|remove` and `flock connect` — no need to memorize IDs
- Shell completion for bash / zsh / fish (`flock completion <shell>`)
- Sensible defaults, no required flags
- Embedded web UI — no separate frontend to deploy

---

## Supported models

> **For the complete per-model walkthrough** (system requirements, performance per platform, install + use snippets for every client) see **[MODELS.md](MODELS.md)**.

Flock ships a curated catalog of **37 open-weight models** in `catalog/*.yaml`, spanning everything from 1 B edge models to 1 T-parameter sharded frontier MoE. Any other model also works via `flock model add hf:<owner>/<repo>` (HuggingFace direct) or `flock model add ollama:<name>` (any Ollama-pullable tag). See [catalog/README.md](catalog/README.md) for the YAML schema if you want to PR an entry.

> 📋 **Picker table — what to install** — full table with size, RAM, chat/code/reasoning/vision/audio/context ratings and license per model: **[MODELS.md → Picker table](MODELS.md#-picker-table--what-to-install)**.

### Shipped catalog at a glance

| Tier | Models |
|---|---|
| **Edge (≤2 GB RAM)** | `llama-3.2-1b`, `llama-3.2-3b` |
| **Small / laptop (8-16 GB)** | `qwen-coder-7b`, `deepseek-r1-8b`, `lfm2.5-8b-a1b` ⭐, `qwen3-8b`, `mellum2-12b`, `mistral-nemo-12b`, `gemma4-12b` (multimodal), `qwen3-14b`, `qwen-coder-14b`, `phi-4-14b` |
| **Consumer big (16-32 GB)** | `gpt-oss-20b` ⭐, `qwen3.6-27b` ⭐, `gemma4-26b`, `qwen3-30b`, `qwen3-coder-30b`, `qwen-coder-32b` |
| **Single 80 GB GPU** | `llama-3.3-70b-sharded`, `gpt-oss-120b`, `llama-4-scout` (10M ctx, multimodal) |
| **Sharded frontier (≥128 GB combined)** | `step-3.7-flash-sharded` ⭐ (Apache-2.0), `deepseek-v4-flash-sharded`, `nemotron-3-ultra-sharded` (Mamba-MoE, 1M ctx), `glm-5.1-sharded`, `kimi-k2.6-sharded` |

⭐ = current top picks (June 2026).

Run `flock model search` to list everything live with sizes and capabilities, or `flock model info <id>` for one model's full spec. Add `--sort=released` for newest-first, `--since 2026-01-01` to filter by date, or `--json` for machine-readable output. `flock model ls`, `flock status`, `flock usage`, and `flock audit` also accept `--json`. Running any `flock model add|info|remove` or `flock connect` with no ID launches an interactive picker (type to filter; arrow keys to navigate). Output is colored when stdout is a TTY; set `NO_COLOR=1` (or `FLOCK_NO_COLOR=1`) to disable.

The dashboard at `http://localhost:8080` mirrors the CLI: persistent top-bar chips show role + engine reachability + node/model counts (polled every 5 s); the Home tab summarizes traffic (requests-per-minute sparkline, p50/p95/p99, error rate, top model, recent activity); the Models tab includes a filterable catalog browser with per-row install; Nodes / Models / Usage / Audit refresh live while their tab is active; and "Add a worker" generates a one-time join token with copy-pasteable install-and-join snippets.

The same aggregates are available from the CLI: `flock usage --summary` and `flock audit --summary` print the top-models / p50-p95-p99 / error-rate / sparkline view that the dashboard renders. Both also accept `--json`.

Engine reliability: when Flock auto-spawned the engine itself (`flock up` with `FLOCK_ENGINE=llamacpp`), a health watchdog polls every 30 s and force-restarts the process after three consecutive failures — so a hung `llama-server` no longer requires manual intervention. For user-managed engines (Ollama, vLLM) Flock leaves the process alone but `/v1/chat/completions` now returns a typed `engine_unreachable` error with the engine name, endpoint, and the exact command to start it (`ollama serve`, `mlx_lm.server …`, etc.) when the engine isn't responding.

### Proxied (paid APIs — shipped, works today)

When a request's model name matches one of these, Flock proxies to the upstream vendor with **your** API key (env-configured) and logs the call as usage like any other request:

- **Anthropic upstream**: any `claude-*` model id
- **OpenAI upstream**: `gpt-*`, `o1*`, `o3*`, `o4*` model ids

Routing logic lives in `internal/api/egress.go`; vendor detection in `internal/router/router.go`.

### Roadmap — model families not yet in catalog

These work today via `flock model add hf:owner/repo` but don't have curated YAML entries with hardware specs:

- **Larger general / agent models** — Qwen3-235B, MiniMax-M2.7, MiMo-V2 sharded variants — pending sharded YAML entries.
- **Speech / transcription** — `/v1/audio/transcriptions` not yet shipped.
- **Rerank** — `/v1/rerank` not yet shipped (capability declared in catalog schema for future use).

Shipped recently (don't fall in this list):
- **Vision (image input)** — `gemma4-12b`, `gemma4-26b`, `gemma4-31b`, `gemma4-e2b`, `gemma4-e4b`, `qwen3-vl-8b`, `qwen3-vl-32b`, `pixtral-12b`, `moondream3`, `mimo-vl-7b`, `llama-4-scout` all serve through `/v1/chat/completions` with `image_url` content blocks.
- **Embeddings (for RAG)** — `/v1/embeddings` is live; install `nomic-embed-text` and call it from any OpenAI-shape embedding client.
- **Audio (input)** — `mimo-audio`, `gemma4-e2b`, `gemma4-e4b` declare `audio` capability for future routing; today they serve as `chat` models.

---

## Supported clients

The web UI generates a copy-pasteable config snippet for each tool.

| Client | Protocol | Config |
|---|---|---|
| **Cursor** | OpenAI | Settings → Models → Override OpenAI Base URL |
| **Continue.dev** | OpenAI or Anthropic | `~/.continue/config.json` → `apiBase` |
| **Aider** | OpenAI | `aider --openai-api-base http://flock:8080/v1` |
| **Zed** | OpenAI | `language_models.openai_compatible.api_url` |
| **Cline / Roo Code** (VS Code) | OpenAI or Anthropic | Provider settings panel |
| **Claude Code** | Anthropic | `ANTHROPIC_BASE_URL` env var |
| **OpenAI Python SDK** | OpenAI | `OpenAI(base_url=…, api_key=…)` |
| **Anthropic Python SDK** | Anthropic | `Anthropic(base_url=…, api_key=…)` |
| **LangChain / LlamaIndex** | Either | `openai_api_base` or `anthropic_api_url` |
| **`qwen-code` / `OpenCode`** | Anthropic | Same as Claude Code |
| **curl** | Either | Direct |

---

## Hardware recommendations

### Solo / dev (1 node)

| Hardware | Models that fit | Good for |
|---|---|---|
| MacBook M2/M3, 16 GB | 3–7B Q4 | Autocomplete, learning |
| MacBook M3/M4 Pro, 24–36 GB | 7–14B Q4 | Real coding work |
| Mac Mini M4 Pro, 64 GB | up to 32B Q4 | Solo agent-grade |
| Linux + RTX 4090 (24 GB) | up to 32B AWQ | Solo agent-grade, batched |

### Team of ~10 (recommended)

| Role | Box | Cost |
|---|---|---|
| Big chat/agent model | Linux + 2× RTX 5090 (64 GB total), Threadripper, 128 GB RAM | ~$11k |
| Code completion #1 | Mac Mini M4 Pro 64 GB | ~$2k |
| Code completion #2 | Mac Mini M4 Pro 64 GB | ~$2k |
| Control plane | Mac Mini base / NUC | ~$1k |
| Network | 10 GbE switch + cables | ~$0.5k |
| **Total** | | **~$16k** |

Serves ~10 heavy users with headroom. Power draw ~300 W idle, ~900 W peak. Fits one 20 A circuit. Breaks even vs. typical Claude/GPT spend in ~5 months.

### Larger team / production

- 1× H100 80 GB or 2× A100 80 GB for the flagship model
- 2× Mac Mini for completion
- 1× dedicated control box

Serves 25–50 users comfortably.

---

## Installation

### Prerequisites — read first

Flock is a **gateway** — it doesn't include an LLM engine. You need one of:
- **Ollama** (recommended for most users; works on Mac + Linux + NVIDIA + CPU)
- vLLM (for NVIDIA GPUs at scale — Linux only)
- MLX-LM (for fastest perf on Apple Silicon)

> ⚠️ **Apple Silicon heads-up:** the Homebrew `ollama` formula is currently missing the internal `llama-server` binary — model inference fails with `500: llama-server binary not found`. Use the **cask** (`brew install --cask ollama`) or the official installer instead. The Flock installer detects this and warns you.

### macOS (Apple Silicon)

```bash
# 1. install Ollama (use cask, NOT plain `brew install ollama`)
brew install --cask ollama
open -a Ollama                      # starts the daemon

# 2. install Flock
curl -fsSL https://raw.githubusercontent.com/hadihonarvar/flock/main/installer/install.sh | sh

# 3. add the install dir to PATH if the installer says so, e.g.:
export PATH="$HOME/.local/bin:$PATH"

# 4. start Flock
flock up
```

### Linux (x86_64 or arm64)

```bash
# 1. install Ollama
curl -fsSL https://ollama.com/install.sh | sh
sudo systemctl enable --now ollama   # or just: ollama serve &

# 2. install Flock
curl -fsSL https://raw.githubusercontent.com/hadihonarvar/flock/main/installer/install.sh | sh

# 3. add install dir to PATH if needed
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.bashrc
source ~/.bashrc

# 4. start Flock
flock up
```

### What the installer does

1. Detects your OS + architecture (must be macOS/arm64, Linux/x86_64, or Linux/arm64)
2. Checks for required shell tools (curl, tar)
3. Checks whether Ollama is installed and warns with the install command if not
4. Detects the broken-Homebrew-ollama case on macOS and tells you how to fix it
5. Fetches the **latest release** binary from GitHub Releases
6. Verifies SHA-256 against `checksums.txt`
7. Installs to `~/.local/bin/flock` (or `/usr/local/bin/flock` with sudo)
8. Drops the bundled model catalog (`*.yaml`) into `~/.flock/catalog/` so `flock up` works without further setup
9. Prints next steps + tells you if PATH needs updating

### Installer flags (after `| sh -s --`)

```bash
--help                  show usage
--version <vX.Y.Z>      install a specific version
--install-dir <path>    install to a specific dir
--no-engine             skip the Ollama check
--dry-run               show what would happen, no writes
```

### Installer env vars (alternative to flags)

```bash
# pin a specific version (skips the GH API lookup — also avoids the 60/hr rate limit)
curl -fsSL https://raw.githubusercontent.com/hadihonarvar/flock/main/installer/install.sh \
  | FLOCK_VERSION=v1.14.0 sh

# install to a custom dir
curl -fsSL https://raw.githubusercontent.com/hadihonarvar/flock/main/installer/install.sh \
  | FLOCK_INSTALL_DIR=/opt/flock/bin sh

# skip the Ollama check (CI, custom engine setups)
curl -fsSL https://raw.githubusercontent.com/hadihonarvar/flock/main/installer/install.sh \
  | FLOCK_SKIP_ENGINE=1 sh
```

Install **and** join a cluster in one command:

```bash
curl -fsSL https://raw.githubusercontent.com/hadihonarvar/flock/main/installer/install.sh | \
    sh -s -- join https://leader.local:8080?token=<TOKEN>
```

### Upgrade / uninstall

```bash
# upgrade in place (no need to re-run the installer)
flock update              # downloads latest release, verifies SHA-256, swaps binary
flock update --check      # just check, don't install

# uninstall — remove binary, catalog, and data dir
rm -f ~/.local/bin/flock       # (sudo-installed? then /usr/local/bin/flock)
rm -rf ~/.flock                 # catalog + data + config (destructive)
```

### Build from source

```bash
git clone https://github.com/hadihonarvar/flock
cd flock
go build -o flock ./cmd/flock
./flock version
```

Requires Go 1.25+. See [ARCHITECTURE.md → Build from source](ARCHITECTURE.md#build-from-source) for cross-compile + release builds.

### System requirements

- **macOS** 13+ on Apple Silicon (M1 or newer). Intel Macs not tested.
- **Linux** x86_64 or arm64 (Ubuntu 22.04+, Debian 12+, Fedora 39+, RHEL 9+).
- **Linux + NVIDIA**: NVIDIA driver 535+ (for vLLM); CUDA installed via the standard NVIDIA repos.
- **RAM**: 8 GB minimum, 16+ GB recommended; whatever model you load needs to fit.
- **Disk**: 50 GB for the binary + configs + small model cache; 200+ GB if you'll cache 70B-class models.
- **Network**: outbound HTTPS to GitHub + HuggingFace for downloading.

### Troubleshooting installation

| Symptom | Cause | Fix |
|---|---|---|
| `curl: (22) … 404` from installer | No release yet for your platform | Check https://github.com/hadihonarvar/flock/releases ; specify `--version` if needed |
| `command not found: flock` after install | Install dir not on PATH | `export PATH="$HOME/.local/bin:$PATH"` in your shell rc |
| `flock up` works, but chat returns 502 `llama-server binary not found` | Homebrew `ollama` formula on Apple Silicon | `brew uninstall ollama && brew install --cask ollama` |
| `flock up` says "engine not reachable" | Ollama daemon not running | `ollama serve &` (Linux: `sudo systemctl start ollama`) |
| `Port 8080 in use` | Another process is using the port | `FLOCK_LISTEN=:8081 flock up` |
| `checksum MISMATCH` | Corrupt download or tampering | Re-run installer; if it persists, file a security report (see SECURITY.md) |
| GH API rate-limited during install | Anonymous GH API limit (60/hr) | Wait, or set `FLOCK_VERSION=v0.x.y` to skip the lookup |

---

## Configuration

Flock follows a strict "no config required for defaults" rule. Every flag has a sensible default. The config file is YAML at `~/.flock/config.yaml`, or use env vars (`FLOCK_LISTEN`, `FLOCK_DATA_DIR`, …).

### Minimal config (auto-generated on first `flock up`)

```yaml
# ~/.flock/config.yaml
listen: ":8080"
data_dir: "~/.flock"
auth:
  require_keys: true   # set false for local-only dev mode
```

The initial admin key is auto-generated on first `flock up` and printed to stderr — copy it then. There is no `auth.initial_admin_key` field; the key lives in the SQLite store, not the YAML.

### Full reference

Every field below is parsed by `internal/config/config.go`. Anything not in this list is silently ignored.

```yaml
listen: ":8080"                       # HTTP listen address (used by leader and workers)
external_url: ""                      # public URL printed in UI; empty → use listen addr
data_dir: "~/.flock"                  # root for state.db, models, logs
log_level: "info"                     # debug | info | warn | error
catalog_dir: ""                       # empty → built-in catalog/ directory

storage:
  type: "sqlite"                      # only sqlite ships today
  dsn: "~/.flock/state.db"
  models_dir: "~/.flock/models"

auth:
  require_keys: true                  # set false to disable API-key auth (dev only)

engine:
  preferred: "ollama"                 # ollama | vllm | mlx | llamacpp
  ollama_endpoint:   "http://127.0.0.1:11434"
  vllm_endpoint:     "http://127.0.0.1:8000"
  mlx_endpoint:      "http://127.0.0.1:8080"
  llamacpp_endpoint: "http://127.0.0.1:8089"   # llama-server (single-node or RPC coordinator) — port chosen to avoid Flock leader :8080 and worker :8081

router:
  default_model: ""                   # empty → auto-pick on first up
  sticky_sessions: true
  latency_fallback_p95_seconds: 0     # 0 = disabled. When >0, the router
                                       # walks the catalog `fallback:` chain
                                       # for a faster candidate FIRST whenever
                                       # the primary's recent p95 latency
                                       # exceeds this many seconds. Bet #1.
  fallback:
    enabled: false                    # true → forward unknown claude-*/gpt-* models to vendor
    anthropic_url: "https://api.anthropic.com"
    openai_url:    "https://api.openai.com"
    # Bedrock (AWS) — signed via aws-sdk-go-v2 using the standard AWS
    # credentials chain (env, shared config, instance role). v0.6 supports
    # the anthropic.* model family non-streaming; amazon.*/meta.*/mistral.*
    # return 501 (body translation arrives v0.7).
    bedrock_region: ""                # e.g. us-east-1
    # Vertex (GCP) — ADC auth probe wired; body translation for
    # generateContent lands v0.7. Set the project and a 501 with ADC
    # status returns until then.
    vertex_project:  ""               # GCP project id
    vertex_location: "us-central1"

observability:
  otlp_endpoint: ""                   # e.g. http://localhost:4318 — empty disables tracing (no-op overhead)

placement:                            # memory lifecycle for this node's local engine
  exclusive: false                    # true → one resident model per machine: every
                                      # load evicts all other non-pinned models first
  reserve_percent: 20                 # % of total RAM held back from the admission
                                      # budget (OS + engine overhead headroom)
  drain_timeout_seconds: 30           # max wait for in-flight requests before an
                                      # eviction unloads anyway
```

### Environment variables

| Var | Overrides |
|---|---|
| `FLOCK_LISTEN` | `listen` |
| `FLOCK_DATA_DIR` | `data_dir` |
| `FLOCK_LOG_LEVEL` | `log_level` |
| `FLOCK_EXTERNAL_URL` | `external_url` |
| `FLOCK_ENGINE` | `engine.preferred` |
| `FLOCK_OLLAMA_ENDPOINT` / `FLOCK_VLLM_ENDPOINT` / `FLOCK_MLX_ENDPOINT` / `FLOCK_LLAMACPP_ENDPOINT` | corresponding `engine.*_endpoint` |
| `VLLM_API_KEY` | bearer token sent to a vLLM server (no YAML equivalent) |
| `FLOCK_REQUIRE_KEYS` | `auth.require_keys` (truthy `1/true/yes`) |
| `FLOCK_DEFAULT_MODEL` | `router.default_model` |
| `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` | enables `router.fallback` for the matching vendor |
| `OPENROUTER_API_KEY` / `GROQ_API_KEY` / `TOGETHER_API_KEY` / `FIREWORKS_API_KEY` | enables passthrough to the matching OpenAI-compatible hosted gateway. Models named `openrouter/<id>` / `groq/<id>` / etc are routed; the prefix is stripped before forwarding |
| `COHERE_API_KEY` / `MISTRAL_API_KEY` / `PERPLEXITY_API_KEY` | passthrough to Cohere / Mistral La Plateforme / Perplexity. Models named `cohere/<id>` / `mistral/<id>` / `perplexity/<id>` are routed |
| `FLOCK_CATALOG_DIR` | `catalog_dir` — overrides catalog lookup. Default search order: `$FLOCK_CATALOG_DIR` → `./catalog` → `<exe-dir>/catalog` → `~/.flock/catalog` (curl installer) → `/usr/local/share/flock/catalog` → `/usr/share/flock/catalog` (.deb/.rpm) |
| `FLOCK_OTLP_ENDPOINT` | `observability.otlp_endpoint` (OTLP/HTTP collector URL or bare `host:port`) |
| `FLOCK_COORDINATOR_NODE` | which node hosts the `llama-server` coordinator for sharded models; `local` forces leader, otherwise a node id. Default: highest-RAM worker. |
| `FLOCK_REJECT_BEARER` | set to `1` on a worker to refuse the bearer-fallback auth path and require HMAC for every `/v1/process/*` call. Use once every leader is on v0.5+. |
| `FLOCK_BEDROCK_REGION` | `router.fallback.bedrock_region` — enables Bedrock with real SigV4 signing for the anthropic.* family (v0.6); other families return 501 |
| `FLOCK_VERTEX_PROJECT` | `router.fallback.vertex_project` — wires ADC auth check; body translation lands v0.7 |
| `FLOCK_VERTEX_LOCATION` | `router.fallback.vertex_location` (default `us-central1`) |
| `FLOCK_LATENCY_P95_SECONDS` | `router.latency_fallback_p95_seconds` — when primary p95 exceeds this, prefer a faster fallback. 0 = disabled (default) |
| `FLOCK_EXCLUSIVE` | `placement.exclusive` (truthy `1/true`) — one resident model per machine |
| `FLOCK_PLACEMENT_RESERVE_PERCENT` | `placement.reserve_percent` — RAM held back from the admission budget (default 20) |
| `FLOCK_PLACEMENT_DRAIN_TIMEOUT_SECONDS` | `placement.drain_timeout_seconds` — eviction drain bound (default 30) |
| `FLOCK_UNLOAD_ON_EXIT` | `1` → Ctrl-C of `flock up` also unloads engine-resident models (the `flock down` path already does this by default) |
| `FLOCK_SKIP_SOURCE_CHECK` | `1` → skip the pre-flight HEAD probe that `flock model add` runs against the upstream registry (use for air-gapped mirrors / custom registries) |

### Not yet configurable (roadmap)

These features are mentioned elsewhere in this README but have no YAML knob today. The list is here so you don't waste time guessing.

- **Mesh backend selection** — only the LAN backend ships in v0.4. The `tailscale` (tsnet) backend has an interface defined in `internal/mesh/` but no implementation. Tracked in [ROADMAP.md](ROADMAP.md).
- **OIDC for the UI** — `internal/auth/` ships API keys only. The UI uses a pasted admin key for now.
- **Scheduler policy / replication / drain timeout** — `internal/scheduler/` ships sharding orchestration only; placement is naive least-loaded with no tunables.
- **Per-model fallback routing** — the fallback chain is all-or-nothing today (any unknown `claude-*` → Anthropic, any unknown `gpt-*` → OpenAI). Per-model whitelists are not parsed.
- **Observability endpoints / OTLP** — Prometheus is hardcoded to the main `/metrics` endpoint; no OTLP exporter, no separate Prometheus listener.
- **Per-node config (`~/.flock/node.yaml`)** — not read. Workers inherit engine endpoints from the leader's config or their own env vars.

### Per-node engine override

Workers run their own engine binary. To point a worker at a non-default endpoint, set env vars before `flock join`:

```bash
FLOCK_ENGINE=vllm FLOCK_VLLM_ENDPOINT=http://127.0.0.1:8000 flock join http://leader:8080?token=...
```

---

## Cluster operations

### Start the leader

```bash
flock up
```

Idempotent. Re-running it shows status if already running.

### Add a node

1. From the leader: click **Add Node** in the UI, or run `flock token create --node`
2. On the new machine: `curl -fsSL https://raw.githubusercontent.com/hadihonarvar/flock/main/installer/install.sh | sh -s -- join <leader-url>?token=<token>`

The token is a single-use, time-limited JWT that includes the tailnet auth key. The new node joins the mesh, registers with the leader, and waits for a model assignment.

### Remove a node

```bash
flock node drain <node-id>   # gracefully migrate models off
flock node remove <node-id>  # forget it
```

### End-to-end multi-node walkthrough

For a leader + one worker on the same LAN:

```bash
# === on the leader machine ===
brew install --cask ollama          # working Ollama (not the broken formula)
ollama serve &
flock up                            # bootstraps admin key, starts gateway on :8080
flock model add llama-3.2-3b        # pulls on the leader's Ollama
flock token create --node           # prints the worker join token

# === on the worker machine ===
brew install --cask ollama
ollama serve &
flock join http://<leader-host>:8080?token=<token>   # registers + starts worker HTTP server
flock model add qwen-coder-7b        # pulls on the worker's Ollama (reported back via heartbeat)

# === back on the leader ===
flock node ls                        # both nodes visible
# requests for "llama-3.2-3b" stay local
# requests for "qwen-coder-7b" get proxied to the worker automatically

# === from your laptop ===
curl http://<leader-host>:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-orc-..." \
  -d '{"model":"qwen-coder-7b","messages":[{"role":"user","content":"hi"}]}'
# served by the worker, transparently
```

### Sharded models (split one brain across multiple machines)

For a model too large to fit on any single machine, Flock can split it across N workers using `llama.cpp`'s RPC backend. Flock orchestrates the whole thing — no SSHing into each box.

**Prereqs:**
- `brew install llama.cpp` on the leader (provides `llama-server` for the coordinator).
- `rpc-server` on PATH on every worker that will host a shard. (At time of writing this binary needs a source build of llama.cpp with `cmake --preset rpc`; the Homebrew bottle doesn't include it yet.)
- A catalog entry with `sharding.required: true` and `source.path` pointing at a local GGUF file the leader can read (see `catalog/llama-3.3-70b-sharded.yaml`).
- N workers already joined and `ready` (`flock node ls`).

**One command on the leader:**

```bash
flock model add llama-3.3-70b-sharded
# auto-detects sharding.required=true → delegates to `flock shard create`

# or explicitly:
flock shard create llama-3.3-70b-sharded 2
```

What Flock does:

1. Picks the 2 workers with the most free RAM
2. Sends `POST /v1/process/start` to each worker → launches `rpc-server -p 50052`
3. Waits for both rpc-servers to be TCP-reachable (readiness probe)
4. On the leader, launches `llama-server -m <gguf> --rpc <worker1>:50052,<worker2>:50052 --port 9001`
5. Waits for the coordinator to be reachable
6. Persists shard rows + a `placements` row pointing the model at the local coordinator
7. The Router routes any request for `llama-3.3-70b-sharded` to the coordinator, which fans out to the rpc-server shards internally

**Manage from the CLI or web UI:**

```bash
flock shard ls                              # show every shard + coordinator
flock shard remove llama-3.3-70b-sharded    # stops coordinator + every rpc-server, deletes rows
```

Or open `http://leader:8080` → **Shards** tab → "Create sharded model" form + per-model "Tear down" buttons.

**Caveats (v0.4):**
- Shard crash recovery is automatic for up to 5 restarts with exponential backoff (1s, 2s, 4s, 8s, 16s). After that the process enters `crashloop` state and the admin must intervene — typically by re-running `flock shard create`. Both `rpc-server` and the `llama-server` coordinator restart this way. See `internal/agent/supervisor.go`.
- Coordinator always runs on the leader.
- Worker bin-packing is naive (descending free-RAM); doesn't factor GPU memory or current load.

### List nodes

```bash
flock node ls
# ID            HOSTNAME      HARDWARE          ENGINE   MODEL              STATE
# n_abc123      mac-mini-1    M4 Pro / 64 GB    mlx      qwen-coder-14b     ready
# n_def456      gpu-tower     2× RTX 5090       vllm     qwen3-72b          ready
# n_ghi789      lab-mac       M2 Pro / 32 GB    mlx      —                  idle
```

### Inspect a node

```bash
flock node show n_abc123
```

Shows: hardware specs, current models, recent requests, error log, resource utilization.

---

## Managing models

### Browse the catalog

```bash
flock model search coding
flock model search vision
```

### Add a model

```bash
flock model add qwen3-coder                      # from catalog
flock model add hf:Qwen/Qwen3-72B-AWQ            # from HuggingFace (scheme prefix)
flock model add ollama:phi3:mini                 # any Ollama tag
flock model add file:/abs/path/my-finetune.gguf  # pre-downloaded GGUF
flock model add --from ./my-model.yaml           # from a user-supplied catalog YAML
```

**Four ways to install a model:**

1. **Curated catalog** — the 37 entries in `catalog/*.yaml`. Hardware-floor checks apply.
2. **Scheme prefix** (`hf:` / `ollama:` / `file:`) — one-liner for anything the engine supports; no hardware check.
3. **`--from <my.yaml>`** — install from a user-written catalog YAML. The file is copied into `~/.flock/catalog/` so it persists and shows up in `flock model search` / `info` next run.
4. **Drop-in dir** — write a YAML to `~/.flock/catalog/<id>.yaml` directly, then `flock model add <id>` treats it like a built-in entry. Same schema as `catalog/*.yaml` (`id`, `display_name`, `source.{type,repo,ollama_name,path}`, `hardware`, `capabilities`).

This:
1. Checks `catalog/<id>.yaml`'s `hardware.min_ram_gb` (and `min_vram_gb`) against the cluster — installs that overshoot the floor are refused with a clear error. Pass `--force` to override (e.g. when you know swap or a quantization knob will save you).
1. **Verifies the upstream exists** — a 5s HEAD against the Ollama registry / HuggingFace. A typo'd `hf:owner/repo` or renamed tag is refused immediately with the URL that 404'd, instead of "succeeding" and failing later at engine launch. Network trouble only warns (never blocks); `FLOCK_SKIP_SOURCE_CHECK=1` for air-gapped mirrors. `--dry-run` shows the same check as a "Source check" row.
2. Records the model in the registry
3. Picks the best node(s) to host it (or shards across multiple)
4. Pulls the weights to those nodes (with resume support)
5. Launches the right inference engine
6. Flips the gateway routing to make the model available

### List active models

```bash
flock model ls
# MODEL              NODES                   STATE    REQUESTS/MIN   TOK/S
# qwen-coder-14b     n_abc123, n_ghi789      serving  4.2            42
# qwen3-72b          n_def456                serving  1.1            68
```

### Load, unload, and pin (memory lifecycle)

Installed ≠ resident: a model on disk loads into RAM on its first request. The
memory commands control what occupies RAM *right now*, with admission control
so a machine is never overcommitted:

```bash
flock model ps                          # what's resident + free memory budget
flock model load qwen-coder-14b         # bring into RAM now; REFUSES if it doesn't fit
flock model load qwen-coder-14b --swap  # evict least-recently-used models to make room
flock model load nomic-embed-text --pin # pinned: never evicted, no idle TTL
flock model unload qwen-coder-14b       # drain in-flight requests, then release RAM
```

How it works:

- **Admission** checks the model's footprint (weights + ~20% overhead) against
  live engine residency (Ollama `/api/ps`) and this machine's RAM budget
  (total minus a 20% OS reserve, tunable via `placement.reserve_percent`).
- **`--swap`** evicts only as many models as needed, least-recently-used first
  (from the usage log), never pinned ones. Victims are **drained** — the router
  stops sending them requests and in-flight ones finish (up to
  `placement.drain_timeout_seconds`, default 30) — then unloaded and
  audit-logged (`model_evicted`). Evicted models stay installed and reload on
  demand.
- **Loaded/pinned models are remembered** (desired placements in SQLite) and
  restored in priority order on the next `flock up`.
- **`flock up --exclusive`** (or `placement.exclusive: true`, or
  `FLOCK_EXCLUSIVE=1`) enforces one resident model per machine: every load
  evicts all other non-pinned models first. Good for single-GPU boxes.
- **`flock down` releases memory by default** — it's a deliberate teardown.
  Ctrl-C of `flock up` keeps models warm for fast dev restarts (Ollama's
  ~5-min idle TTL frees them anyway); opt into immediate release there with
  `--unload-on-exit`.

> Residency reporting requires Ollama today. Other engines degrade gracefully:
> admission still refuses over-budget models, and Flock-spawned llama-server
> processes are killed (memory freed) on shutdown by the process supervisor.

### Remove a model

```bash
flock model remove qwen-coder-14b
```

### Add a LoRA adapter (planned, v0.5)

LoRA adapter loading (`flock model adapter add`) is on the roadmap; see TASKS.md.

---

## Connecting clients

You have **three ways** to wire up a tool: the CLI, the dashboard, or copy-paste from the snippets below. All three produce the same config — they all invoke the same `internal/control/` code path.

### Fastest: `flock connect <client>`

```bash
flock connect claude-code                          # Anthropic-shape: Claude Code, qwen-code, hermes
flock connect cursor                               # OpenAI-shape: Cursor, Aider, Zed, OpenClaw, Codex CLI, …
flock connect hermes                               # Nous Research's CLI agent w/ persistent memory
flock connect open-webui                           # self-hosted ChatGPT-style web UI (Docker)
flock connect open-notebook                        # OSS NotebookLM clone (sources → chat + podcast)
flock connect goose                                # Block's OSS terminal agent
flock connect plandex                              # terminal-native agentic planner (MIT)
flock connect openhands                            # autonomous coding agent (formerly OpenDevin)
flock connect codex-cli                            # OpenAI's official CLI
flock connect opencode                             # terminal coding agent w/ per-provider baseURL
flock connect --list                               # full client roster (19 today)

# Overrides
flock connect cursor --model qwen-coder-14b        # suggest a specific model
flock connect aider --base-url https://flock.lan   # override gateway URL
FLOCK_TOKEN=sk-orc-… flock connect aider           # use a non-default token
flock connect aider --token sk-orc-…               # same, via flag
```

Anything that speaks OpenAI or Anthropic's API shape connects with one line. The full roster today: **claude-code**, **cursor**, **aider**, **continue**, **zed**, **cline**, **qwen-code**, **hermes**, **openclaw**, **opencode**, **open-webui**, **open-notebook**, **goose**, **plandex**, **openhands**, **codex-cli**, **openai-sdk**, **anthropic-sdk**, **curl**.

Token comes from `--token`, then `$FLOCK_TOKEN`, then `~/.flock/admin.key` (written when you ran `flock up`). Base URL comes from `--base-url`, then `external_url` in `~/.flock/config.yaml`, then `http://localhost:<listen>`.

### Reversing: `flock disconnect <client>`

```bash
flock disconnect claude-code        # prints the unset + sk-ant-… export commands
flock disconnect cursor             # GUI steps to clear the override
flock disconnect --list             # same 19 clients
```

Prints the exact commands to roll back whatever `flock connect` set up — does NOT modify any shell, editor, or config file. You run the commands when you're ready. Once disconnected, the client talks straight to the vendor (`api.anthropic.com`, `api.openai.com`); nothing about your Flock host needs to change. Re-run `flock connect <client>` anytime to go back.

### For a teammate: `flock invite <name>`

```bash
flock invite hadi --quota 100000
# Creates a user-scope token with a 100k tokens/day cap.
# Prints a paste-into-Slack markdown card with snippets for every supported client.
# Recipient picks the tool they use and pastes — done.

# Filter the share card to specific clients
flock invite alice --clients claude-code,cursor,curl

# Suggest a specific default model in the snippets
flock invite bob --model qwen-coder-14b

# Override the gateway URL printed in the card (useful behind a reverse proxy)
flock invite carol --base-url https://flock.example.com

# Machine-readable output for scripting
flock invite dave --format json | jq '.token'
```

Flags: `--quota N` (daily token cap, 0 = unlimited), `--clients id1,id2,…` (subset of clients to include), `--format markdown|json`, `--base-url <url>`, `--model <id>`. The token is shown exactly once — capture it then. Revoke later with `flock token revoke <id>`.

### In the dashboard

Open `http://localhost:8080` after `flock up`. Tabs:

- **Connect** — pick a tool from a dropdown, copy the snippet, click "Test connection" to verify the gateway works end-to-end
- **Playground** — in-browser chat box: pick a model, send a message, see the streaming response. Useful sanity check before configuring Cursor.
- **Tokens → + Invite teammate** — same as `flock invite`, with a modal that copies the share card as markdown.

### Reference snippets (manual)

If you can't run `flock connect`, the snippets below are the same content you'd get from the CLI. Substitute your own base URL + token where shown.

### Cursor

Settings → Models → Add Model:
- Name: `flock`
- Provider: OpenAI Compatible
- Base URL: `http://flock.your-tailnet.ts.net/v1`
- API Key: `sk-orc-…`

### Claude Code

```bash
export ANTHROPIC_BASE_URL=http://flock.your-tailnet.ts.net
export ANTHROPIC_AUTH_TOKEN=sk-orc-…
claude
```

Add to `~/.zshrc` or `~/.bashrc` to make permanent.

### Continue.dev

`~/.continue/config.json`:

```json
{
  "models": [
    {
      "title": "Flock - Qwen3-Coder",
      "provider": "openai",
      "model": "qwen3-coder",
      "apiBase": "http://flock.your-tailnet.ts.net/v1",
      "apiKey": "sk-orc-…"
    }
  ]
}
```

### Aider

```bash
aider --openai-api-base http://flock.your-tailnet.ts.net/v1 \
      --openai-api-key sk-orc-… \
      --model openai/qwen3-coder
```

### OpenAI Python SDK

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://flock.your-tailnet.ts.net/v1",
    api_key="sk-orc-…",
)

resp = client.chat.completions.create(
    model="auto",
    messages=[{"role": "user", "content": "write a haiku about caching"}],
)
print(resp.choices[0].message.content)
```

### Anthropic Python SDK

```python
from anthropic import Anthropic

client = Anthropic(
    base_url="http://flock.your-tailnet.ts.net",
    api_key="sk-orc-…",
)

resp = client.messages.create(
    model="qwen3-coder",
    max_tokens=1024,
    messages=[{"role": "user", "content": "explain CRDTs"}],
)
print(resp.content[0].text)
```

---

## API reference

### OpenAI surface

| Method | Path | Notes |
|---|---|---|
| `POST` | `/v1/chat/completions` | Streaming + non-streaming; accepts `image_url` content blocks (Ollama path). Returns typed `engine_unreachable` errors with engine name + start hint when the upstream engine is down. |
| `POST` | `/v1/embeddings` | Ollama embedding models (e.g. `nomic-embed-text`) |
| `GET` | `/v1/models` | Lists available models |

(Planned: `/v1/completions`, `/v1/audio/transcriptions`, `/v1/rerank`.)

### Anthropic surface

| Method | Path | Notes |
|---|---|---|
| `POST` | `/v1/messages` | Streaming (SSE) + non-streaming |
| `POST` | `/v1/messages/count_tokens` | Pre-flight token count |

### Flock admin surface

| Method | Path | Notes |
|---|---|---|
| `GET` | `/healthz` `/readyz` | Liveness / readiness |
| `GET` | `/metrics` | Prometheus exposition |
| `GET` | `/admin/v1/nodes` | List nodes |
| `POST` | `/admin/v1/nodes/register` | (scope=admin or node) Worker registration |
| `POST` | `/admin/v1/nodes/heartbeat` | (scope=admin or node) Worker heartbeat with loaded models |
| `POST` | `/admin/v1/nodes/{id}/drain` | Mark node as draining |
| `DELETE` | `/admin/v1/nodes/{id}` | Forget a node |
| `GET` | `/admin/v1/models` | List installed models |
| `GET` | `/admin/v1/catalog` | List catalog entries |
| `POST` | `/admin/v1/models` | Install a model (auto-delegates to shard orch if `sharding.required`) |
| `DELETE` | `/admin/v1/models/{id}` | Uninstall (auto-handles sharded teardown) |
| `POST` | `/admin/v1/models/{id}/unload` | Drain in-flight requests, drop the model from engine RAM (weights stay; cleared from desired placements so it stays unloaded across restarts). Engines that don't support it return `status:"noop"` |
| `POST` | `/admin/v1/models/{id}/load` | Memory-aware load: `{swap, pin, priority}`. 200 = loaded (response lists any evictions); 409 `needs_swap` with the LRU victim list; 409 `blocked_by_pinned`; 422 `impossible` (over the node's budget even when empty) |
| `GET` | `/admin/v1/memory` | Live engine residency (per-model RAM/VRAM bytes via Ollama `/api/ps`), pins, priorities, free budget, exclusive flag |
| `GET` | `/admin/v1/tokens` | List API keys (no hash, no plaintext) |
| `POST` | `/admin/v1/tokens` | Create a key — returns plaintext ONCE |
| `DELETE` | `/admin/v1/tokens/{id}` | Revoke a key |
| `GET` | `/admin/v1/shards` | List shards across all models |
| `POST` | `/admin/v1/shards/create` | Orchestrate a sharded model |
| `DELETE` | `/admin/v1/shards/{model_id}` | Tear down a sharded model |
| `GET` | `/admin/v1/usage/recent` | Recent inference records |
| `GET` | `/admin/v1/usage/summary` | Aggregate stats (top models, p50/p95/p99, error rate, RPM sparkline) |
| `GET` | `/admin/v1/audit/recent` | Recent admin actions |
| `GET` | `/admin/v1/audit/summary` | Top actors + top actions |
| `GET` | `/admin/v1/config` | Effective config, secrets redacted |
| `GET` | `/admin/v1/status` | Compact role + engine reachability + node/model counts (powers dashboard top-bar chips) |
| `GET` | `/admin/v1/events` | Server-Sent Events stream. Push-on-change for `models` / `nodes` / `shards` topics. Sends a 25 s `keepalive` comment so proxies don't idle. Auth via Bearer or `?key=` query param. |

All admin endpoints require an admin key (`flock token create --admin`).

### Model routing rules

`model` field in the request determines backend:

| Model name | Routes to |
|---|---|
| exact catalog ID (`qwen3-coder`) | local cluster, that model |
| `auto` | local; gateway picks based on heuristics |
| `claude-…` | Anthropic API (proxied) |
| `gpt-…`, `o3`, `o4` | OpenAI API (proxied) |
| `hf:…` | local, if the model is loaded |

**Routing-sort suffixes** (OpenRouter-compatible): append `:floor` (cheapest first) or `:nitro` (highest tokens/sec first) to any local model id — `qwen3.6-27b:floor` walks that model's candidate chain ordered by price instead of catalog preference. Equivalent explicit forms: `flock.sort: "price" | "latency" | "throughput"` in the body or `X-Flock-Sort` header (explicit wins over the suffix). The suffix is stripped before the engine, usage records, and per-key allowlists see the name — an allowlist of `["x"]` authorizes `x:floor`. Price comes from the catalog + vendor pricing table (free local models always beat paid egress); latency/throughput come from each model's rolling p95 / median tokens-per-second over recent requests, so rankings warm up with traffic. Audit-logged like every routing override.

---

## CLI reference

Every admin action is available via the CLI **and** the web UI — full parity. Most subcommands launch an interactive picker (type to filter, ↑↓/enter) when called with no argument or an unknown ID, so you rarely need to memorize an ID.

```
# --- lifecycle (CLI only — UI can't kill the process running the UI) ---
flock up [--no-wizard] [--auto-pull=false] [--exclusive]
                                  Start the local node (first-run wizard picker
                                  installs a starter model unless --no-wizard is
                                  set; --exclusive = one resident model per machine)
flock down [--no-unload]          Stop the local node and release engine memory
                                  (--no-unload leaves models resident)
flock status [--json]             Show local + cluster status
flock join <url>?token=…          Join an existing cluster as a worker
flock doctor                      Diagnose common problems
flock update [--check]            Check / install the latest Flock release
flock upgrade                     Alias for `update`
flock completion <bash|zsh|fish>  Print a shell completion script
flock version                     Print version

# --- nodes ---
flock node ls                     List nodes
flock node show <id>              Inspect a node
flock node drain <id>             Drain a node (no new requests routed to it)
flock node remove <id> [--yes]    Forget a node (prompts unless --yes)

# --- models (non-sharded) ---
flock model search [q] [--sort=released] [--since YYYY-MM-DD] [--json]
                                  Search catalog with optional date filters
flock model ls [--json]           List installed models
flock model add <id> [--force] [--dry-run]
                                  Install a model. --dry-run previews size/RAM/
                                  engine/ETA without pulling weights.
flock model info <id> [--json]    Full details for one catalog model
flock model remove <id> [--yes]   Uninstall a model (prompts unless --yes)

# --- memory lifecycle (which models occupy RAM right now) ---
flock model ps [--json]           Resident models + RAM/VRAM sizes, pins, free budget
flock model load <id> [--swap] [--pin] [--priority N]
                                  Bring a model into engine RAM with admission
                                  control: refuses when it doesn't fit; --swap
                                  evicts least-recently-used models (drained
                                  first, audit-logged); --pin exempts from
                                  eviction + the engine's idle TTL. Loaded/pinned
                                  models are restored on the next `flock up`.
flock model unload <id>           Drain, then drop from engine RAM (weights stay
                                  on disk; stays unloaded across restarts)

# --- sharded models (one model split across N machines) ---
flock shard create <model> [N]    Orchestrate a sharded model across N workers
flock shard ls                    List shards across all sharded models
flock shard remove <model> [--yes]  Tear down a sharded model (prompts unless --yes)

# --- API keys / tokens ---
flock token create [name]         Issue an API key (--admin, --node)
flock token ls                    List API keys
flock token revoke <id>           Revoke a key

# --- observability ---
flock usage [--limit N] [--user X] [--summary] [--json]
                                  Recent inference records, or aggregate summary
                                  (top models, p50/p95/p99, error rate, sparkline)
flock audit [--limit N] [--actor X] [--summary] [--json]
                                  Recent admin audit entries, or top-actors/top-actions
                                  summary

# --- config ---
flock config show [--json]        Show effective runtime config (secrets redacted)
flock config path                 Print config file path
flock config edit                 Print the editor command for the config file
```

Output is colored when stdout is a TTY. Set `NO_COLOR=1` (or `FLOCK_NO_COLOR=1`) to disable. Top-level subcommand typos get a "did you mean ..." suggestion via Damerau-Levenshtein over the registered subcommand list.

---

## Web UI

The UI is shipped embedded in the Go binary via `//go:embed`. It is *not* a separate deployment. Open `http://localhost:8080` and paste the admin key.

All admin actions are also doable via CLI — see the [CLI reference](#cli-reference).

Persistent top-bar chips (every view) show: role (leader/worker), engine reachability, node count, model count — polled every 5 s. Most tabs subscribe to the `/admin/v1/events` SSE stream and re-fetch instantly when the relevant topic fires; a 15 s polling fallback runs underneath in case the stream drops (also pauses when the browser tab is hidden).

| Tab | Capabilities |
|---|---|
| **Dashboard (home)** | 4 KPI cards (nodes, models, requests, tokens served); latency card with p50/p95/p99; tier-colored error-rate card; top-model card; full-width SVG sparkline of requests-per-minute over the last 60 minutes; recent-activity strip (last 6 requests with outcome badges); copy-paste curl example |
| **Nodes** | List + status; **Add a worker** modal generates a one-time node-scope token and shows both an install-and-join curl one-liner and a `flock join` command for boxes that already have the binary; per-row **drain** and **remove** with confirmation |
| **Models** | **Engine memory card** (resident models with RAM sizes, pin badges, usage bar against the admission budget, per-row pin/unload); installed models table with per-row **test** (opens Playground pre-wired to the model), **load** (memory-aware; offers an LRU swap with a victim-list confirm when the model doesn't fit), **unload** (drain, then drop from engine RAM, keep weights on disk), and **remove** (confirmed; auto-handles sharded teardown) buttons; **filterable catalog browser** (search, sort by size/newest/id, hide-installed toggle, color-coded license badge, per-row Install button) |
| **Shards** | List shards grouped by sharded model; **Create sharded model** form (id + shard count); per-model **Tear down** button |
| **Tokens** | List API keys (id/name/scope/quota/status); **Create** form with name + scope (user/admin/node) + daily quota; **Revoke** button per row; new keys shown ONCE in a modal |
| **Usage** | Recent inference records: time, user, model, protocol, tokens, latency, outcome (live polling) |
| **Audit** | Recent admin actions with actor + action + target (live polling) |
| **Settings** | Read-only effective config with secrets redacted; instructions for editing `~/.flock/config.yaml` and the env vars (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `FLOCK_*`) |

Mutating actions surface results via a toast notification (bottom-right, 3 s auto-dismiss) instead of inline error sprawl.

Keyboard shortcuts (vim-style leader sequence; skipped while typing in any input):
`g d` Dashboard · `g c` Connect · `g p` Playground · `g n` Nodes · `g m` Models · `g h` Shards · `g t` Tokens · `g u` Usage · `g a` Audit · `g s` Settings · `?` help · `Esc` close modals. Click the `?` chip in the top bar for the same cheatsheet.

## CLI vs UI parity

Every cluster action is available both ways. Pick whichever fits your workflow:

| Action | CLI | UI |
|---|---|---|
| Add node | `flock token create --node` → `flock join <url>?token=…` on worker | Nodes tab → "Add node…" |
| Drain node | `flock node drain <id>` | Nodes tab → row's "drain" |
| Remove node | `flock node remove <id>` | Nodes tab → row's "remove" |
| Install model | `flock model add <id>` | Models tab → catalog picker → "Install" |
| Load model into RAM | `flock model load <id> [--swap] [--pin]` | Models tab → row's "load" (confirm dialog lists evictions) |
| Unload model from RAM | `flock model unload <id>` | Models tab → row's "unload" |
| View engine memory | `flock model ps` | Models tab → Engine memory card |
| Remove model | `flock model remove <id>` | Models tab → row's "remove" |
| Create sharded model | `flock shard create <model> [N]` | Shards tab → "Create sharded model" |
| Tear down sharded model | `flock shard remove <model>` | Shards tab → "Tear down" |
| Create API key | `flock token create <name>` | Tokens tab → "Create" form |
| Revoke API key | `flock token revoke <id>` | Tokens tab → row's "revoke" |
| View recent usage | `flock usage` | Usage tab |
| View audit log | `flock audit` | Audit tab |
| View effective config | `flock config show` | Settings tab |
| Edit config | edit `~/.flock/config.yaml`, restart | (read-only via UI; CLI shows the path) |

**The only thing that can't be done from the UI**: starting / stopping `flock up` itself — the UI is served by that process, so it can't safely tear itself down. Use `flock up` / `flock down` from the terminal.

---

## Troubleshooting

### `flock up` fails to start

```bash
flock doctor
```

Common issues:

- Port 8080 in use → set `listen: ":8081"` in config
- macOS firewall blocking mesh → System Settings → Privacy & Security → allow Flock
- Insufficient memory → pick a smaller model (`flock model add llama-3.2-3b`)

### A node won't join

- Token expired (5-minute TTL by default) — generate a fresh one in the UI
- Clock skew >5 minutes between leader and node — fix NTP
- Tailscale already running on the node — set `mesh.backend: lan` to use direct LAN

### Slow inference

- Check GPU utilization (`flock node show <id>`). If pinned at 100% under load: add a replica or upgrade.
- Sticky sessions disabled? Re-enable for better KV cache reuse.
- Model is CPU-falling-back? Check the leader's stderr where `flock up` is running — engine driver errors are logged there. Per-node log streaming is on the roadmap.

### Claude Code shows "model not found"

- Make sure the model ID in your request matches a local catalog ID, or one of the proxied vendor IDs.
- `flock model ls` to confirm what's loaded.

### Slow inference?

- Check engine reachability: `flock doctor`
- Add a node + install the model there: `flock node` / `flock model add` (router auto-load-balances)
- For sharded large models: `flock shard create`

---

## FAQ

**Can I run Claude or GPT on my hardware?**
No — those are closed-weight proprietary models. Flock proxies to their APIs when you ask for them, so they appear in the same endpoint, but inference happens at Anthropic/OpenAI and you pay per token.

**Do I need a GPU?**
For real coding work, yes — either an NVIDIA GPU on Linux or an Apple Silicon Mac. CPU-only works via llama.cpp for tiny models (3B and under) and is useful for testing only.

**Can I mix Macs and NVIDIA boxes in one cluster?**
Yes. That's a core design goal. The scheduler treats them as distinct pools and assigns models that fit each.

**Does Flock work without internet?**
Yes, after initial model download. The mesh requires a Tailscale coordination server reachable from each node for *joining*; once joined, traffic is direct. For air-gapped deployments, use Headscale (open-source Tailscale control server) or set `mesh.backend: lan`.

**How is this different from Ollama?**
Ollama is a great single-node inference engine. Flock is the *orchestration layer* across many machines. Flock uses Ollama as one of its supported engine backends.

**How is this different from vLLM?**
vLLM is a single-node inference server. Flock orchestrates vLLM (and others) across your fleet.

**How is this different from exo?**
exo is the closest project conceptually. Flock differs by: (1) Anthropic-API compatibility for Claude Code, (2) explicit hybrid local+vendor routing, (3) multi-tenant API keys / quotas / audit log (OIDC planned), (4) embedded UI and observability stack, (5) Go single-binary install.

**Does Flock train models?**
No. Use Axolotl / Unsloth / torchtune for training. Bring back a LoRA adapter; Flock will serve it.

**Why Go and not Rust?**
Go ships a static binary as fast as Rust for this workload, with a faster development loop. We may rewrite hot paths in Rust if measurements justify it.

**Is there a hosted version?**
Not initially. The product is the software you run.

**Can I use my own Tailscale account?**
Yes — set `mesh.tailnet_name` and `mesh.auth_key` to your tailnet. Otherwise Flock spins up a dedicated tailnet for the cluster.

**Does Flock support AMD GPUs?**
Linux + ROCm via vLLM-ROCm is on the roadmap.

**Can I run this on Windows?**
Workers no (no MLX, no native vLLM). Leader/CLI yes via WSL2. Native Windows isn't a near-term priority.

---

## Also known as / search terms

Flock is a **self-hosted LLM gateway** and **inference router**. If you found this repo searching for an alternative to a hosted service or a frontend for a local engine, the answer is yes:

- **OpenRouter alternative** (self-hosted) — same one-endpoint-for-many-models idea, but on your hardware with your keys.
- **LiteLLM alternative** (Go binary instead of Python) — same OpenAI + Anthropic protocol shim, plus multi-node routing.
- **Self-hosted Claude proxy / Claude Code proxy** — point `ANTHROPIC_BASE_URL` at Flock; serve local models or transparently proxy to real Anthropic per request.
- **Ollama frontend / multi-machine Ollama** — Flock orchestrates several Ollama (or vLLM / MLX-LM / llama.cpp) nodes behind one gateway with auth, quotas, and audit.
- **Private inference cluster / on-prem LLM gateway** — keep all inference on a trusted LAN or Tailscale; opt in to vendor fallback only when you choose.
- **Self-hosted Cursor / Aider / Continue backend** — drop-in OpenAI-compatible URL for IDE coding tools.
- **AI gateway with per-user keys + quotas + audit** for teams of 10-50 spending $30k+/yr on Claude / GPT.
- **Sharded inference orchestrator** — split a model larger than any single machine across multiple workers via `llama.cpp-RPC`.

Related concepts: local LLM, on-prem AI, private GPT, GGUF, multi-tenant inference, model placement, fallback chain, hybrid local + vendor.

---

## License

Apache License 2.0 — see [LICENSE](LICENSE).

You can use Flock commercially, modify it, fork it, embed it, redistribute it. The only requirements are (a) keep the license + notice, (b) state significant changes you made. No copyleft.

## Acknowledgments

Flock stands on the shoulders of:

- **vLLM** — for fast NVIDIA inference
- **MLX-LM** — for Apple Silicon inference
- **llama.cpp** — for the universal fallback
- **Ollama** — for proving the developer-experience bar
- **Tailscale** — for the mesh and the `tsnet` library
- **LiteLLM** — for cross-provider protocol translation
- **Hugging Face** — for the open-weight model ecosystem
- The teams behind **Qwen, Llama, DeepSeek, Mistral, GLM, Phi, Gemma, StarCoder** — for releasing open weights

---

**Project links**

- Website: https://flockllm.com
- GitHub: https://github.com/hadihonarvar/flock
- Maintainer: [Hadi Honarvar Nazari](https://www.linkedin.com/in/hadi-honarvar-nazari/) — `hadi.work.ca@gmail.com`
- Security disclosures: see [SECURITY.md](SECURITY.md)
