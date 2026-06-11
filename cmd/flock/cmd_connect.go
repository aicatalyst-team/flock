package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/hadihonarvar/flock/internal/control"
)

func cmdConnect(args []string) {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	var (
		list    = fs.Bool("list", false, "list all supported clients and exit")
		baseURL = fs.String("base-url", "", "Flock base URL to embed in the snippet (default: cfg.ExternalURL or http://localhost:<listen>)")
		token   = fs.String("token", "", "API key to embed in the snippet (default: $FLOCK_TOKEN, then ~/.flock/admin.key)")
		model   = fs.String("model", "auto", "model id to suggest in the snippet")
		retries = fs.Int("retries", 0, "ask the gateway to retry each candidate up to N times (1-5) — emits X-Flock-Num-Retries in the snippet")
	)
	help := helpSpec{
		name:    "connect",
		summary: "print copy-paste configuration for a tool (Claude Code, Cursor, Aider, …)",
		usage:   "flock connect <client>   |   flock connect --list",
		flags:   fs,
		examples: []string{
			"flock connect --list                # show supported clients",
			"flock connect claude-code           # print env vars for Claude Code",
			"flock connect cursor                # print Cursor settings",
			"flock connect openai-sdk            # print Python SDK snippet",
			"flock connect curl                  # print a smoke-test curl",
			"flock connect cursor --model qwen-coder-14b",
			"flock connect curl --retries 3            # ask the gateway to retry 3× per candidate",
			"FLOCK_TOKEN=sk-orc-... flock connect aider",
		},
		notes: []string{
			"The base URL defaults to cfg.ExternalURL if set in ~/.flock/config.yaml,",
			"otherwise http://localhost:<listen>. Override per-call with --base-url.",
			"",
			"The token defaults to $FLOCK_TOKEN, then the admin key saved when you",
			"ran `flock up`. Override per-call with --token.",
		},
	}
	if wantsHelp(args) {
		showHelp(help)
	}
	args = reorderFlagsFirst(args, map[string]bool{
		"-base-url": true, "--base-url": true,
		"-token": true, "--token": true,
		"-model": true, "--model": true,
		"-retries": true, "--retries": true,
	})
	_ = fs.Parse(args)

	if *list {
		printClientList(os.Stdout)
		return
	}

	rest := fs.Args()
	if len(rest) > 1 {
		die("connect takes exactly one client name (got: %s)", strings.Join(rest, " "))
	}
	clientID := ""
	if len(rest) == 1 {
		clientID = rest[0]
	}
	if clientID == "" || !connectHasClient(clientID) {
		clientID = pickConnectClient("Pick a client to print a snippet for:", clientID)
		if clientID == "" {
			dieHelp(help)
		}
	}

	cfg := loadConfigOrExit()
	resolvedURL := resolveBaseURL(cfg, *baseURL)
	resolvedToken := resolveToken(cfg, *token)
	if resolvedToken == "" {
		die("no token available — set $FLOCK_TOKEN, pass --token <key>, or run `flock up` to create one")
	}

	out, err := control.ConnectSnippet(control.ConnectInput{
		Client:  clientID,
		BaseURL: resolvedURL,
		Token:   resolvedToken,
		Model:   *model,
		Retries: *retries,
	})
	if err != nil {
		die("%v", err)
	}
	fmt.Print(out.Snippet)
}

func connectHasClient(id string) bool {
	for _, c := range control.Clients() {
		if c.ID == id {
			return true
		}
	}
	return false
}

func pickConnectClient(prompt, seed string) string {
	cs := control.Clients()
	items := make([]pickerItem, 0, len(cs))
	for _, c := range cs {
		items = append(items, pickerItem{
			ID:    c.ID,
			Label: c.ID,
			Meta:  c.Protocol + " · " + c.Description,
		})
	}
	return pickFromList(prompt, items, seed)
}

func printClientList(w *os.File) {
	cs := control.Clients()
	fmt.Fprintln(w, "Supported clients:")
	fmt.Fprintln(w)
	// Find longest ID for column alignment.
	maxLen := 0
	for _, c := range cs {
		if l := len(c.ID); l > maxLen {
			maxLen = l
		}
	}
	for _, c := range cs {
		pad := strings.Repeat(" ", maxLen-len(c.ID))
		fmt.Fprintf(w, "  %s%s  · %s · %s\n", c.ID, pad, c.Protocol, c.Description)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Print a config snippet for any of these:")
	fmt.Fprintln(w, "  flock connect <client>")
}
