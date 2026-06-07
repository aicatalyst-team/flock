# Roadmap — multimodal + accessibility (v0.4 → v1.0)

Last updated: 2026-06-07 · Current release: **v0.3.0** · See [TASKS.md](TASKS.md) for the per-task tracker.

This file is the strategic plan. It groups everything into three buckets — **modalities that fit Flock's architecture**, **modalities that stretch it**, and the **eight accessibility bets** that turn open-source AI from "I can run a model" into "my team uses this in production."

Video and real-time voice agents are intentionally out of scope. They belong in sibling projects (`Reel` and `Murmur`) that depend on Flock for the LLM piece — see [§ Out of scope](#out-of-scope).

---

## Buckets

### A. Fits naturally — extend the gateway in place (v0.4)

These reuse the existing Engine interface, router, and store. They add endpoints or capability flags; the operational model stays the same.

| Item | Endpoint | Engines that support it | Compat notes | Status |
| --- | --- | --- | --- | --- |
| **Vision (image input)** | `POST /v1/chat/completions` with `image_url` content blocks | Ollama (`images: []`), vLLM, MLX-LM | `engines.Message.Content` → needs `Images []string`. OpenAI content-array parsing in `internal/api/openai.go`. Anthropic `image` blocks in `internal/api/anthropic.go`. Catalog already has `vision` capability. | **Shipped in v0.4 (this commit)** for Ollama |
| **Embeddings** | `POST /v1/embeddings` | Ollama (`/api/embeddings`), vLLM (`/v1/embeddings`), MLX-LM | New `Engine.Embed(ctx, model, input) []float32` method. Catalog entries get `embedding` capability + `embedding_dim`. Router picks by capability. | Planned |
| **Rerank** | `POST /v1/rerank` (Cohere shape) | BGE / cohere-rerank via vLLM, llama.cpp custom | Sibling to embeddings. `Engine.Rerank(ctx, model, query, docs)`. | Planned |

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
| 1 | **Cost transparency** | Informs routing decisions — operator sees "$0 (local) vs $0.02 (vendor)" per call, can decide which models to keep paid. Pure orchestration data, not user-mgmt. | Catalog gains `cost_per_1m_tokens_in/out`; `usage_records` gains `cost_micros`; UI Usage tab shows the column | **S** | v0.4 |
| 2 | **Latency-aware fallback** | Router silently falls back to a smaller local model (or vendor) when the current node can't keep up at < 2 s TTFT. 10× addressable hardware. | Router records p95 TTFT per (node, model); falls back over threshold | **M** | v0.5 |
| 3 | **Hardware abstraction** | Treat M3 Studio + RTX 4090 + Snapdragon X laptop as one compute pool. Scheduler routes by VRAM/load/network. Pure orchestration. | Replace router's `pick()` with a planner over `nodes.capabilities` (already in store) | **M** | v0.6 |
| 4 | **Edge runtime (NAS / Pi)** | Gateway runs on smaller hardware = more deployments. 4 B model on a $400 Synology NAS democratizes the gateway. | Cross-compile `linux/arm64-musl`, statically link, ship as `.deb` / `.spk` packages | **S** | v0.7 |
| 5 | **Signed model catalogs** | Supply-chain trust for catalog entries. "apt for AI." | `minisign` signatures alongside catalog YAML; `flock model add` verifies before install | **S** | v0.6 |
| 6 | **Embeddable Go library** | Let desktop apps / IDE plugins import `flock/runtime` directly. Biggest distribution channel for OSS AI in 2026 isn't a CLI, it's *embedded in tools developers already use*. | Move CLI glue out of `internal/`; expose `pkg/runtime`, `pkg/router`, `pkg/store` | **L** | v1.0 |

### Explicitly killed (or sibling-projected) scope

| Item | Why not Flock |
| --- | --- |
| RBAC roles / OIDC / SSO | Enterprise auth is feature creep for a gateway on a trusted network. Per-user keys + quotas + audit already cover the accountability story. |
| Billing-per-user analytics dashboards | Cost transparency (bet 1) gives operators the data; user-facing billing UIs are SaaS-app territory. |
| Content policies / output filtering | Different operational concern; happens at the client (Claude Code, Cursor) layer, not the gateway. |
| Privacy-by-default RAG | RAG is a separate workload (vector store, retrieval, ranking pipelines). If needed, build it as a sibling project that depends on Flock's embeddings + chat endpoints. |
| Video / real-time voice | Already out of scope — see [§ Out of scope](#out-of-scope). |

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
| (1) Cost | none | none | **add `cost_micros` column** | none | *add `cost_per_1m_tokens_*`* |
| (2) RBAC | none | *check role on every request* | *new `roles` + `policies` tables* | none | none |
| (3) HW abstraction | none | none | extend `nodes.capabilities` JSON | **rewrite `pick()`** | none |
| (4) RAG | sibling — new package `internal/rag/` | new `/v1/rag/*` endpoints | new `rag_collections` + vector storage | route via embeddings | none |
| (5) Latency fallback | none | none | *add `route_telemetry` table* | extend pick() | *add fallback chain to catalog* |
| (6) Edge runtime | none | none | none | none | none |
| (7) Signed catalogs | none | none | none | none | *add `signature` field* |
| (8) Go library | **reorganize package layout** | none | none | none | none |

The only **breaking changes** are (3) router rewrite and (8) package layout — both planned for after v0.5 so users have time to adopt.

---

## Sequence

```
v0.4.0 (done)  → Vision (Ollama path)
v0.4.x         → Embeddings · Rerank · Cost transparency (bet 1)
v0.5           → ASR · TTS · Latency-aware fallback (bet 2)
v0.6           → Image generation · Hardware abstraction (bet 3) · Signed catalogs (bet 5)
v0.7           → Edge runtime / arm64 NAS packages (bet 4)
v1.0           → Embeddable Go library (bet 6) · API stability commitment
```

Every release is auto-cut from conventional commits — see `.github/workflows/auto-release.yml`.

---

## Out of scope

- **Video generation.** Sibling repo `Reel`. Job-queue model, not gateway.
- **Real-time voice agents.** Sibling repo `Murmur`. Uses Flock as the LLM backend.
- **Training / fine-tuning.** Out of project scope. Use `axolotl`, `unsloth`, or `torchtune`.
- **Vector store as a service.** Flock will ship an *adapter* for SQLite-VSS / pgvector in (4) but won't run its own vector store.
