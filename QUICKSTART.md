# Flock — Quick Start

The fastest path from zero to your first local chat completion. **3 minutes** on a fresh Mac or Linux machine.

> For full docs see [README.md](README.md). For design see [ARCHITECTURE.md](ARCHITECTURE.md).

---

## 🤔 First — how many machines?

| Your situation | Use |
|---|---|
| **One person**, or a small team sharing one beefy box | **1 machine** — everything below works |
| **More throughput** (lots of concurrent users) | 2+ machines — leader + workers |
| **A model bigger than any single machine** (e.g. Llama 70B on Mac Minis) | 2+ machines + sharding (`flock shard create`) |
| **Heterogeneous fleet** (e.g. Mac for coder model, NVIDIA for chat) | 2+ machines, models pinned per node |

**One machine is enough for most teams.** Multi-machine is for scale-out, not a requirement. Both setups install the same way — only the *commands you run after installing* differ.

### 🖼️ Single-machine setup (what you're building below)

```
              Your computer  (Mac or Linux)
   ┌─────────────────────────────────────────────────┐
   │                                                 │
   │   ┌───────────┐  ┌────────────┐  ┌───────────┐  │
   │   │  Cursor   │  │ Claude Code│  │   curl    │  │
   │   │  Aider    │  │            │  │   SDKs    │  │
   │   └─────┬─────┘  └─────┬──────┘  └─────┬─────┘  │
   │         └──────────────┼───────────────┘        │
   │                        │                        │
   │                        ▼                        │
   │           ┌──────────────────────────┐          │
   │           │      FLOCK  :8080        │          │
   │           │   OpenAI + Anthropic     │          │
   │           │   APIs · auth · quotas   │          │
   │           │   audit log · admin UI   │          │
   │           └────────────┬─────────────┘          │
   │                        │ (local pipe)           │
   │                        ▼                        │
   │           ┌──────────────────────────┐          │
   │           │   Ollama  :11434         │          │
   │           │   (the actual LLM)       │          │
   │           └──────────────────────────┘          │
   │                                                 │
   └─────────────────────────────────────────────────┘
```

---

## 🐣 Step 0 — what you need

- **A computer**: Mac (Apple Silicon) or Linux (x86_64 / arm64)
- **8 GB+ RAM** (more is better; the model has to fit in memory)
- **10 GB free disk** (for the model)
- **Internet** (one-time, to download the binary + the model)

---

## 🍎 macOS (Apple Silicon)

```bash
# 1. install Ollama (use the cask — the plain `brew install ollama` is broken)
brew install --cask ollama
open -a Ollama

# 2. install Flock
curl -fsSL https://raw.githubusercontent.com/hadihonarvar/flock/main/installer/install.sh | sh

# 3. start Flock with a small model (auto-downloads on first run)
FLOCK_DEFAULT_MODEL=llama-3.2-1b flock up
```

## 🐧 Linux (x86_64 or arm64)

```bash
# 1. install Ollama
curl -fsSL https://ollama.com/install.sh | sh
sudo systemctl enable --now ollama

# 2. install Flock
curl -fsSL https://raw.githubusercontent.com/hadihonarvar/flock/main/installer/install.sh | sh

# 3. start Flock with a small model
FLOCK_DEFAULT_MODEL=llama-3.2-1b flock up
```

---

## ✅ What you should see

After step 3, Flock prints:

```
▶ detected darwin/arm64 · 24 GB RAM · 8 cores
✔ default model: llama-3.2-1b
✔ engine: ollama at http://127.0.0.1:11434
▶ pulling llama3.2:1b ... 100%
✔ model ready: llama-3.2-1b

  Flock is ready.

  Dashboard:  http://localhost:8080
  API:        http://localhost:8080/v1
  Health:     http://localhost:8080/healthz

  Admin API key (shown once — store it now):
    sk-orc-xK9pQANw-nmzUbVdvL3S-aJKKvPeNa-eedqt

  Next steps:
    →  Test in the browser:  http://localhost:8080
    →  Wire up Claude Code:  flock connect claude-code
    →  Wire up Cursor:       flock connect cursor
    →  See all clients:      flock connect --list
    →  Invite a teammate:    flock invite <name>

  Press Ctrl-C to stop.
```

**Copy that admin key now.** You won't see it again. (It's also saved to `~/.flock/admin.key` for subsequent CLI commands like `flock connect` and `flock invite`.)

---

## 💬 Test it (pick one)

### A) Fastest — `flock connect <tool>`

Prints copy-paste config for any of 10 supported tools, with your URL + token already substituted:

```bash
flock connect claude-code     # Anthropic-API tools
flock connect cursor          # IDE settings
flock connect aider           # CLI flags
flock connect --list          # see all 10
```

### B) Web dashboard

1. Open <http://localhost:8080> in a browser
2. Paste the admin key (auto-saved as `~/.flock/admin.key` so subsequent CLI calls don't need it)
3. Click **Connect** in the nav — pick a tool, click Copy, you're done
4. Or click **Playground** for an in-browser chat to sanity-check the model
5. Other tabs: Dashboard, Nodes, Models, Shards, Tokens, Usage, Audit, Settings

### C) curl from your terminal

```bash
KEY="sk-orc-xK9p…"   # paste your key

curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"auto","messages":[{"role":"user","content":"say hi in 5 words"}]}'
```

You'll see JSON like:

```json
{"choices":[{"message":{"role":"assistant","content":"Hello! How can I help?"}}]}
```

### D) Manual — Claude Code (if you can't run `flock connect`)

```bash
export ANTHROPIC_BASE_URL=http://localhost:8080
export ANTHROPIC_AUTH_TOKEN=sk-orc-xK9p…
export ANTHROPIC_MODEL=llama-3.2-1b      # tell Claude Code which local model to use
claude
```

Claude Code now talks to your local Llama 1B instead of `api.anthropic.com`.

> **Why `ANTHROPIC_MODEL`?** Without it Claude Code defaults to a `claude-*` model name. With no `ANTHROPIC_API_KEY` set, Flock won't proxy to real Anthropic, so the request would 404 against your local engine. Setting `ANTHROPIC_MODEL` to a local catalog id makes Claude Code request your local model.

### E) Vision (image input)

If you've installed a vision-capable model (e.g. `flock model add gemma4-12b`, `gemma4-26b`, `llama-4-scout`, or any `qwen3-vl-*`), you can send images on the same `/v1/chat/completions` endpoint:

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemma4-12b",
    "messages": [{
      "role": "user",
      "content": [
        {"type": "text", "text": "what is in this picture?"},
        {"type": "image_url", "image_url": {"url": "data:image/png;base64,iVBORw0KGgoAA..."}}
      ]
    }]
  }'
```

Anthropic-shape (`/v1/messages` with `image` content blocks) works too. Vision routes through the Ollama path today; the engine driver pulls the image bytes from data URLs or http(s) URLs.

### F) Embeddings

```bash
flock model add nomic-embed-text       # one-time install of the default embedding model

curl http://localhost:8080/v1/embeddings \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"nomic-embed-text","input":"hello world"}'
```

You'll get back an OpenAI-shape `{"data":[{"embedding":[…768 floats…]}]}` response. Use it with any RAG library that talks OpenAI embeddings.

---

## 👥 Share with your team

Once you've confirmed it works:

```bash
flock invite hadi --quota 100000
```

This creates a user-scope token for `hadi` (capped at 100k tokens/day) and prints a paste-into-Slack markdown card with config snippets for every supported client (Claude Code, Cursor, Aider, Continue, Zed, Cline, Qwen-Code, **Hermes Agent**, **OpenClaw**, **OpenCode**, **Open WebUI**, **Goose**, OpenAI SDK, Anthropic SDK, curl). Your teammate copies the snippet for the tool they use → they're talking to your hardware.

The same flow works in the dashboard: **Tokens → + Invite teammate**.

---

## 🆘 If it doesn't work

Run the doctor first — it tells you what's wrong in plain English:

```bash
flock doctor
```

Most common failures:

| You see | Fix |
|---|---|
| `command not found: flock` | The install dir isn't on your PATH. Run: `export PATH="$HOME/.local/bin:$PATH"` (and add it to `~/.zshrc` or `~/.bashrc` to make it permanent) |
| `engine (ollama) at http://127.0.0.1:11434 is not reachable` | Start Ollama: `ollama serve &` (Linux: `sudo systemctl start ollama`) |
| `502 Bad Gateway` with `llama-server binary not found` | The Homebrew `ollama` formula on Apple Silicon is broken. Fix: `brew uninstall ollama && brew install --cask ollama` |
| `Port 8080 in use` | Another process is on it. Use a different port: `FLOCK_LISTEN=:8090 flock up` (avoid `:8081` — that's the default worker port) |
| `no admin key on disk` (running CLI) | `flock up` isn't running on this host. Start it first, then re-run the CLI command |

More fixes in the [main README's troubleshooting table](README.md#troubleshooting-installation).

---

## 🌐 Add a second (or third…) machine

Same install command everywhere. The first machine becomes the **leader**, every other machine becomes a **worker**.

### 🖼️ Two-machine setup (what you're about to build)

```
                MACHINE A  (leader)                            MACHINE B  (worker)
   ┌──────────────────────────────────────┐       ┌──────────────────────────────────────┐
   │                                      │       │                                      │
   │  Your tools ──► Flock :8080          │       │      Flock agent :8081               │
   │                 ┌───────┐            │       │      (worker HTTP server,            │
   │                 │ Router│ ───────────┼───────┼────► proxies requests to local       │
   │                 └───────┘            │       │       Ollama, token-auth'd)          │
   │                                      │       │                  │                   │
   │  Admin UI  :8080/                    │       │                  ▼                   │
   │  CLI: flock node ls / model ls       │       │      Ollama :11434                   │
   │                                      │       │      (does the model serving)        │
   │  Local Ollama :11434                 │       │                                      │
   │  (serves models the leader hosts)    │       │  ◄── heartbeat every 5s ──┐          │
   │                                      │       │      (carries loaded_models)         │
   │                                      │       │                           │          │
   └──────────────────────────────────────┘       └───────────────────────────┼──────────┘
                  ▲                                                           │
                  │                                                           │
                  └───────────── LAN / tailnet (e.g. WiFi, Tailscale) ────────┘
```

### 🖼️ Step-by-step (what happens when)

```
   LEADER (Machine A)                              WORKER (Machine B)
   ──────────────────                              ─────────────────

   1. install Ollama
   2. install Flock              (steps 1+2 same on every machine)
   3. flock up
      ✔ admin key shown
      ✔ listening :8080

   4. flock token create --node
      ✔ sk-orc-NodeJoin-AbCd1234…
              ─────► copy this token to the worker machine

                                                  1. install Ollama
                                                  2. install Flock
                                                  3. flock join \
                                                       http://leader:8080?token=...
                                                     ✔ registered with leader

                                ◄──── heartbeat every 5s ────

   5. flock node ls
      ID         HOSTNAME    STATE
      local      machine-a   ready
      n_abc123   machine-b   ready

                                                  6. flock model add qwen-coder-7b
                                                     (pulls on the worker's Ollama)

   7. curl :8080/v1/chat/completions
      with model=qwen-coder-7b
            ────► router sees worker has this model ────►
                                                     (worker serves it)
            ◄──────────── response streamed back ─────
```

### Step 1 — on the leader

```bash
flock token create --node
# prints something like:
#   sk-orc-NodeJoin-ABcD1234…
```

Note the leader's reachable address. On a LAN it's its LAN IP (e.g. `192.168.1.42`); on Tailscale, the tailnet hostname.

### Step 2 — on the new machine

Install Flock + Ollama **the same way as above**, then instead of `flock up`:

```bash
flock join http://192.168.1.42:8080?token=sk-orc-NodeJoin-ABcD1234…
```

(Substitute the leader's address and the token you copied.)

### Step 3 — install a model on the worker

```bash
flock model add qwen-coder-7b
```

### Step 4 — verify on the leader

```bash
flock node ls
# example output:
# ID         HOSTNAME      OS/ARCH       ADDRESS              STATE   LAST HB
# local      machine-a     darwin/arm64  127.0.0.1:8080       ready   2026-06-05T…
# n_abc123   machine-b     darwin/arm64  192.168.1.50:8081    ready   2026-06-05T…
```

Any request the gateway gets for `qwen-coder-7b` is now routed automatically to the worker. If you install the **same** model on two workers, the leader load-balances between them.

> ⚠️  Only do this on a trusted LAN or Tailscale — see [Security model](#-security-model-read-before-exposing-it) below.

**Need to split one big model across multiple machines?** That's *sharding* — `flock shard create <model> <N>`. See the [sharded models section in the README](README.md).

---

## 🤖 Use a different model (Qwen, Llama, DeepSeek…)

The default `llama-3.2-1b` is tiny — good for "does this work?" but underpowered for real work. Flock ships a curated **catalog** of better models.

### Browse what's in the catalog

```bash
flock model search           # list everything
flock model search coder     # filter
```

| Catalog id | What it's for | RAM | Engine name |
|---|---|---|---|
| `llama-3.2-1b` | smoke test | 2 GB | `ollama:llama3.2:1b` |
| `llama-3.2-3b` | small fast chat | 4 GB | `ollama:llama3.2:3b` |
| `qwen-coder-7b` | code completion + chat | 8 GB | `ollama:qwen2.5-coder:7b` |
| `qwen-coder-14b` | better code + agent | 16 GB | `ollama:qwen2.5-coder:14b` |
| `qwen-coder-32b` | strong code agent (laptop max) | 32 GB | `ollama:qwen2.5-coder:32b` |
| `qwen3-8b` | general chat, balanced | 12 GB | `ollama:qwen3:8b` |
| `qwen3-14b` | general chat, more capable | 16 GB | `ollama:qwen3:14b` |
| `deepseek-r1-8b` | reasoning ("thinking") | 12 GB | `ollama:deepseek-r1:8b` |
| `llama-3.3-70b-sharded` | frontier, split across machines | 48+ GB total | sharded llama.cpp |

### Install + use a specific model

```bash
# 1. install the model
flock model add qwen-coder-14b
# Flock asks Ollama to pull qwen2.5-coder:14b and registers it.

# 2. use it by its catalog id in API requests
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-orc-..." \
  -d '{"model":"qwen-coder-14b","messages":[{"role":"user","content":"explain monads"}]}'

# 3. or in Claude Code
export ANTHROPIC_MODEL=qwen-coder-14b
claude
```

> 📖 **Full step-by-step per-model guide:** [MODELS.md](MODELS.md) — for *every* model in the catalog: system requirements, performance expectations on Mac/Linux, install + use snippets for curl / Cursor / Claude Code / SDKs, when to switch up.

### Use ANY model Ollama supports (not just the catalog)

The catalog is curated for UX, but Flock will pass through any Ollama model name as-is. Steps:

```bash
# pull directly via Ollama (Flock's catalog is bypassed):
ollama pull mistral-nemo:12b

# then in your API request, use the engine-native name:
curl :8080/v1/chat/completions -H "Authorization: Bearer sk-orc-..." \
  -d '{"model":"mistral-nemo:12b","messages":[…]}'
```

This works because Flock's router falls through to the engine when no catalog entry matches the requested model id.

### Use a different engine entirely (vLLM, MLX-LM, llama.cpp)

Edit `~/.flock/config.yaml`:

```yaml
engine:
  preferred: vllm                       # was: ollama
  vllm_endpoint: http://gpu-host:8000   # where your vLLM is running
```

Or via env: `FLOCK_ENGINE=vllm FLOCK_VLLM_ENDPOINT=http://gpu:8000 flock up`. The router doesn't care which engine serves the request — it just routes by model name.

**Want bare-metal speed on weak hardware?** Use `llama.cpp` directly — lower RAM and cold-start latency than Ollama, and Flock auto-launches `llama-server` for you so it's still one command:

```bash
# 1. install llama.cpp (provides llama-server + rpc-server)
brew install llama.cpp     # macOS · apt: see https://github.com/ggml-org/llama.cpp

# 2. that's it — Flock spawns llama-server itself
FLOCK_ENGINE=llamacpp FLOCK_DEFAULT_MODEL=llama-3.2-1b flock up
```

Flock looks up the catalog entry for the default model, reads its `source.repo` (a GGUF HuggingFace repo), and runs `llama-server -hf <repo> --port 8089` via its internal supervisor — the same one that already manages `rpc-server` for sharded models. When Flock stops, the spawned `llama-server` stops too.

If you'd rather manage `llama-server` yourself (e.g. to pass custom flags like `-ngl 999`), start it before `flock up` and Flock will detect it and skip the auto-spawn:

```bash
llama-server -m ~/models/qwen2.5-coder-7b-q4_k_m.gguf --port 8089 --n-gpu-layers 999
FLOCK_ENGINE=llamacpp flock up
```

Flock's default `llamacpp_endpoint` is `http://127.0.0.1:8089` — chosen to avoid both `:8080` (Flock leader) and `:8081` (worker agent). The same `llamacpp` engine name also covers RPC sharding — `flock shard create` launches a `llama-server --rpc …` coordinator that this driver talks to.

---

## 🔌 Switch Claude Code back to real Anthropic

You set three env vars to route Claude Code through Flock. The fastest way back:

```bash
flock disconnect claude-code
```

This prints the exact `unset` + `export` commands you need (works for every client `flock connect` supports — see `flock disconnect --list`). Paste what it prints, and you're back on `api.anthropic.com`.

Manually, it's just unsetting the three env vars:

```bash
unset ANTHROPIC_BASE_URL
unset ANTHROPIC_AUTH_TOKEN
unset ANTHROPIC_MODEL
```

Then in the same terminal, set your real Anthropic key:

```bash
export ANTHROPIC_API_KEY=sk-ant-…
claude
```

Or just **open a fresh terminal** that never had the Flock vars exported — Claude Code defaults to `api.anthropic.com` when `ANTHROPIC_BASE_URL` isn't set.

### Make the switch permanent

If you'd added those `export` lines to `~/.zshrc` or `~/.bashrc`, remove them:

```bash
# before:
export ANTHROPIC_BASE_URL=http://localhost:8080
export ANTHROPIC_AUTH_TOKEN=sk-orc-...
export ANTHROPIC_MODEL=llama-3.2-1b

# after (remove all three, or just comment them out)
```

Then `source ~/.zshrc` (or open a new terminal).

### Hybrid: keep Flock as your default, fall back to real Claude when needed

This is the best-of-both pattern. Leave the three vars set, but configure Flock with your real Anthropic key:

```bash
# add to your shell rc:
export ANTHROPIC_API_KEY=sk-ant-…    # real Anthropic key (for Flock to proxy)
# (Flock vars stay as before)
```

Restart `flock up`. Now:
- `claude --model llama-3.2-1b` → served by your local Ollama (free, private)
- `claude --model claude-opus-4-7` → transparently proxied to real Anthropic by Flock, logged in the Usage tab, billed to *your* Anthropic account

Same `claude` command, same key paste, you pick per-prompt.

---

## ⬆️ Updating to a new version

When a new release is published on GitHub, update in one command:

```bash
flock update
```

This checks the latest release, downloads the right binary for your platform, verifies the SHA-256 against the published `checksums.txt`, and replaces the binary in place. After it finishes:

```bash
flock down
flock up
```

Other options:

```bash
flock update --check              # see if there's a new version, don't install
flock update --version v0.1.1     # pin a specific version
flock update --force              # reinstall even if already on the latest
flock upgrade                     # alias of `update`
```

If your binary lives in `/usr/local/bin/` (installed with sudo), `flock update` stages the new binary next to it and prints the exact `sudo mv` command to finish.

### 🔔 Update notice on `flock up`

`flock up` checks GitHub for a newer release on startup and prints a one-liner if one exists. The check is cached for 24 hours at `~/.flock/update-check.json` so it only hits GitHub once a day, with a hard 1-second budget so it never slows startup.

To disable (offline environments, privacy):

```bash
export FLOCK_NO_UPDATE_CHECK=1
flock up
```

---

## 🎯 Next steps

- **See the full UI tour, CLI reference, troubleshooting**: [README.md](README.md)
- **Understand the architecture**: [ARCHITECTURE.md](ARCHITECTURE.md)
- **Per-command help**: `flock <cmd> --help` for any command
- **Add more workers**: see [Add a second machine](#-add-a-second-or-third-machine) above

---

## 🔒 Security model (read before exposing it)

Flock v0.4 assumes a **trusted network** (LAN or [Tailscale](https://tailscale.com/)). Specifically:

- **User API keys** (admin / user scope) are **sha256-hashed** in the database. The plaintext shown at creation time is the only way to use the key.
- **Worker tokens** (the shared secret between leader and worker) are stored on the `nodes.worker_token` column. As of v0.5, control-plane traffic uses **HMAC-SHA256 signatures** so the token itself isn't transmitted on the wire after the initial join — the agent and leader both sign with the per-node token. The SQLite file still holds the secret, so a stolen DB still lets an attacker impersonate a worker; encrypt the DB at rest if you can't trust the host. Set `FLOCK_REJECT_BEARER=1` on workers to refuse the bearer-fallback path entirely (HMAC-only).
- **Worker HTTP servers** bind only to the mesh address (LAN / tailnet IP), never to `0.0.0.0`. Network reachability is the first line of defense.
- The **embedded web UI** authenticates by pasted admin key (stored in browser `localStorage`).

If you're not on a trusted LAN, still run the cluster **behind Tailscale** or a similar zero-trust overlay — HMAC stops in-flight token theft but doesn't replace network-layer encryption. The bearer fallback exists for one transition release; set `FLOCK_REJECT_BEARER=1` once every leader and worker is on v0.5+.

---

## 📖 Every command has --help

```bash
flock --help                  # top-level
flock up --help               # any subcommand
flock shard create --help     # any sub-subcommand
flock model --help            # see the available actions
```

---

**Stuck?** Open an issue: <https://github.com/hadihonarvar/flock/issues>
