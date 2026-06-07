package control

import "fmt"

// DisconnectSnippet returns the reversal instructions for a client previously
// set up by ConnectSnippet — how to un-set the Flock-pointing config and
// (optionally) point the client back at the vendor with the user's own key.
//
// Snippets are intentionally static (no template substitution): there's no
// user-specific data to embed and the cleanup steps are the same for everyone.
// If a new client is added to the `clients` registry, add a matching case
// here too — DisconnectSnippet errors clearly on an unknown id so the gap
// is obvious.
func DisconnectSnippet(clientID string) (string, error) {
	for _, c := range clients {
		if c.ID == clientID {
			s, ok := disconnectSnippets[clientID]
			if !ok {
				return "", fmt.Errorf("disconnect: no reversal snippet for %q (the connect registry lists it, but disconnect.go doesn't — please update disconnectSnippets)", clientID)
			}
			return s, nil
		}
	}
	return "", fmt.Errorf("disconnect: unknown client %q (try `flock disconnect --list`)", clientID)
}

// disconnectSnippets is the per-client reversal text. Keep these in sync
// with the connect-side templates: any env var or settings field that
// connect sets should appear in the matching disconnect block.
var disconnectSnippets = map[string]string{
	"claude-code": `# Stop pointing Claude Code at Flock:
unset ANTHROPIC_BASE_URL
unset ANTHROPIC_AUTH_TOKEN
unset ANTHROPIC_MODEL

# Then either let Claude Code use its built-in API key handling,
# or point it at your own Anthropic account directly:
export ANTHROPIC_API_KEY=sk-ant-...

# Verify with:
claude --help
`,

	"cursor": `# Cursor's Flock override lives in the GUI, not env vars.
# Reverse it like this:
#
#   1. Open Cursor → Settings (Cmd/Ctrl + ,)
#   2. Models → "Override OpenAI Base URL" → toggle OFF
#      (or just clear the URL field and the model name override)
#   3. Cursor immediately resumes using its built-in billing,
#      or your own OpenAI key if you set one under API Keys.
#
# Nothing to unset on the shell side — Cursor stores its
# config in its own settings store.
`,

	"aider": `# Aider takes its base URL from CLI flags or env vars.
# Reverse the Flock override:
unset OPENAI_API_BASE
unset OPENAI_API_KEY

# Then run aider with your own vendor key:
export OPENAI_API_KEY=sk-...           # for OpenAI
# OR
export ANTHROPIC_API_KEY=sk-ant-...    # for Claude

aider                                  # picks up the key automatically
`,

	"continue": `# Continue's config lives in ~/.continue/config.json (VS Code)
# or ~/.continue/config.json on JetBrains.
#
# Open the file and remove the model entry that points at Flock.
# It looks something like this and should be deleted:
#
#   {
#     "title": "Flock",
#     "provider": "openai",
#     "model": "...",
#     "apiBase": "http://your-flock-host:8080/v1",
#     "apiKey": "sk-orc-..."
#   }
#
# Continue picks up the change the next time you open the panel.
`,

	"zed": `# Zed reads its model config from settings.json
# (Cmd/Ctrl + , in the editor, or ~/.config/zed/settings.json).
#
# Find and remove the assistant block that points at Flock:
#
#   "assistant": {
#     "default_model": { ... pointing at Flock ... }
#   }
#
# Zed will fall back to the default OpenAI configuration —
# add your own OPENAI_API_KEY if you want to use vendor directly.
`,

	"cline": `# Cline / Roo Code stores config in the VS Code settings panel.
#
#   1. Open VS Code → Settings (Cmd/Ctrl + ,)
#   2. Search "cline" (or "roo")
#   3. Clear the "API Base URL" and "API Key" fields you set for Flock.
#   4. Pick a provider (Anthropic / OpenAI) and paste your own key.
#
# Cline picks up the change on the next message.
`,

	"qwen-code": `# Qwen-Code uses the same Anthropic-compatible env vars as Claude Code.
# Reverse the Flock override:
unset ANTHROPIC_BASE_URL
unset ANTHROPIC_AUTH_TOKEN
unset ANTHROPIC_MODEL

# Then point it at your own Anthropic account if you want vendor:
export ANTHROPIC_API_KEY=sk-ant-...

qwen-code --help
`,

	"openai-sdk": `# If you set base_url in your Python/JS code, remove that argument:
#
#   # before
#   client = OpenAI(base_url="http://your-flock-host:8080/v1", api_key="sk-orc-...")
#   # after
#   client = OpenAI()                            # uses OPENAI_API_KEY env var
#
# If you used the env-var form, unset them:
unset OPENAI_BASE_URL
unset OPENAI_API_KEY

# Then set your own:
export OPENAI_API_KEY=sk-...
`,

	"anthropic-sdk": `# If you set base_url in your Python/JS code, remove that argument:
#
#   # before
#   client = Anthropic(base_url="http://your-flock-host:8080", auth_token="sk-orc-...")
#   # after
#   client = Anthropic()                         # uses ANTHROPIC_API_KEY env var
#
# If you used the env-var form, unset them:
unset ANTHROPIC_BASE_URL
unset ANTHROPIC_AUTH_TOKEN

# Then set your own:
export ANTHROPIC_API_KEY=sk-ant-...
`,

	"curl": `# Just point curl at the vendor URL with your own key, no Flock cleanup needed:

# Anthropic:
curl https://api.anthropic.com/v1/messages \
  -H "x-api-key: sk-ant-..." \
  -H "anthropic-version: 2023-06-01" \
  -H "content-type: application/json" \
  -d '{"model":"claude-sonnet-4-6","max_tokens":256,"messages":[{"role":"user","content":"hi"}]}'

# OpenAI:
curl https://api.openai.com/v1/chat/completions \
  -H "Authorization: Bearer sk-..." \
  -H "content-type: application/json" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}'
`,
}
