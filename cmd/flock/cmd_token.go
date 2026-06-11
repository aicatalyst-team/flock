package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hadihonarvar/flock/internal/auth"
)

func cmdToken(args []string) {
	help := helpSpec{
		name:    "token",
		summary: "manage API keys and node-join tokens",
		usage:   "flock token <create [name] [--admin|--node] [--models a,b,…] | ls | edit <id> ... | revoke <id>>",
		examples: []string{
			"flock token create alice                            # user-scope key for dev `alice`",
			"flock token create alice-admin --admin              # admin-scope key (can call /admin/v1/*)",
			"flock token create alice --models qwen-coder-7b     # restrict to one model",
			"flock token create bob   --models 'claude-*,gpt-*'  # vendor families via glob",
			"flock token create --node                           # one-time join token for a new worker",
			"flock token edit k_abc123 --add-model qwen3-14b     # extend the allowlist",
			"flock token edit k_abc123 --remove-model gpt-4o     # tighten the allowlist",
			"flock token edit k_abc123 --set-models a,b,c        # replace the allowlist",
			"flock token edit k_abc123 --clear-models            # drop the allowlist (any model)",
			"flock token ls",
			"flock token revoke k_abc123",
		},
		notes: []string{
			"⚠️  --node tokens are the shared secret leader ↔ worker — only issue on a trusted network (LAN or Tailscale).",
			"`--models` accepts a comma-separated list. Entries support a `*` suffix wildcard (`claude-*`).",
			"A key with no allowlist can call any model. An empty allowlist (`--set-models ''`) denies every model.",
		},
	}
	if len(args) == 0 {
		dieHelp(help)
	}
	if wantsHelp(args) {
		showHelp(help)
	}
	switch args[0] {
	case "create":
		name := "default"
		scope := "user"
		var models []string
		for i := 1; i < len(args); i++ {
			a := args[i]
			switch a {
			case "--admin":
				scope = "admin"
			case "--node":
				scope = "node"
				if name == "default" {
					name = "node-join"
				}
			case "--models":
				if i+1 < len(args) {
					models = parseModelList(args[i+1])
					i++
				}
			default:
				if strings.HasPrefix(a, "--models=") {
					models = parseModelList(strings.TrimPrefix(a, "--models="))
					continue
				}
				if name == "default" {
					name = a
				}
			}
		}
		tokenCreate(name, scope, models)
	case "edit":
		if len(args) < 2 {
			die("usage: flock token edit <id> --add-model X | --remove-model Y | --set-models a,b,c | --clear-models")
		}
		tokenEdit(args[1], args[2:])
	case "ls", "list":
		tokenList()
	case "revoke":
		if len(args) < 2 {
			die("usage: flock token revoke <id>")
		}
		tokenRevoke(args[1])
	default:
		die("unknown subcommand: token %s", args[0])
	}
}

// parseModelList splits a comma-separated list, trims whitespace, and
// drops empties. Returns nil for an empty input — callers distinguish
// nil (no flag) from []string{} (explicit deny-all via the API).
func parseModelList(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func tokenCreate(name, scope string, models []string) {
	cfg := loadConfigOrExit()
	st := openStoreOrExit(cfg)
	defer st.Close()
	// The token's name doubles as its UserID today. Once OIDC lands the
	// UserID will come from the issuing admin's session.
	userID := name
	if scope == "node" {
		userID = "" // node tokens have no owner
	}
	plain, rec, err := auth.Generate(name, scope, userID)
	if err != nil {
		die("generate: %v", err)
	}
	rec.AllowedModels = models
	if err := st.APIKeys().Create(context.Background(), rec); err != nil {
		die("persist key: %v", err)
	}
	ok(os.Stdout, "created %s (id=%s, scope=%s)", name, rec.ID, scope)
	if len(models) > 0 {
		fmt.Printf("  allowed models: %s\n", strings.Join(models, ", "))
	}
	fmt.Println()
	fmt.Println("  Key (shown once — store it now):")
	fmt.Printf("    %s\n", plain)
}

// tokenEdit currently supports only allowlist edits — that's the one
// editable field today. Add/remove deltas are applied to the existing
// list; --set-models replaces it; --clear-models drops the restriction
// entirely.
func tokenEdit(id string, args []string) {
	cfg := loadConfigOrExit()
	st := openStoreOrExit(cfg)
	defer st.Close()
	key, err := st.APIKeys().GetByID(context.Background(), id)
	if err != nil {
		die("lookup %s: %v", id, err)
	}
	if key == nil {
		die("no such token: %s", id)
	}

	current := append([]string(nil), key.AllowedModels...)
	hadList := key.AllowedModels != nil
	var setList []string
	setExplicit := false
	clearRestriction := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--add-model":
			if i+1 >= len(args) {
				die("--add-model requires a model id")
			}
			current = appendUnique(current, args[i+1])
			hadList = true
			i++
		case "--remove-model":
			if i+1 >= len(args) {
				die("--remove-model requires a model id")
			}
			current = removeOne(current, args[i+1])
			hadList = true
			i++
		case "--set-models":
			if i+1 >= len(args) {
				die("--set-models requires a comma-separated list (use --clear-models to drop the restriction)")
			}
			setList = parseModelList(args[i+1])
			setExplicit = true
			i++
		case "--clear-models":
			clearRestriction = true
		default:
			if strings.HasPrefix(a, "--add-model=") {
				current = appendUnique(current, strings.TrimPrefix(a, "--add-model="))
				hadList = true
				continue
			}
			if strings.HasPrefix(a, "--remove-model=") {
				current = removeOne(current, strings.TrimPrefix(a, "--remove-model="))
				hadList = true
				continue
			}
			if strings.HasPrefix(a, "--set-models=") {
				setList = parseModelList(strings.TrimPrefix(a, "--set-models="))
				setExplicit = true
				continue
			}
			die("unknown flag: %s", a)
		}
	}

	var newAllowed []string
	switch {
	case clearRestriction:
		newAllowed = nil
	case setExplicit:
		if setList == nil {
			// Explicit empty list → deny all.
			newAllowed = []string{}
		} else {
			newAllowed = setList
		}
	case hadList:
		// Apply add/remove deltas. An empty list is meaningful here:
		// "you removed your only allowed model" → deny all.
		newAllowed = current
		if newAllowed == nil {
			newAllowed = []string{}
		}
	default:
		die("no edit flag given (try --add-model, --remove-model, --set-models, or --clear-models)")
	}

	if err := st.APIKeys().UpdateAllowedModels(context.Background(), id, newAllowed); err != nil {
		die("update allowed_models: %v", err)
	}
	switch {
	case newAllowed == nil:
		ok(os.Stdout, "%s: allowlist cleared (any model allowed)", id)
	case len(newAllowed) == 0:
		ok(os.Stdout, "%s: allowlist now denies every model", id)
	default:
		ok(os.Stdout, "%s: allowed models = %s", id, strings.Join(newAllowed, ", "))
	}
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

func removeOne(list []string, v string) []string {
	out := list[:0]
	for _, x := range list {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

func tokenList() {
	cfg := loadConfigOrExit()
	st := openStoreOrExit(cfg)
	defer st.Close()
	keys, err := st.APIKeys().List(context.Background())
	if err != nil {
		die("list keys: %v", err)
	}
	if len(keys) == 0 {
		fmt.Println("(no API keys — create one with `flock token create`)")
		return
	}
	fmt.Printf("%-14s %-20s %-8s %-7s %-30s %s\n", "ID", "NAME", "SCOPE", "REVOKED", "MODELS", "CREATED")
	for _, k := range keys {
		rev := "no"
		if k.Revoked {
			rev = "yes"
		}
		models := "any"
		switch {
		case k.AllowedModels == nil:
			// unrestricted; render as "any"
		case len(k.AllowedModels) == 0:
			models = "(deny all)"
		default:
			models = strings.Join(k.AllowedModels, ",")
			if len(models) > 28 {
				models = models[:27] + "…"
			}
		}
		fmt.Printf("%-14s %-20s %-8s %-7s %-30s %s\n", k.ID, k.Name, k.Scope, rev, models, k.CreatedAt.Format(time.RFC3339))
	}
}

func tokenRevoke(id string) {
	cfg := loadConfigOrExit()
	st := openStoreOrExit(cfg)
	defer st.Close()
	if err := st.APIKeys().Revoke(context.Background(), id); err != nil {
		die("revoke: %v", err)
	}
	ok(os.Stdout, "revoked %s", id)
}
