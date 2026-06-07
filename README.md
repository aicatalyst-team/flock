# Flock

> **Self-hosted AI for your team. One endpoint. Your hardware.**

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
      │   27 curated catalog models (Qwen 3.6,        Each request routed   │
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

### 🐧 Linux (x86_64 or arm64)

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
| **Status** | Alpha — build-verified on macOS/arm64; single-node verified end-to-end with curl; multi-node routing has in-process E2E coverage (`internal/controlplane/two_node_e2e_test.go`); real two-machine verification is the [10-min manual checklist](docs/TWO_NODE_VERIFICATION.md) |
| **License** | Apache 2.0 |
| **Language** | Go (orchestrator + embedded HTML UI) |
| **Platforms** | macOS (Apple Silicon), Linux (x86_64, arm64) |

## What's shipped

### Core (single-node, works today)

- ✅ Single binary (`go build ./cmd/flock` → 23 MB) — no Python or Docker required
- ✅ **OpenAI-compatible** API (`/v1/chat/completions`, `/v1/models`) — Cursor, Aider, Continue, Zed, OpenAI SDK
- ✅ **Anthropic-compatible** API (`/v1/messages`, `/v1/messages/count_tokens`) — Claude Code, Anthropic SDK
- ✅ Streaming (SSE) for both protocols, with proper client-disconnect handling (no goroutine leaks)
- ✅ **Hybrid fallback** — requests for `claude-*` or `gpt-*` transparently proxy to the real Anthropic / OpenAI API (set `ANTHROPIC_API_KEY` / `OPENAI_API_KEY`); protocol mismatch (e.g., Claude model on OpenAI route) returns a clear 400
- ✅ Engine drivers: **Ollama**, **vLLM**, **MLX-LM**, **llama.cpp** (incl. RPC mode)
- ✅ Engine endpoints + API keys configurable per engine via env (`FLOCK_VLLM_ENDPOINT`, `VLLM_API_KEY`, …)
- ✅ Hardware auto-detection (mac + linux + NVIDIA) and auto-pick a default model
- ✅ Catalog with curated model entries (Llama 3.2, Qwen2.5-Coder)

### Multi-node (cross-node routing — landed, untested with 2 real boxes)

- ✅ `flock token create --node` issues a worker join token
- ✅ `flock join <leader>?token=…` registers + starts a worker HTTP server bound to the LAN/tailnet address
- ✅ Workers run their own engine (Ollama / vLLM / MLX); leader proxies inference requests to them
- ✅ **Router** picks the right node per request: local-preferred if the model is loaded locally, otherwise least-loaded worker that has the model
- ✅ **Heartbeat carries loaded models** every 5s; leader reconciles the placements table automatically
- ✅ Agent handles auth errors gracefully (401 → exit, 404 → re-register, transient → exponential backoff)
- ✅ **Sharding auto-orchestration** — `flock shard create <model> <N>` picks N workers, launches `rpc-server` on each via the worker process-supervisor API, launches the coordinator `llama-server --rpc <list>` locally, registers the placement, and the Router routes requests to the coordinator transparently. Web UI exposes the same in the Shards tab.
- ✅ Process supervisor (`internal/agent/supervisor.go`) — Start/Stop/Logs with TCP-port readiness probe, used by the leader for the coordinator and by workers for rpc-server.
- ⚠️ Tailscale `tsnet` mesh backend — interface defined; LAN backend ships in v0.3

### Multi-tenant + observability

- ✅ Per-user API keys with scopes (admin / user / node), daily token quotas, audit log
- ✅ Usage metering — every request recorded with model/protocol/tokens/latency; metrics fire even in dev mode (no key required)
- ✅ Prometheus metrics at `/metrics`
- ✅ Embedded web UI (single HTML, Tailwind via CDN) — dashboard, nodes, models, usage, audit, settings
- ⚠️ OIDC for the UI — deferred to v0.4; UI uses pasted API key for now

### Release + ops

- ✅ GitHub Actions CI workflow
- ✅ GoReleaser config + release workflow (auto-builds darwin/linux × arm64/amd64, creates Homebrew formula)
- ✅ Homebrew formula template
- ✅ install.sh (`curl … | sh`) script — pulls latest from GH Releases when you tag one

### Verified to work

- ✅ `go build ./cmd/flock` — clean on go 1.22 / darwin-arm64
- ✅ `go vet ./...` — clean
- ✅ `flock up` boots, bootstraps admin key, starts gateway
- ✅ `flock up` → `curl /v1/models` returns the auto-picked model
- ✅ `curl /v1/chat/completions` reaches Ollama and translates errors back as proper OpenAI shape
- ⚠️ Actual model inference response — Homebrew's `ollama` formula on arm64 is broken (missing internal `llama-server` binary); use `brew install --cask ollama` or `curl -fsSL https://ollama.com/install.sh | sh` for a working Ollama install

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
✔ Installed flock v0.1.0
✔ Detected: Apple M3, 24 GB unified memory
✔ Started control plane on http://localhost:8080
✔ Mesh ready (tailnet: flock-7f3a)
✔ Auto-selected model: qwen2.5-coder:7b (fits in 24 GB)
✔ Downloading model... ████████████ 100%
✔ Ready.

  Web UI: http://localhost:8080
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

- OpenAI-compatible API (`/v1/chat/completions`, `/v1/completions`, `/v1/embeddings`, `/v1/models`, `/v1/audio/transcriptions`)
- Anthropic-compatible API (`/v1/messages`, `/v1/messages/count_tokens`)
- SSE streaming
- Tool / function calling (pass-through for capable models)
- Vision (image input) on multimodal models
- Structured output (JSON schema)
- `model=auto` smart routing
- Sticky sessions by user/session ID for KV cache reuse
- LoRA adapter hot-loading (planned)

### Cluster

- Auto-discovery — a node joins by running one command with a token
- Auto-placement — scheduler picks which node(s) host which model
- Heterogeneous sharding via llama.cpp RPC for models larger than any single node (planned)
- Live model migration (planned)
- Cross-platform workers: Mac (MLX), Linux+NVIDIA (vLLM), Linux+AMD (vLLM ROCm — planned), CPU (llama.cpp fallback)
- HA leader (planned)

### Multi-tenancy

- Per-user API keys with revocation and scopes (admin / user / node)
- Daily token quotas per key with usage metering
- Audit log of every admin mutation
- OIDC login for the web UI (Google, GitHub, Okta) — **planned**; v0.4 uses a pasted admin key

### Hybrid local + cloud

- Built-in egress adapters for Anthropic + OpenAI; vendor model IDs (`claude-*`, `gpt-*`) transparently proxy upstream when `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` is set
- Failure-based fallback chain: any catalog entry can declare `fallback: [next-id, …]` and the router will try the chain in order on engine errors, 503s, or timeouts (transparent to the client)
- Bedrock / Vertex / other cloud providers — **planned**

### Observability

- Prometheus metrics endpoint (`/metrics`) — per-model RPS, latency, tokens, errors
- Per-call usage records (model, protocol, tokens, latency, outcome) via `flock usage` and the Usage tab
- Admin audit log via `flock audit` and the Audit tab
- Reference Grafana dashboards in [`dashboards/`](dashboards/) — `cluster-overview.json`, `per-model.json`, `per-node.json`. Import any of them into Grafana 10+ and point at your Prometheus scrape of Flock's `/metrics`.
- OpenTelemetry / OTLP traces. Set `observability.otlp_endpoint` (or `FLOCK_OTLP_ENDPOINT`) to your collector — e.g. `http://localhost:4318` — and Flock emits one span per HTTP request via `otelhttp`, with W3C `traceparent` propagation. Empty endpoint = no-op (zero overhead).

### Developer experience

- One-line install (`curl | sh`)
- One-line model add (`flock model add llama3.3`)
- One-line client config (UI generates per-tool snippets)
- Sensible defaults, no required flags
- Embedded web UI — no separate frontend to deploy

---

## Supported models

> **For the complete per-model walkthrough** (system requirements, performance per platform, install + use snippets for every client) see **[MODELS.md](MODELS.md)**.

Flock ships a curated catalog of **27 open-weight models** in `catalog/*.yaml`, spanning everything from 1 B edge models to 1 T-parameter sharded frontier MoE. Any other model also works via `flock model add hf:<owner>/<repo>` (HuggingFace direct) or `flock model add ollama:<name>` (any Ollama-pullable tag). See [catalog/README.md](catalog/README.md) for the YAML schema if you want to PR an entry.

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

Run `flock model search` to list everything live with sizes and capabilities, or `flock model info <id>` for one model's full spec.

### Proxied (paid APIs — shipped, works today)

When a request's model name matches one of these, Flock proxies to the upstream vendor with **your** API key (env-configured) and logs the call as usage like any other request:

- **Anthropic upstream**: any `claude-*` model id
- **OpenAI upstream**: `gpt-*`, `o1*`, `o3*`, `o4*` model ids

Routing logic lives in `internal/api/egress.go`; vendor detection in `internal/router/router.go`.

### Roadmap — model families not yet in catalog

These work today via `flock model add hf:owner/repo` but don't have curated YAML entries with hardware specs:

- **Larger general / agent models** — Qwen3-235B, MiniMax-M2.7, MiMo-V2 — pending sharded YAML entries.
- **Vision (image input)** — `llama-4-scout`, `gemma4-26b`, and `qwen3-vl:*` are in the catalog but the API path (`POST /v1/chat/completions` with image_url) still needs engine wiring → tracked as **M4-T03**.
- **Embeddings + rerank (for RAG)** — `/v1/embeddings` endpoint not shipped; tracked as **M4-T05**.
- **Speech / transcription** — `/v1/audio/transcriptions` not shipped; tracked as **M4-T04**.

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
8. Prints next steps + tells you if PATH needs updating

### Installer flags (after `| sh -s --`)

```bash
--help                  show usage
--version <vX.Y.Z>      install a specific version
--install-dir <path>    install to a specific dir
--no-engine             skip the Ollama check
--dry-run               show what would happen, no writes
```

Install **and** join a cluster in one command:

```bash
curl -fsSL https://raw.githubusercontent.com/hadihonarvar/flock/main/installer/install.sh | \
    sh -s -- join https://leader.local:8080?token=<TOKEN>
```

### Build from source

```bash
git clone https://github.com/hadihonarvar/flock
cd flock
go build -o flock ./cmd/flock
./flock version
```

Requires Go 1.22+. See [ARCHITECTURE.md → Build from source](ARCHITECTURE.md#build-from-source) for cross-compile + release builds.

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
  ollama_endpoint: "http://127.0.0.1:11434"
  vllm_endpoint:   "http://127.0.0.1:8000"
  mlx_endpoint:    "http://127.0.0.1:8080"

router:
  default_model: ""                   # empty → auto-pick on first up
  sticky_sessions: true
  fallback:
    enabled: false                    # true → forward unknown claude-*/gpt-* models to vendor
    anthropic_url: "https://api.anthropic.com"
    openai_url:    "https://api.openai.com"

observability:
  otlp_endpoint: ""                   # e.g. http://localhost:4318 — empty disables tracing (no-op overhead)
```

### Environment variables

| Var | Overrides |
|---|---|
| `FLOCK_LISTEN` | `listen` |
| `FLOCK_DATA_DIR` | `data_dir` |
| `FLOCK_LOG_LEVEL` | `log_level` |
| `FLOCK_EXTERNAL_URL` | `external_url` |
| `FLOCK_ENGINE` | `engine.preferred` |
| `FLOCK_OLLAMA_ENDPOINT` / `FLOCK_VLLM_ENDPOINT` / `FLOCK_MLX_ENDPOINT` | corresponding `engine.*_endpoint` |
| `VLLM_API_KEY` | bearer token sent to a vLLM server (no YAML equivalent) |
| `FLOCK_REQUIRE_KEYS` | `auth.require_keys` (truthy `1/true/yes`) |
| `FLOCK_DEFAULT_MODEL` | `router.default_model` |
| `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` | enables `router.fallback` for the matching vendor |
| `FLOCK_CATALOG_DIR` | `catalog_dir` |
| `FLOCK_OTLP_ENDPOINT` | `observability.otlp_endpoint` (OTLP/HTTP collector URL or bare `host:port`) |

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
flock model add qwen3-coder           # from catalog
flock model add hf:Qwen/Qwen3-72B-AWQ # from HuggingFace
flock model add file:./my-finetune.gguf
```

This:
1. Checks `catalog/<id>.yaml`'s `hardware.min_ram_gb` (and `min_vram_gb`) against the cluster — installs that overshoot the floor are refused with a clear error. Pass `--force` to override (e.g. when you know swap or a quantization knob will save you).
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
flock connect claude-code                          # prints env vars, pre-filled with your URL + token
flock connect cursor                               # prints Cursor settings, pre-filled
flock connect --list                               # supported clients today

# Overrides
flock connect cursor --model qwen-coder-14b        # suggest a specific model
flock connect aider --base-url https://flock.lan   # override gateway URL
FLOCK_TOKEN=sk-orc-… flock connect aider           # use a non-default token
flock connect aider --token sk-orc-…               # same, via flag
```

Token comes from `--token`, then `$FLOCK_TOKEN`, then `~/.flock/admin.key` (written when you ran `flock up`). Base URL comes from `--base-url`, then `external_url` in `~/.flock/config.yaml`, then `http://localhost:<listen>`.

### Reversing: `flock disconnect <client>`

```bash
flock disconnect claude-code        # prints the unset + sk-ant-… export commands
flock disconnect cursor             # GUI steps to clear the override
flock disconnect --list             # same 10 clients
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
| `POST` | `/v1/chat/completions` | Streaming + non-streaming; accepts `image_url` content blocks (Ollama path) |
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
| `GET` | `/admin/v1/tokens` | List API keys (no hash, no plaintext) |
| `POST` | `/admin/v1/tokens` | Create a key — returns plaintext ONCE |
| `DELETE` | `/admin/v1/tokens/{id}` | Revoke a key |
| `GET` | `/admin/v1/shards` | List shards across all models |
| `POST` | `/admin/v1/shards/create` | Orchestrate a sharded model |
| `DELETE` | `/admin/v1/shards/{model_id}` | Tear down a sharded model |
| `GET` | `/admin/v1/usage/recent` | Recent inference records |
| `GET` | `/admin/v1/audit/recent` | Recent admin actions |
| `GET` | `/admin/v1/config` | Effective config, secrets redacted |

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

---

## CLI reference

Every admin action is available via the CLI **and** the web UI — full parity since v0.4.

```
# --- lifecycle (CLI only — UI can't kill the process running the UI) ---
flock up                          Start the local node (leader on first run)
flock down                        Stop the local node
flock status                      Show local + cluster status
flock join <url>?token=…          Join an existing cluster as a worker
flock doctor                      Diagnose common problems
flock update [--check]            Check / install the latest Flock release
flock upgrade                     Alias for `update`
flock version                     Print version

# --- nodes ---
flock node ls                     List nodes
flock node show <id>              Inspect a node
flock node drain <id>             Drain a node (no new requests routed to it)
flock node remove <id>            Forget a node

# --- models (non-sharded) ---
flock model search <query>        Search catalog
flock model ls                    List installed models
flock model add <id>              Install a model (auto-delegates if sharded)
flock model info <id>             Full details for one catalog model
flock model remove <id>           Uninstall a model

# --- sharded models (one model split across N machines) ---
flock shard create <model> [N]    Orchestrate a sharded model across N workers
flock shard ls                    List shards across all sharded models
flock shard remove <model>        Tear down a sharded model

# --- API keys / tokens ---
flock token create [name]         Issue an API key (--admin, --node)
flock token ls                    List API keys
flock token revoke <id>           Revoke a key

# --- observability (CLI new in v0.4 — was UI-only before) ---
flock usage [--limit N] [--user X]   Show recent inference usage records
flock audit [--limit N] [--actor X]  Show recent admin audit log entries

# --- config (CLI new in v0.4) ---
flock config show [--json]        Show effective runtime config (secrets redacted)
flock config path                 Print config file path
flock config edit                 Print the editor command for the config file
```

---

## Web UI

The UI is shipped embedded in the Go binary via `//go:embed`. It is *not* a separate deployment. Open `http://localhost:8080` and paste the admin key.

All admin actions are also doable via CLI — see the [CLI reference](#cli-reference).

| Tab | Capabilities |
|---|---|
| **Dashboard** | Cluster summary: nodes, models, recent request count, tokens served, copy-paste curl example with your admin key |
| **Nodes** | List + status; **Add node** wizard generates a join token; per-row **drain** and **remove** buttons |
| **Models** | List installed models; **catalog picker** dropdown to install a new one; per-row **remove** button (auto-handles sharded teardown) |
| **Shards** | List shards grouped by sharded model; **Create sharded model** form (id + shard count); per-model **Tear down** button |
| **Tokens** | List API keys (id/name/scope/quota/status); **Create** form with name + scope (user/admin/node) + daily quota; **Revoke** button per row; new keys shown ONCE in a modal |
| **Usage** | Recent inference records: time, user, model, protocol, tokens, latency, outcome |
| **Audit** | Recent admin actions with actor + action + target |
| **Settings** | Read-only effective config with secrets redacted; instructions for editing `~/.flock/config.yaml` and the env vars (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `FLOCK_*`) |

## CLI vs UI parity (v0.4)

Every cluster action is available both ways. Pick whichever fits your workflow:

| Action | CLI | UI |
|---|---|---|
| Add node | `flock token create --node` → `flock join <url>?token=…` on worker | Nodes tab → "Add node…" |
| Drain node | `flock node drain <id>` | Nodes tab → row's "drain" |
| Remove node | `flock node remove <id>` | Nodes tab → row's "remove" |
| Install model | `flock model add <id>` | Models tab → catalog picker → "Install" |
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
exo is the closest project conceptually. Flock differs by: (1) Anthropic-API compatibility for Claude Code, (2) explicit hybrid local+vendor routing, (3) multi-tenant API keys / quotas / OIDC, (4) embedded UI and observability stack, (5) Go single-binary install.

**Does Flock train models?**
No. Use Axolotl / Unsloth / torchtune for training. Bring back a LoRA adapter; Flock will serve it.

**Why Go and not Rust?**
Go ships a static binary as fast as Rust for this workload, with a faster development loop. We may rewrite hot paths in Rust if measurements justify it.

**Is there a hosted version?**
Not initially. The product is the software you run.

**Can I use my own Tailscale account?**
Yes — set `mesh.tailnet_name` and `mesh.auth_key` to your tailnet. Otherwise Flock spins up a dedicated tailnet for the cluster.

**Does Flock support AMD GPUs?**
Linux + ROCm via vLLM-ROCm is on the roadmap (v1.0).

**Can I run this on Windows?**
Workers no (no MLX, no native vLLM). Leader/CLI yes via WSL2. Native Windows isn't a near-term priority.

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
