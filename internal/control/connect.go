package control

import (
	"bytes"
	"embed"
	"fmt"
	"sort"
	"strings"
	"text/template"
)

//go:embed snippets/*.tmpl
var snippetFS embed.FS

// Client describes one supported tool that can be wired up to Flock.
type Client struct {
	ID          string // stable identifier, kebab-case
	Protocol    string // "OpenAI" | "Anthropic" | "Both" | "Raw HTTP"
	Description string // one-line "what is this"
}

// clients is the registry of supported tools. Order here is the order
// they appear in `flock connect --list` and in invite share cards.
var clients = []Client{
	{ID: "claude-code", Protocol: "Anthropic", Description: "Anthropic's official CLI coding agent"},
	{ID: "cursor", Protocol: "OpenAI", Description: "IDE with built-in AI"},
	{ID: "aider", Protocol: "OpenAI", Description: "Terminal-based AI pair programmer"},
	{ID: "continue", Protocol: "Both", Description: "VS Code / JetBrains AI assistant"},
	{ID: "zed", Protocol: "OpenAI", Description: "Fast multiplayer code editor"},
	{ID: "cline", Protocol: "Both", Description: "VS Code AI extension (Cline / Roo Code)"},
	{ID: "qwen-code", Protocol: "Anthropic", Description: "Open-source Claude Code fork"},
	{ID: "openai-sdk", Protocol: "OpenAI", Description: "OpenAI Python/JS SDK"},
	{ID: "anthropic-sdk", Protocol: "Anthropic", Description: "Anthropic Python/JS SDK"},
	{ID: "curl", Protocol: "Raw HTTP", Description: "Direct HTTP calls for testing"},
}

// Clients returns the list of supported clients in display order. Callers
// should treat the slice as read-only.
func Clients() []Client {
	out := make([]Client, len(clients))
	copy(out, clients)
	return out
}

// ConnectInput is the request to render a per-client config snippet.
type ConnectInput struct {
	Client  string // one of the IDs returned by Clients()
	BaseURL string // e.g. "http://localhost:8080" (no trailing slash)
	Token   string // Flock API key, plaintext (sk-orc-…)
	Model   string // model id to suggest in the snippet; defaults to "auto"
}

// ConnectOutput is the rendered snippet plus metadata about the client.
type ConnectOutput struct {
	Client      Client
	Snippet     string
	BaseURL     string
	Token       string
	Model       string
}

// ConnectSnippet renders the embedded template for the named client with
// the caller's base URL + token substituted in. Returns an error if the
// client is unknown or the template can't be parsed.
func ConnectSnippet(in ConnectInput) (*ConnectOutput, error) {
	in.BaseURL = strings.TrimRight(in.BaseURL, "/")
	if in.BaseURL == "" {
		return nil, fmt.Errorf("base URL is required")
	}
	if in.Token == "" {
		return nil, fmt.Errorf("token is required")
	}
	if in.Model == "" {
		in.Model = "auto"
	}

	var matched *Client
	for i := range clients {
		if clients[i].ID == in.Client {
			matched = &clients[i]
			break
		}
	}
	if matched == nil {
		return nil, fmt.Errorf("unknown client %q (run `flock connect --list` for supported clients)", in.Client)
	}

	tmplBytes, err := snippetFS.ReadFile("snippets/" + matched.ID + ".tmpl")
	if err != nil {
		return nil, fmt.Errorf("snippet template missing for %q: %w", matched.ID, err)
	}

	tmpl, err := template.New(matched.ID).Parse(string(tmplBytes))
	if err != nil {
		return nil, fmt.Errorf("snippet template parse for %q: %w", matched.ID, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, struct {
		BaseURL string
		Token   string
		Model   string
	}{in.BaseURL, in.Token, in.Model}); err != nil {
		return nil, fmt.Errorf("snippet template render for %q: %w", matched.ID, err)
	}

	return &ConnectOutput{
		Client:  *matched,
		Snippet: buf.String(),
		BaseURL: in.BaseURL,
		Token:   in.Token,
		Model:   in.Model,
	}, nil
}

// ClientIDs returns just the IDs in display order, useful for completion
// and list views.
func ClientIDs() []string {
	out := make([]string, len(clients))
	for i := range clients {
		out[i] = clients[i].ID
	}
	return out
}

// SortedClientIDs returns the IDs in alphabetical order, useful when the
// caller wants a deterministic listing independent of display order.
func SortedClientIDs() []string {
	out := ClientIDs()
	sort.Strings(out)
	return out
}
