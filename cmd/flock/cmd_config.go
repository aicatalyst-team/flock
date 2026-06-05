package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

// cmdConfig shows the effective runtime config (secrets redacted) and
// surfaces the file path + env-var overrides admins need to know.
//
//	flock config show       # default
//	flock config path       # print the config file path
//	flock config edit       # open in $EDITOR
func cmdConfig(args []string) {
	if wantsHelp(args) {
		showHelp(helpSpec{
			name:    "config",
			summary: "view + locate the effective runtime config",
			usage:   "flock config <show [--json] | path | edit>",
			examples: []string{
				"flock config show              # effective config, secrets redacted",
				"flock config show --json       # same, JSON output",
				"flock config path              # print the config file path",
				"flock config edit              # print the editor command",
			},
			notes: []string{
				"Config is YAML at ~/.flock/config.yaml. Secrets (vendor keys, worker tokens) come from env vars.",
			},
		})
	}
	if len(args) == 0 {
		args = []string{"show"}
	}
	switch args[0] {
	case "show":
		configShow(args[1:])
	case "path":
		cfg := loadConfigOrExit()
		fmt.Println(cfg.DataDir + "/config.yaml")
	case "edit":
		cfg := loadConfigOrExit()
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vi"
		}
		fmt.Printf("Run: %s %s/config.yaml\n", editor, cfg.DataDir)
		fmt.Println("(then restart flock for changes to take effect)")
	default:
		die("unknown subcommand: config %s (try show|path|edit)", args[0])
	}
}

func configShow(args []string) {
	fs := flag.NewFlagSet("config show", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print as JSON instead of pretty key=value lines")
	_ = fs.Parse(args)

	cfg := loadConfigOrExit()
	body, err := adminCall(context.Background(), cfg, "GET", "/admin/v1/config", nil)
	if err != nil {
		// Fall back to local file read (works even when leader isn't running)
		fmt.Fprintln(os.Stderr, "(leader not reachable; showing config loaded by this CLI)")
		printConfigLocal(cfg, *asJSON)
		return
	}
	if *asJSON {
		fmt.Println(string(body))
		return
	}
	var v map[string]any
	_ = json.Unmarshal(body, &v)
	printConfigPretty("", v, 0)
	fmt.Println()
	if hint, ok := v["edit_hint"].(string); ok && hint != "" {
		fmt.Println(hint)
	}
}

func printConfigPretty(prefix string, v any, depth int) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			if k == "edit_hint" {
				continue
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			child := t[k]
			label := k
			if prefix != "" {
				label = prefix + "." + k
			}
			switch child.(type) {
			case map[string]any:
				printConfigPretty(label, child, depth+1)
			default:
				fmt.Printf("  %-40s %v\n", label, child)
			}
		}
	default:
		fmt.Printf("%v\n", t)
	}
}

func printConfigLocal(cfg any, asJSON bool) {
	b, _ := json.MarshalIndent(cfg, "", "  ")
	if asJSON {
		fmt.Println(string(b))
		return
	}
	// minimal pretty for local fallback
	out := strings.ReplaceAll(string(b), "\"", "")
	fmt.Println(out)
}
