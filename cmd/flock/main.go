// Command flock is the single binary that runs Flock in all of its modes:
// leader (flock up), worker (flock join + flock up), and one-shot CLI.
//
// All subcommands live in cmd/flock/cmd_*.go alongside this file.
package main

import (
	"fmt"
	"io"
	"os"
)

// version is overwritten at link time via -ldflags.
var version = "0.1.0-dev"

func main() {
	if len(os.Args) < 2 {
		printUsage(os.Stderr)
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	switch cmd {
	case "version", "--version", "-v":
		cmdVersion(args)
	case "up":
		cmdUp(args)
	case "down":
		cmdDown(args)
	case "status":
		cmdStatus(args)
	case "join":
		cmdJoin(args)
	case "node":
		cmdNode(args)
	case "model":
		cmdModel(args)
	case "shard":
		cmdShard(args)
	case "token":
		cmdToken(args)
	case "doctor":
		cmdDoctor(args)
	case "help", "--help", "-h":
		printUsage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "flock: unknown command %q\n\n", cmd)
		printUsage(os.Stderr)
		os.Exit(2)
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `flock — orchestrate open-weight LLMs across your machines

Usage:
  flock <command> [options]

Commands:
  up                       Start the local node (leader on first run)
  down                     Stop the local node
  status                   Show local node and cluster status
  join <url>?token=...     Join an existing cluster as a worker
  node ls                  List nodes
  node show <id>           Show one node
  node drain <id>          Mark node as draining
  node remove <id>         Remove a node
  model add <id>           Install a model from the catalog
  model ls                 List installed models
  model search [q]         Search the catalog
  model remove <id>        Uninstall a model
  shard create <model> [N] Orchestrate a sharded model across N workers
  shard ls                 List shards
  shard remove <model>     Tear down a sharded model
  token create [name]      Issue an API key (--admin, --node)
  token ls                 List API keys
  token revoke <id>        Revoke an API key
  doctor                   Diagnose common problems
  version                  Print version
  help                     Show this help

Run 'flock <command> --help' for command-specific options.
Docs: https://github.com/hadihonarvar/flock`)
}
