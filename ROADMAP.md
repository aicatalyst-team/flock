# Roadmap — multimodal + accessibility (v0.4 → v1.0)

Last updated: 2026-06-07 · Current release: **v0.21.0** (auto-release bumps +0.1 per `feat:` commit) · See [TASKS.md](TASKS.md) for the per-task tracker.

This file is the strategic plan. It groups everything into three buckets — **modalities that fit Flock's architecture**, **modalities that stretch it**, and the **eight accessibility bets** that turn open-source AI from "I can run a model" into "my team uses this in production."

Video and real-time voice agents are intentionally out of scope. They belong in sibling projects (`Reel` and `Murmur`) that depend on Flock for the LLM piece — see [§ Out of scope](#out-of-scope).

---

## Buckets

### A. Fits naturally — extend the gateway in place (v0.4)

These reuse the existing Engine interface, router, and store. They add endpoints or capability flags; the operational model stays the same.

| Item | Endpoint | Engines that support it | Compat notes | Status |
| --- | --- | --- | --- | --- |
| **Vision (image input)** | `POST /v1/chat/completions` with `image_url` content blocks | Ollama (`images: []`), vLLM, MLX-LM | `engines.Message.Content` → needs `Images []string`. OpenAI content-array parsing in `internal/api/openai.go`. Anthropic `image` blocks in `internal/api/anthropic.go`. Catalog already has `vision` capability. | **Shipped in v0.4** (Ollama path) |
| **Embeddings** | `POST /v1/embeddings` | Ollama (`/api/embeddings`), vLLM (`/v1/embeddings`), MLX-LM | New `Engine.Embed(ctx, model, input) []float32` method. Catalog entries get `embedding` capability + `embedding_dim`. Router picks by capability. | **Shipped in v0.4** (Ollama path) |
| **Rerank** | `POST /v1/rerank` (Cohere shape) | BGE / Jina / mxbai cross-encoders via llama-server `/v1/rerank` or TEI | Deferred — needs a rerank-shaped engine method + a Cohere-format handler. The llama.cpp single-node driver (shipped) is the transport; the API surface is the remaining work. | Deferred |

### B. Stretches the gateway — works but requires new code paths (v0.5–v0.6)

These add endpoints that don't fit the chat-streaming pattern. Worth doing but each is a noticeable code-shape change.

| Item | Endpoint | Engines | Stretch | Verdict |
| --- | --- | --- | --- | --- |
| **ASR (speech → text)** | `POST /v1/audio/transcriptions` | faster-whisper, NVIDIA NeMo (Nemotron 3.5 ASR), vLLM-whisper | Synchronous request, non-streaming response. New engine type `ASREngine`. Audio bytes in, text out. | v0.5 — voice agents are next wave; needed |
| **TTS (text → speech)** | `POST /v1/audio/speech` | Piper, Coqui XTTS, Bark | Output is binary audio (mp3/opus/pcm). Streaming via chunked binary or WebSocket. Different result shape than chat. | v0.5 |
| **Image generation** | `POST /v1/images/generations` | Stable Diffusion via diffusers, ComfyUI, Flux | 5–30 s synchronous. Different VRAM profile (squeezes out chat). Router needs to know about "GPU-locked" jobs vs token-streamed jobs. | v0.6 — only if there's demand |

### C. Out of scope — separate apps that depend on Flock

| Workload | Why separate | Sibling project (proposed) |
| --- | --- | --- |
| Video generation (HunyuanVideo, Wan2.1, LTX, Mochi) | Minutes per inference, multi-GB output, needs real job queue + webhook callbacks. Operational model is render farm, not API gateway. | **`Reel`** |
| Real-time voice agents (full-duplex, < 300 ms loop) | Bidirectional streaming, VAD, interruption handling. Tight ASR + LLM + TTS loop in one socket. | **`Murmur`** — uses Flock as the LLM backend |

---

## Orchestration bets (the actual gateway value)

Scope filter: Flock is an **orchestration / router / gateway** for open-weight LLMs running on a trusted network. RBAC, SSO, billing-per-user analytics, and content policies are explicitly **out of scope** — they're enterprise-SaaS feature creep and other projects do them better. We assume:

- The network is trusted (LAN, Tailscale, internal VPN)
- Existing per-user API keys + daily token quotas + full audit log are sufficient for accountability
- Operators want better routing decisions, not more user management

With that filter, here's what genuinely moves the needle:

| # | Bet | Why it's gateway value | Where it lives | Effort | Target |
| --- | --- | --- | --- | --- | --- |
| 1 | **Latency-aware fallback** | Router silently falls back to a smaller local model (or vendor) when the current node can't keep up at < 2 s TTFT. 10× addressable hardware. | Router records p95 TTFT per (node, model); falls back over threshold | **M** | v0.7 |
| 2 | **Hardware abstraction** | Treat M3 Studio + RTX 4090 + Snapdragon X laptop as one compute pool. Scheduler routes by VRAM/load/network. Pure orchestration. | Replace router's `pick()` with a planner over `nodes.capabilities` (already in store) | **M** | v0.8 |
| 3 | **Edge runtime (NAS / Pi)** | Gateway runs on smaller hardware = more deployments. Already cross-compiled to `linux/arm64`. | ✅ **.deb + .rpm shipped v0.6** via GoReleaser `nfpms` (binary at `/usr/bin/flock`, catalog at `/usr/share/flock/catalog`, recommends `llama.cpp` for sharding). Synology `.spk` (DSM SDK toolchain) is the remaining piece for v0.7. | **S** | v0.6 (.deb/.rpm) · v0.7 (.spk) |
| 4 | **Signed model catalogs** | Supply-chain trust for catalog entries. "apt for AI." | `minisign` signatures alongside catalog YAML; `flock model add` verifies before install | **S** | v0.8 |
| 5 | **Embeddable Go library** | Let desktop apps / IDE plugins import `flock/runtime` directly. Biggest distribution channel for OSS AI in 2026 isn't a CLI, it's *embedded in tools developers already use*. | Move CLI glue out of `internal/`; expose `pkg/runtime`, `pkg/router`, `pkg/store` | **L** | v1.0 |

### Explicitly killed (or sibling-projected) scope

| Item | Why not Flock |
| --- | --- |
| RBAC roles / OIDC / SSO | Enterprise auth is feature creep for a gateway on a trusted network. Per-user keys + quotas + audit already cover the accountability story. |
| Cost / billing tracking | Not Flock's job. LLM API charges go to the operator's account; how they slice cost by team / project / user is a manager-side reporting concern, not a gateway concern. The `audit_log` + `usage` tables expose enough data for anyone to roll their own. |
| Billing-per-user analytics dashboards | Same reasoning — manager-side concern. |
| Unified billing across local + vendor calls | Same family as cost / billing tracking — out. Operators reconcile vendor invoices themselves; the `usage` table records what each call cost in tokens, not dollars. |
| Policy-based routing by user / request shape | "By user" routing is tenant-isolation = enterprise creep, same family as RBAC. "By request shape" is already handled — the router picks engines by capability (vision vs embedding vs chat) and falls back via the catalog `fallback:` chain. Anything beyond that is a content-policy concern (see below). |
| Content policies / output filtering | Different operational concern; happens at the client (Claude Code, Cursor) layer, not the gateway. |
| Privacy-by-default RAG | RAG is a separate workload (vector store, retrieval, ranking pipelines). If needed, build it as a sibling project that depends on Flock's embeddings + chat endpoints. |
| Video / real-time voice | Already out of scope — see [§ Out of scope](#out-of-scope). |

---

## Gateway plumbing improvements

Not strategic bets — small, scoped extensions of subsystems that already exist. Listed here so they don't get lost between "modalities" and "bets."

| # | Item | What it adds | Where it lives | Effort | Target |
| --- | --- | --- | --- | --- | --- |
| P1 | **Bedrock + Vertex egress adapters** | Two more vendor routes alongside the existing Anthropic + OpenAI fallback. Lets orgs with AWS / GCP spend keep using their existing billing path. | ✅ **Bedrock shipped** v0.6 (SigV4 via aws-sdk-go-v2; `anthropic.*` model family, non-streaming). ADC auth probe wired for Vertex. **Remaining for v0.7**: Bedrock streaming + non-Anthropic body shapes (amazon.*, meta.*, mistral.*); Vertex body translation (OpenAI/Anthropic → generateContent Contents). | **S** | v0.6 (Bedrock) / v0.7 (Vertex) |
| P2 | **OpenTelemetry / OTLP traces** | End-to-end span coverage: HTTP handler → router → engine driver. Pairs with the existing Prometheus metrics so latency anomalies in Grafana have a corresponding trace to drill into. | ✅ **Shipped end-to-end** in v0.6. HTTP-layer spans (chi wrapped with `otelhttp`, OTLP/HTTP exporter, W3C propagation) + router child spans (`router.Chat`, `router.Embed`, per-attempt span for fallback) + Ollama engine spans (with prompt/completion token counts). vLLM/MLX/llamacpp drivers follow the same pattern in v0.7. | **S** | v0.6 |
| P3 | **Reference Grafana dashboards** | Importable JSON for cluster overview, per-model, per-user / per-key — covers the same Prometheus metrics already exposed. | New `dashboards/` directory with three `.json` files; documented in README. No code change. | **XS** | v0.7 |

---

## Compatibility review (per item)

Already-shipped subsystems that each item touches. Bold = breaking change, italic = additive only.

| Item | engines.Engine | internal/api/* | internal/store/* | internal/router | catalog YAML schema |
| --- | --- | --- | --- | --- | --- |
| Vision | *add Images []string* | *parse content array* | none | none | none (already has `vision`) |
| Embeddings | *new method `Embed()`* | *new `/v1/embeddings`* | *new `embedding_calls` table* | *route by capability* | *add `embedding_dim`* |
| Rerank | *new method `Rerank()`* | *new `/v1/rerank`* | piggyback embeddings table | route by capability | *add `rerank` capability* |
| ASR | *new sibling interface `ASREngine`* | *new `/v1/audio/transcriptions`* | *audio_calls table* | extend pick() | *add `asr` capability* |
| TTS | *new `TTSEngine`* | *new `/v1/audio/speech`* | reuse audio_calls | extend pick() | *add `tts` capability* |
| Image gen | *new `ImageEngine`* | *new `/v1/images/generations`* | *image_calls + storage* | needs job-aware scheduler | *add `image_gen` capability* |
| (1) Latency fallback | none | none | *add `route_telemetry` table* | extend pick() | *add fallback chain to catalog* |
| (2) HW abstraction | none | none | extend `nodes.capabilities` JSON | **rewrite `pick()`** | none |
| (3) Edge runtime | none | none | none | none | none |
| (4) Signed catalogs | none | none | none | none | *add `signature` field* |
| (5) Go library | **reorganize package layout** | none | none | none | none |

The only **breaking changes** are (2) router rewrite and (5) package layout — both planned for after v0.7 so users have time to adopt.

---

## Sequence

```
v0.4.0 (done)  → Vision (Ollama path)
v0.5.0 (done)  → Embeddings · catalog fallback chain · HMAC mutual auth · GGUF distribution
                 · coordinator-on-worker · OTLP traces (HTTP layer) · Grafana dashboards
                 · Bedrock + Vertex routing detection · 15-client `flock connect` roster
v0.6           → Bedrock + Vertex actual signing · OTLP child spans (router + engines)
                 · Rerank · Anthropic extended thinking + computer use
v0.7           → Latency-aware fallback (bet 1) · Edge runtime / NAS packages (bet 3)
v0.8           → Hardware abstraction (bet 2) · Signed catalogs (bet 4) · Image generation
v0.9           → ASR · TTS · LoRA hot-loading
v1.0           → Embeddable Go library (bet 5) · API stability commitment
```

Every release is auto-cut from conventional commits — see `.github/workflows/auto-release.yml`.

---

## Out of scope

- **Video generation.** Sibling repo `Reel`. Job-queue model, not gateway.
- **Real-time voice agents.** Sibling repo `Murmur`. Uses Flock as the LLM backend.
- **Training / fine-tuning.** Out of project scope. Use `axolotl`, `unsloth`, or `torchtune`.
- **Vector store as a service.** Flock will ship an *adapter* for SQLite-VSS / pgvector in (4) but won't run its own vector store.
