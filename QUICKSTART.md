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
claude
```

Now Claude Code talks to your local Llama 1B instead of api.anthropic.com.

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
| `Port 8080 in use` | Another process is on it. Use a different port: `FLOCK_LISTEN=:8081 flock up` |
| `no admin key on disk` (running CLI) | `flock up` isn't running on this host. Start it first, then re-run the CLI command |

More fixes in the [main README's troubleshooting table](README.md#troubleshooting-installation).

---

## 🎯 Next steps

- **Use a bigger model**: `flock model add qwen-coder-14b` (needs ~10 GB RAM)
- **Add a second machine** so multiple devs share inference:
  ```bash
  flock token create --node       # on leader, prints a join token
  # on worker:
  flock join http://leader:8080?token=<TOKEN>
  ```
- **Set up Claude/GPT fallback**: `export ANTHROPIC_API_KEY=...` before `flock up` — requests for `claude-*` model names will transparently proxy to Anthropic and get logged the same as local requests
- **See the full UI tour and CLI reference**: [README.md](README.md)
- **Understand the architecture**: [ARCHITECTURE.md](ARCHITECTURE.md)

---

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
