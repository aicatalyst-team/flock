package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/hadihonarvar/flock/internal/auth"
)

func cmdToken(args []string) {
	help := helpSpec{
		name:    "token",
		summary: "manage API keys and node-join tokens",
		usage:   "flock token <create [name] [--admin|--node] | ls | revoke <id>>",
		examples: []string{
			"flock token create alice                  # user-scope key for dev `alice`",
			"flock token create alice-admin --admin    # admin-scope key (can call /admin/v1/*)",
			"flock token create --node                 # one-time join token for a new worker",
			"flock token ls",
			"flock token revoke k_abc123",
		},
		notes: []string{
			"⚠️  --node tokens are the shared secret leader ↔ worker — only issue on a trusted network (LAN or Tailscale).",
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
		for i := 1; i < len(args); i++ {
			switch args[i] {
			case "--admin":
				scope = "admin"
			case "--node":
				scope = "node"
				if name == "default" {
					name = "node-join"
				}
			default:
				if name == "default" {
					name = args[i]
				}
			}
		}
		tokenCreate(name, scope)
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

func tokenCreate(name, scope string) {
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
	if err := st.APIKeys().Create(context.Background(), rec); err != nil {
		die("persist key: %v", err)
	}
	ok(os.Stdout, "created %s (id=%s, scope=%s)", name, rec.ID, scope)
	fmt.Println()
	fmt.Println("  Key (shown once — store it now):")
	fmt.Printf("    %s\n", plain)
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
	fmt.Printf("%-14s %-20s %-8s %-7s %s\n", "ID", "NAME", "SCOPE", "REVOKED", "CREATED")
	for _, k := range keys {
		rev := "no"
		if k.Revoked {
			rev = "yes"
		}
		fmt.Printf("%-14s %-20s %-8s %-7s %s\n", k.ID, k.Name, k.Scope, rev, k.CreatedAt.Format(time.RFC3339))
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
