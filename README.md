# Flock

> **One endpoint. Every LLM. Your hardware.**
>
> A self-hosted, open-source platform that orchestrates open-weight LLMs (Qwen, Llama, DeepSeek, …) across a cluster of your own machines — Mac, Linux/NVIDIA, or mixed — and exposes them as a single API that is drop-in compatible with both OpenAI and Anthropic.
>
> Point Cursor, Claude Code, Aider, Continue, or any OpenAI/Anthropic SDK at Flock. It just works.

---

## 🚀 Try it in 60 seconds

Pick your platform — 4 commands each.

### 🍎 macOS (Apple Silicon — M1/M2/M3/M4)

```bash
# 1. install Ollama (use the cask — plain `brew install ollama` is broken)
brew install --cask ollama
open -a Ollama

# 2. install Flock
curl -fsSL https://raw.githubusercontent.com/hadihonarvar/flock/main/installer/install.sh | sh

# 3. add install dir to PATH if the installer says so
export PATH="$HOME/.local/bin:$PATH"

# 4. start Flock with a tiny model (~1 GB, fast download)
FLOCK_DEFAULT_MODEL=llama-3.2-1b flock up
```

### 🐧 Linux (x86_64 or arm64)

```bash
# 1. install Ollama
curl -fsSL https://ollama.com/install.sh | sh
sudo systemctl enable --now ollama

# 2. install Flock
curl -fsSL https://raw.githubusercontent.com/hadihonarvar/flock/main/installer/install.sh | sh

# 3. add install dir to PATH if needed
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.bashrc && source ~/.bashrc

# 4. start Flock with a tiny model (~1 GB, fast download)
FLOCK_DEFAULT_MODEL=llama-3.2-1b flock up
```

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
| **Status** | Alpha — build-verified on macOS/arm64; single-node verified end-to-end with curl; multi-node routing landed but not yet tested with two physical machines |
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

---

## 60-second quick start

### On the first machine (becomes the leader)

```bash
curl -fsSL https://get.flock.dev | sh
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
    curl -fsSL https://get.flock.dev | sh -s -- join flock-7f3a.ts.net?token=…
```

### On any additional machine

```bash
curl -fsSL https://get.flock.dev | sh -s -- join flock-7f3a.ts.net?token=…
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

- Per-user API keys with revocation and scopes
- Daily/monthly token quotas per key
- Audit log of every call
- OIDC login for the web UI (Google, GitHub, Okta)
- Cost-equivalent metering ("you saved $X vs. OpenAI this month")

### Hybrid local + cloud

- Built-in egress adapters for Anthropic, OpenAI, Bedrock, Vertex
- Policy-based routing: by model name, by user, by request shape
- Transparent fallback when local is overloaded
- Unified billing and logs

### Observability

- Prometheus metrics endpoint
- Pre-built Grafana dashboards
- OpenTelemetry traces across gateway → scheduler → worker
- Web UI live tail of recent requests

### Developer experience

- One-line install (`curl | sh`)
- One-line model add (`flock model add llama3.3`)
- One-line client config (UI generates per-tool snippets)
- Sensible defaults, no required flags
- Embedded web UI — no separate frontend to deploy

---

## Supported models

> **For the complete per-model walkthrough** (system requirements, performance per platform, install + use snippets for every client) see **[MODELS.md](MODELS.md)**.



Flock ships a curated catalog so users don't have to choose AWQ vs GPTQ vs GGUF. Any HuggingFace repo also works via `flock model add hf:owner/repo`.

### Chat / agent

| Catalog ID | Backing model | Quant | RAM/VRAM | Strengths |
|---|---|---|---|---|
| `qwen3-72b` | Qwen3-72B-Instruct | AWQ | 42 GB | Strong all-rounder, agent-capable |
| `qwen3-coder` | Qwen3-Coder-30B-A3B | AWQ | 20 GB | Best single-GPU coding agent |
| `qwen3-coder-large` | Qwen3-Coder-480B-A35B | Q4 | ~280 GB | Frontier open coding agent |
| `llama-3.3` | Llama-3.3-70B-Instruct | AWQ | 42 GB | Broad ecosystem support |
| `deepseek-v3` | DeepSeek-V3 | Q4 | ~380 GB | Frontier general quality |
| `deepseek-r1-distill` | R1-Distill-Qwen-32B | AWQ | 20 GB | Reasoning on one GPU |
| `glm-4.6` | GLM-4.6 | Q4 | varies | Strong tool use |

### Coding completion (low latency)

| Catalog ID | Backing model | Quant | RAM/VRAM |
|---|---|---|---|
| `qwen-coder-7b` | Qwen2.5-Coder-7B | Q4 | 5 GB |
| `qwen-coder-14b` | Qwen2.5-Coder-14B | Q4 | 9 GB |
| `qwen-coder-32b` | Qwen2.5-Coder-32B | Q4 | 20 GB |
| `starcoder2-15b` | StarCoder2-15B | Q4 | 10 GB |
| `granite-code-8b` | Granite-Code-8B | Q4 | 5 GB |

### Small / fast

`llama-3.2-3b` · `qwen3-4b` · `phi-4-mini` · `gemma-3-4b`

### Vision

`qwen2.5-vl-7b` · `qwen2.5-vl-72b` · `llama-3.2-vision-11b` · `pixtral-12b` · `gemma-3-12b`

### Embeddings & rerank (for RAG)

`bge-m3` · `bge-reranker-v2` · `nomic-embed-text-v2` · `qwen3-embedding`

### Speech

`whisper-large-v3` · `whisper-turbo` · `parakeet`

### Proxied (paid APIs)

When a request's model name matches one of these, Flock proxies to the upstream vendor and logs the call:

`claude-opus-4-7` · `claude-sonnet-4-6` · `gpt-4o` · `gpt-4-turbo` · `o3` · `gemini-2-flash` · `gemini-2-pro`

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
mesh:
  enabled: true
  tailnet_name: ""   # auto-generated on first up
auth:
  initial_admin_key: ""   # auto-generated, shown in CLI
```

### Full reference

```yaml
listen: ":8080"
external_url: "https://flock.example.com"   # for redirects in UI; default = listen addr
data_dir: "~/.flock"

mesh:
  enabled: true
  backend: tailscale         # tailscale | netbird | lan
  tailnet_name: ""           # auto-generated if empty
  auth_key: ""               # for headless tailnet login

storage:
  type: sqlite               # sqlite | postgres
  dsn: "~/.flock/state.db"
  models_dir: "~/.flock/models"
  cache_size_gb: 200

auth:
  oidc:
    enabled: false
    issuer: ""
    client_id: ""
    client_secret: ""
  api_keys:
    require: true
    initial_admin_key: ""    # auto-generated

scheduler:
  policy: spread             # spread | binpack
  replication: auto          # auto | always | never
  drain_timeout_s: 60

router:
  default_model: qwen-coder-14b
  sticky_sessions: true
  fallback:
    enabled: true
    providers:
      anthropic:
        api_key_env: ANTHROPIC_API_KEY
        models: [claude-opus-4-7, claude-sonnet-4-6]
      openai:
        api_key_env: OPENAI_API_KEY
        models: [gpt-4o, o3]

observability:
  prometheus: ":9090"
  otlp_endpoint: ""
  log_level: info
```

### Per-node config

Workers don't usually need their own config — they pull settings from the leader. To override (e.g. force a specific engine), drop `~/.flock/node.yaml`:

```yaml
engines:
  preferred: vllm            # vllm | mlx | llamacpp | ollama
  vllm:
    image: vllm/vllm-openai:latest
    args: ["--enable-prefix-caching"]
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
2. On the new machine: `curl -fsSL https://get.flock.dev | sh -s -- join <leader-url>?token=<token>`

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
- No automatic restart on shard crash — the admin re-runs `flock shard create`.
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
1. Records the model in the registry
2. Picks the best node(s) to host it (or shards across multiple)
3. Pulls the weights to those nodes (with resume support)
4. Launches the right inference engine
5. Flips the gateway routing to make the model available

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

The web UI generates these snippets per tool, with your real key baked in.

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
| `POST` | `/v1/chat/completions` | Streaming + non-streaming |
| `GET` | `/v1/models` | Lists available models |

(Planned: `/v1/completions`, `/v1/embeddings`, `/v1/audio/transcriptions`.)

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
- Model is CPU-falling-back? `flock logs --node <id>` will show.

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

- Website: https://flock.dev
- GitHub: https://github.com/hadihonarvar/flock
- Discord: https://discord.gg/flock
- Twitter/X: [@flock_hq](https://twitter.com/flock_hq)
