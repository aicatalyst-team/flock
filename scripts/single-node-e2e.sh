#!/usr/bin/env bash
#
# single-node-e2e.sh — bring up a fresh `flock up`, install a tiny
# model from Ollama, and verify a chat completion round-trips through
# the gateway. Catches the "binary builds but doesn't actually serve
# requests" failure mode that unit tests can't reach.
#
# Companion to the two-node-smoke.sh script — that one tests cross-
# node routing; this one tests the single-binary single-host flow on
# the dev's own machine or in CI.
#
# Usage:
#   ./scripts/single-node-e2e.sh
#
# Env knobs:
#   FLOCK_BIN     — flock binary; defaults to ./flock (build it first)
#   MODEL         — model id to install + call; defaults to llama-3.2-1b
#   OLLAMA_HOST   — where ollama is listening; defaults to 127.0.0.1:11434
#   DATA_DIR      — Flock state dir; defaults to a temp dir (cleaned up)
#   LISTEN_PORT   — gateway port; defaults to 18080 to avoid clashes
#
# Exit codes:
#   0   everything worked
#   1   prerequisites missing (ollama, flock binary)
#   2   `flock up` never came healthy
#   3   model install failed
#   4   non-streaming chat completion failed
#   5   streaming SSE round-trip failed

set -euo pipefail

FLOCK_BIN="${FLOCK_BIN:-./flock}"
MODEL="${MODEL:-llama-3.2-1b}"
OLLAMA_HOST="${OLLAMA_HOST:-127.0.0.1:11434}"
DATA_DIR="${DATA_DIR:-$(mktemp -d -t flock-e2e-XXXXXXXX)}"
LISTEN_PORT="${LISTEN_PORT:-18080}"
GATEWAY="http://127.0.0.1:${LISTEN_PORT}"

# --- helpers ---
ok()   { printf "\033[32m✔\033[0m %s\n" "$*"; }
warn() { printf "\033[33m⚠\033[0m %s\n" "$*"; }
die()  { printf "\033[31m✘\033[0m %s\n" "$*" >&2; exit "${2:-1}"; }
note() { printf "  %s\n" "$*"; }

cleanup() {
  if [ -n "${FLOCK_PID:-}" ] && kill -0 "$FLOCK_PID" 2>/dev/null; then
    kill "$FLOCK_PID" 2>/dev/null || true
    wait "$FLOCK_PID" 2>/dev/null || true
  fi
  if [ "${KEEP_DATA:-0}" != "1" ] && [[ "$DATA_DIR" == *"flock-e2e-"* ]]; then
    rm -rf "$DATA_DIR"
  fi
}
trap cleanup EXIT

# --- step 0: prerequisites ---
[ -x "$FLOCK_BIN" ] || die "flock binary not found at $FLOCK_BIN — run \`go build ./cmd/flock\` first" 1
command -v ollama >/dev/null || die "ollama CLI not installed — see https://ollama.com/download" 1
if ! curl -sf "http://${OLLAMA_HOST}/api/version" >/dev/null; then
  die "ollama not reachable at ${OLLAMA_HOST} — run \`ollama serve\` in another terminal" 1
fi
ok "ollama reachable at $OLLAMA_HOST"

# --- step 1: start flock up in background ---
note "starting flock up against $DATA_DIR (port $LISTEN_PORT)"
mkdir -p "$DATA_DIR"
LOG="$DATA_DIR/flock.log"
FLOCK_DATA_DIR="$DATA_DIR" \
FLOCK_OLLAMA_ENDPOINT="http://${OLLAMA_HOST}" \
FLOCK_LISTEN="127.0.0.1:${LISTEN_PORT}" \
FLOCK_REQUIRE_KEYS="false" \
"$FLOCK_BIN" up --no-wizard --no-unload-on-exit >"$LOG" 2>&1 &
FLOCK_PID=$!

# --- step 2: wait for /healthz ---
deadline=$(( $(date +%s) + 30 ))
while ! curl -sf "${GATEWAY}/healthz" >/dev/null; do
  if [ "$(date +%s)" -gt "$deadline" ]; then
    cat "$LOG" >&2
    die "flock up never came healthy in 30s — see log above" 2
  fi
  if ! kill -0 "$FLOCK_PID" 2>/dev/null; then
    cat "$LOG" >&2
    die "flock up exited early — see log above" 2
  fi
  sleep 0.5
done
ok "flock up healthy"

# --- step 3: install the model ---
note "installing $MODEL via flock model add (may pull weights)"
FLOCK_DATA_DIR="$DATA_DIR" \
FLOCK_OLLAMA_ENDPOINT="http://${OLLAMA_HOST}" \
"$FLOCK_BIN" model add "$MODEL" >/dev/null 2>&1 || die "model install failed" 3
ok "model $MODEL installed"

# --- step 4: chat completion (non-streaming) ---
RESP=$(curl -sf -X POST "${GATEWAY}/v1/chat/completions" \
  -H 'Content-Type: application/json' \
  -d "{\"model\":\"${MODEL}\",\"messages\":[{\"role\":\"user\",\"content\":\"reply with the single word OK\"}],\"max_tokens\":4}" \
) || die "chat completion request failed" 4
CONTENT=$(echo "$RESP" | sed -n 's/.*"content"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
[ -n "$CONTENT" ] || die "chat completion returned empty content. Body: $RESP" 4
ok "chat completion returned content"
note "model said: ${CONTENT:0:120}"

# --- step 5: chat completion (streaming SSE) ---
note "streaming round-trip"
SSE=$(curl -sNf -X POST "${GATEWAY}/v1/chat/completions" \
  -H 'Content-Type: application/json' \
  -d "{\"model\":\"${MODEL}\",\"messages\":[{\"role\":\"user\",\"content\":\"count 1 2 3\"}],\"max_tokens\":12,\"stream\":true}" \
) || die "streaming request failed" 5
echo "$SSE" | head -3 | grep -q '^data: ' || die "no data: lines in SSE response. Head: $(echo "$SSE" | head -3)" 5
echo "$SSE" | tail -5 | grep -q 'data: \[DONE\]' || die "missing [DONE] terminator. Tail: $(echo "$SSE" | tail -5)" 5
ok "SSE stream emitted data: lines and [DONE] terminator"

ok "single-node e2e passed — gateway + ollama + chat (sync + streaming) all green"
