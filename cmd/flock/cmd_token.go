package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
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
			"flock token create alice --rpm 60 --tpm 100000      # per-minute ceilings",
			"flock token create --node                           # one-time join token for a new worker",
			"flock token edit k_abc123 --add-model qwen3-14b     # extend the allowlist",
			"flock token edit k_abc123 --remove-model gpt-4o     # tighten the allowlist",
			"flock token edit k_abc123 --set-models a,b,c        # replace the allowlist",
			"flock token edit k_abc123 --clear-models            # drop the allowlist (any model)",
			"flock token edit k_abc123 --rpm 30 --tpm 50000      # set per-minute ceilings (0 = unlimited)",
			"flock token budget add k_abc --window month --limit 100 --unit usd  # $100/mo cap",
			"flock token budget add k_abc --window day --limit 1000000 --unit tokens",
			"flock token budget ls k_abc                         # show active budgets + utilization",
			"flock token budget rm k_abc 4                       # drop budget #4",
			"flock token ls",
			"flock token revoke k_abc123",
		},
		notes: []string{
			"⚠️  --node tokens are the shared secret leader ↔ worker — only issue on a trusted network (LAN or Tailscale).",
			"`--models` accepts a comma-separated list. Entries support a `*` suffix wildcard (`claude-*`).",
			"A key with no allowlist can call any model. An empty allowlist (`--set-models ''`) denies every model.",
			"`--rpm` (requests/min) and `--tpm` (tokens/min) are in-memory leaky buckets; reset on leader restart. 0 = unlimited.",
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
		rpm, tpm := 0, 0
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
			case "--rpm":
				if i+1 < len(args) {
					rpm = parseIntFlag(args[i+1], "--rpm")
					i++
				}
			case "--tpm":
				if i+1 < len(args) {
					tpm = parseIntFlag(args[i+1], "--tpm")
					i++
				}
			default:
				if strings.HasPrefix(a, "--models=") {
					models = parseModelList(strings.TrimPrefix(a, "--models="))
					continue
				}
				if strings.HasPrefix(a, "--rpm=") {
					rpm = parseIntFlag(strings.TrimPrefix(a, "--rpm="), "--rpm")
					continue
				}
				if strings.HasPrefix(a, "--tpm=") {
					tpm = parseIntFlag(strings.TrimPrefix(a, "--tpm="), "--tpm")
					continue
				}
				if name == "default" {
					name = a
				}
			}
		}
		tokenCreate(name, scope, models, rpm, tpm)
	case "edit":
		if len(args) < 2 {
			die("usage: flock token edit <id> --add-model X | --remove-model Y | --set-models a,b,c | --clear-models")
		}
		tokenEdit(args[1], args[2:])
	case "budget":
		tokenBudget(args[1:])
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

// parseIntFlag parses a non-negative integer for a token-create flag.
// Centralized so the error message stays consistent ("invalid --rpm",
// not whatever strconv defaults to).
func parseIntFlag(s, flag string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		die("invalid %s: %q (expected a non-negative integer)", flag, s)
	}
	return n
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

func tokenCreate(name, scope string, models []string, rpm, tpm int) {
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
	rec.RPMLimit = rpm
	rec.TPMLimit = tpm
	if err := st.APIKeys().Create(context.Background(), rec); err != nil {
		die("persist key: %v", err)
	}
	ok(os.Stdout, "created %s (id=%s, scope=%s)", name, rec.ID, scope)
	if len(models) > 0 {
		fmt.Printf("  allowed models: %s\n", strings.Join(models, ", "))
	}
	if rpm > 0 || tpm > 0 {
		fmt.Printf("  rpm: %s · tpm: %s\n", limitStr(rpm), limitStr(tpm))
	}
	fmt.Println()
	fmt.Println("  Key (shown once — store it now):")
	fmt.Printf("    %s\n", plain)
}

func limitStr(n int) string {
	if n <= 0 {
		return "∞"
	}
	return fmt.Sprintf("%d", n)
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
	rpm, tpm := key.RPMLimit, key.TPMLimit
	rpmSet, tpmSet := false, false
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
		case "--rpm":
			if i+1 >= len(args) {
				die("--rpm requires a value (0 = unlimited)")
			}
			rpm = parseIntFlag(args[i+1], "--rpm")
			rpmSet = true
			i++
		case "--tpm":
			if i+1 >= len(args) {
				die("--tpm requires a value (0 = unlimited)")
			}
			tpm = parseIntFlag(args[i+1], "--tpm")
			tpmSet = true
			i++
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
			if strings.HasPrefix(a, "--rpm=") {
				rpm = parseIntFlag(strings.TrimPrefix(a, "--rpm="), "--rpm")
				rpmSet = true
				continue
			}
			if strings.HasPrefix(a, "--tpm=") {
				tpm = parseIntFlag(strings.TrimPrefix(a, "--tpm="), "--tpm")
				tpmSet = true
				continue
			}
			die("unknown flag: %s", a)
		}
	}

	allowlistChange := clearRestriction || setExplicit || hadList
	rateChange := rpmSet || tpmSet
	if !allowlistChange && !rateChange {
		die("no edit flag given (try --add-model, --remove-model, --set-models, --clear-models, --rpm, --tpm)")
	}

	if allowlistChange {
		var newAllowed []string
		switch {
		case clearRestriction:
			newAllowed = nil
		case setExplicit:
			if setList == nil {
				newAllowed = []string{} // explicit empty = deny all
			} else {
				newAllowed = setList
			}
		case hadList:
			newAllowed = current
			if newAllowed == nil {
				newAllowed = []string{}
			}
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
	if rateChange {
		if err := st.APIKeys().UpdateRateLimits(context.Background(), id, rpm, tpm); err != nil {
			die("update rate limits: %v", err)
		}
		ok(os.Stdout, "%s: rpm = %s · tpm = %s", id, limitStr(rpm), limitStr(tpm))
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
	fmt.Printf("%-14s %-20s %-8s %-7s %-8s %-8s %-30s %s\n", "ID", "NAME", "SCOPE", "REVOKED", "RPM", "TPM", "MODELS", "CREATED")
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
		fmt.Printf("%-14s %-20s %-8s %-7s %-8s %-8s %-30s %s\n",
			k.ID, k.Name, k.Scope, rev,
			limitStr(k.RPMLimit), limitStr(k.TPMLimit),
			models, k.CreatedAt.Format(time.RFC3339))
	}
}

// tokenBudget dispatches the `flock token budget` subcommands. All
// three (add/ls/rm) hit the admin API rather than the store directly
// so they work against a remote leader the same way the dashboard
// does.
func tokenBudget(args []string) {
	if len(args) == 0 {
		die("usage: flock token budget <add|ls|rm> <key-id> ...")
	}
	switch args[0] {
	case "add":
		if len(args) < 2 {
			die("usage: flock token budget add <key-id> --window day|week|month --limit N --unit tokens|usd")
		}
		tokenBudgetAdd(args[1], args[2:])
	case "ls", "list":
		if len(args) < 2 {
			die("usage: flock token budget ls <key-id>")
		}
		tokenBudgetList(args[1])
	case "rm", "remove", "delete":
		if len(args) < 3 {
			die("usage: flock token budget rm <key-id> <budget-id>")
		}
		tokenBudgetRemove(args[1], args[2])
	default:
		die("unknown subcommand: token budget %s", args[0])
	}
}

func tokenBudgetAdd(keyID string, args []string) {
	window, unit := "", ""
	var limit float64
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--window":
			if i+1 < len(args) {
				window = args[i+1]
				i++
			}
		case "--limit":
			if i+1 < len(args) {
				v, err := strconv.ParseFloat(args[i+1], 64)
				if err != nil {
					die("invalid --limit %q", args[i+1])
				}
				limit = v
				i++
			}
		case "--unit":
			if i+1 < len(args) {
				unit = args[i+1]
				i++
			}
		default:
			if strings.HasPrefix(a, "--window=") {
				window = strings.TrimPrefix(a, "--window=")
				continue
			}
			if strings.HasPrefix(a, "--limit=") {
				v, err := strconv.ParseFloat(strings.TrimPrefix(a, "--limit="), 64)
				if err != nil {
					die("invalid --limit %q", a)
				}
				limit = v
				continue
			}
			if strings.HasPrefix(a, "--unit=") {
				unit = strings.TrimPrefix(a, "--unit=")
				continue
			}
			die("unknown flag: %s", a)
		}
	}
	if window == "" || unit == "" || limit <= 0 {
		die("--window, --limit (>0), and --unit are all required")
	}
	cfg := loadConfigOrExit()
	body := []byte(fmt.Sprintf(`{"window":%q,"limit_unit":%q,"limit_value":%v}`, window, unit, limit))
	resp, err := adminCall(context.Background(), cfg, "POST", "/admin/v1/tokens/"+keyID+"/budgets", body)
	if err != nil {
		die("%v: %s", err, string(resp))
	}
	ok(os.Stdout, "added %s/%s budget — limit %v (key %s)", window, unit, limit, keyID)
}

func tokenBudgetList(keyID string) {
	cfg := loadConfigOrExit()
	body, err := adminCall(context.Background(), cfg, "GET", "/admin/v1/tokens/"+keyID+"/budgets", nil)
	if err != nil {
		die("%v: %s", err, string(body))
	}
	type budgetView struct {
		ID           int64     `json:"ID"`
		Window       string    `json:"Window"`
		LimitUnit    string    `json:"LimitUnit"`
		LimitValue   float64   `json:"LimitValue"`
		CurrentValue float64   `json:"CurrentValue"`
		ResetAt      time.Time `json:"ResetAt"`
	}
	var bs []budgetView
	if err := json.Unmarshal(body, &bs); err != nil {
		die("decode budgets: %v", err)
	}
	if len(bs) == 0 {
		fmt.Println("(no budgets attached)")
		return
	}
	fmt.Printf("%-4s %-8s %-8s %15s %15s %-6s %s\n", "ID", "WINDOW", "UNIT", "LIMIT", "CURRENT", "USED", "RESETS")
	for _, b := range bs {
		pct := 0.0
		if b.LimitValue > 0 {
			pct = 100 * b.CurrentValue / b.LimitValue
		}
		fmt.Printf("%-4d %-8s %-8s %15s %15s %5.1f%% %s\n",
			b.ID, b.Window, b.LimitUnit,
			fmt.Sprintf("%.4f", b.LimitValue),
			fmt.Sprintf("%.4f", b.CurrentValue),
			pct, b.ResetAt.Format(time.RFC3339))
	}
}

func tokenBudgetRemove(keyID, budgetID string) {
	cfg := loadConfigOrExit()
	body, err := adminCall(context.Background(), cfg, "DELETE",
		fmt.Sprintf("/admin/v1/tokens/%s/budgets/%s", keyID, budgetID), nil)
	if err != nil {
		die("%v: %s", err, string(body))
	}
	ok(os.Stdout, "removed budget %s from key %s", budgetID, keyID)
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
