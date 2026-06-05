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

  API:    http://localhost:8080/v1
  Health: http://localhost:8080/healthz

  Admin API key (shown once — store it now):
    sk-orc-xK9pQANw-nmzUbVdvL3S-aJKKvPeNa-eedqt

  Press Ctrl-C to stop.
```

**Copy that admin key now.** You won't see it again.

---

## 💬 Test it (pick one)

### A) curl from your terminal

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

### B) Web dashboard

1. Open <http://localhost:8080> in a browser
2. Paste the admin key
3. Explore: Dashboard, Nodes, Models, Tokens, Usage, Settings

### C) Claude Code (use your local model instead of paying Anthropic)

```bash
export ANTHROPIC_BASE_URL=http://localhost:8080
export ANTHROPIC_AUTH_TOKEN=sk-orc-xK9p…
export ANTHROPIC_MODEL=llama-3.2-1b      # tell Claude Code which local model to use
claude
```

Claude Code now talks to your local Llama 1B instead of `api.anthropic.com`.

> **Why `ANTHROPIC_MODEL`?** Without it Claude Code defaults to a `claude-*` model name. With no `ANTHROPIC_API_KEY` set, Flock won't proxy to real Anthropic, so the request would 404 against your local engine. Setting `ANTHROPIC_MODEL` to a local catalog id makes Claude Code request your local model.

### D) Cursor / Aider / OpenAI SDK

Point them at `http://localhost:8080/v1` with the admin key as the API key.

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

## 🎯 Next steps

- **Use a bigger model**: `flock model add qwen-coder-14b` (needs ~10 GB RAM)
- **Wire up real Claude/GPT as fallback**: `export ANTHROPIC_API_KEY=...` before `flock up` — requests for `claude-*` model names transparently proxy to Anthropic and get logged like local requests
- **See the full UI tour, CLI reference, troubleshooting**: [README.md](README.md)
- **Understand the architecture**: [ARCHITECTURE.md](ARCHITECTURE.md)
- **Per-command help**: `flock <cmd> --help` for any command

---

## 🔒 Security model (read before exposing it)

Flock v0.4 assumes a **trusted network** (LAN or [Tailscale](https://tailscale.com/)). Specifically:

- **User API keys** (admin / user scope) are **sha256-hashed** in the database. The plaintext shown at creation time is the only way to use the key.
- **Worker tokens** (the shared secret between leader and worker) are **stored plaintext** in `nodes.worker_token`. Anyone with read access to the leader's SQLite file can impersonate a worker. v0.5 plans HMAC-based mutual auth.
- **Worker HTTP servers** bind only to the mesh address (LAN / tailnet IP), never to `0.0.0.0`. Network reachability is the first line of defense.
- The **embedded web UI** authenticates by pasted admin key (stored in browser `localStorage`).

If you're not on a trusted LAN, run the cluster **behind Tailscale** or a similar zero-trust overlay until the HMAC story lands.

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
