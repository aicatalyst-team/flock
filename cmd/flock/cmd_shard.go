package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"
)

// cmdShard dispatches `flock shard <subcommand>`.
//
//	flock shard ls                        # list all shards across all models
//	flock shard create <model> [N]        # create sharded model with N shards
//	flock shard remove <model>            # tear down a sharded model
func cmdShard(args []string) {
	if len(args) == 0 {
		die("usage: flock shard <ls|create|remove> [args]")
	}
	switch args[0] {
	case "ls", "list":
		shardLs()
	case "create":
		if len(args) < 2 {
			die("usage: flock shard create <model> [shards]")
		}
		n := 0
		if len(args) >= 3 {
			parsed, err := strconv.Atoi(args[2])
			if err != nil || parsed < 2 {
				die("invalid shard count %q (must be ≥2)", args[2])
			}
			n = parsed
		}
		shardCreate(args[1], n)
	case "remove", "rm":
		if len(args) < 2 {
			die("usage: flock shard remove <model>")
		}
		shardRemove(args[1])
	default:
		die("unknown subcommand: shard %s", args[0])
	}
}

func shardLs() {
	cfg := loadConfigOrExit()
	body, err := adminCall(context.Background(), cfg, "GET", "/admin/v1/shards", nil)
	if err != nil {
		die("%v: %s", err, string(body))
	}
	var shards []map[string]any
	_ = json.Unmarshal(body, &shards)
	if len(shards) == 0 {
		fmt.Println("(no shards — create one with `flock shard create <model>`)")
		return
	}
	fmt.Printf("%-32s %-14s %-12s %-22s %-10s\n", "MODEL", "ROLE", "NODE", "ADDRESS", "STATUS")
	for _, s := range shards {
		fmt.Printf("%-32s %-14s %-12s %-22s %-10s\n",
			fmt.Sprint(s["ModelID"]),
			fmt.Sprint(s["Role"]),
			fmt.Sprint(s["NodeID"]),
			fmt.Sprint(s["Address"]),
			fmt.Sprint(s["Status"]))
	}
}

func shardCreate(model string, n int) {
	cfg := loadConfigOrExit()
	body, _ := json.Marshal(map[string]any{"model_id": model, "shards": n})
	note(os.Stdout, "creating sharded model %s (this may take a few minutes)…", model)
	resp, err := adminCall(context.Background(), cfg, "POST", "/admin/v1/shards/create", body)
	if err != nil {
		die("%v: %s", err, string(resp))
	}
	var out map[string]string
	_ = json.Unmarshal(resp, &out)
	ok(os.Stdout, "sharded model ready: %s", out["model_id"])
}

func shardRemove(model string) {
	cfg := loadConfigOrExit()
	note(os.Stdout, "removing sharded model %s…", model)
	resp, err := adminCall(context.Background(), cfg, "DELETE", "/admin/v1/shards/"+model, nil)
	if err != nil {
		die("%v: %s", err, string(resp))
	}
	ok(os.Stdout, "removed %s", model)
}

// silence "imported and not used" if time goes unused after future edits
var _ = time.Now
