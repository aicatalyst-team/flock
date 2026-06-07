package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/hadihonarvar/flock/internal/control"
)

func cmdDisconnect(args []string) {
	fs := flag.NewFlagSet("disconnect", flag.ExitOnError)
	list := fs.Bool("list", false, "list all supported clients and exit")
	help := helpSpec{
		name:    "disconnect",
		summary: "print instructions for reverting a `flock connect` setup",
		usage:   "flock disconnect <client>   |   flock disconnect --list",
		flags:   fs,
		examples: []string{
			"flock disconnect --list                # show supported clients",
			"flock disconnect claude-code           # unset env vars; point at api.anthropic.com",
			"flock disconnect cursor                # GUI steps to clear the override",
			"flock disconnect openai-sdk            # remove base_url= from code or env",
		},
		notes: []string{
			"`disconnect` only prints instructions — it does NOT modify your shell, your",
			"editor, or any config file. Run the commands it shows when you're ready.",
			"",
			"Once disconnected, the client talks directly to its vendor (api.openai.com,",
			"api.anthropic.com) using whatever key you configure it with. Nothing about",
			"your Flock install needs to change — your API keys and audit history stay",
			"on the Flock host. You can re-run `flock connect <client>` anytime to go back.",
		},
	}
	if wantsHelp(args) {
		showHelp(help)
	}
	_ = fs.Parse(args)

	if *list {
		printClientList(os.Stdout)
		return
	}

	rest := fs.Args()
	if len(rest) == 0 {
		dieHelp(help)
	}
	if len(rest) > 1 {
		die("disconnect takes exactly one client name (got: %s)", strings.Join(rest, " "))
	}

	out, err := control.DisconnectSnippet(rest[0])
	if err != nil {
		die("%v", err)
	}
	fmt.Print(out)
}
