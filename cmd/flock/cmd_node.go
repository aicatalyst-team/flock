package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// cmdNode dispatches `flock node <subcommand>`. All operations call the
// admin API on the leader (local by default).
func cmdNode(args []string) {
	if len(args) == 0 {
		die("usage: flock node <ls|show|drain|remove> [id]")
	}
	switch args[0] {
	case "ls", "list":
		nodeLs()
	case "show":
		if len(args) < 2 {
			die("usage: flock node show <id>")
		}
		nodeShow(args[1])
	case "drain":
		if len(args) < 2 {
			die("usage: flock node drain <id>")
		}
		nodeDrain(args[1])
	case "remove", "rm":
		if len(args) < 2 {
			die("usage: flock node remove <id>")
		}
		nodeRemove(args[1])
	default:
		die("unknown subcommand: node %s", args[0])
	}
}

func nodeLs() {
	cfg := loadConfigOrExit()
	st := openStoreOrExit(cfg)
	defer st.Close()
	nodes, err := st.Nodes().List(context.Background())
	if err != nil {
		die("list nodes: %v", err)
	}
	if len(nodes) == 0 {
		fmt.Println("(no nodes registered yet — issue a join token: `flock token create --node`)")
		return
	}
	fmt.Printf("%-14s %-20s %-12s %-22s %-10s %s\n", "ID", "HOSTNAME", "OS/ARCH", "ADDRESS", "STATE", "LAST HB")
	for _, n := range nodes {
		fmt.Printf("%-14s %-20s %-12s %-22s %-10s %s\n",
			n.ID, n.Hostname, n.OS+"/"+n.Arch, n.Address, n.State,
			n.LastHeartbeat.Format(time.RFC3339))
	}
}

func nodeShow(id string) {
	cfg := loadConfigOrExit()
	st := openStoreOrExit(cfg)
	defer st.Close()
	n, err := st.Nodes().Get(context.Background(), id)
	if err != nil {
		die("get node: %v", err)
	}
	if n == nil {
		die("no such node: %s", id)
	}
	b, _ := json.MarshalIndent(n, "", "  ")
	fmt.Println(string(b))
}

func nodeDrain(id string) {
	cfg := loadConfigOrExit()
	st := openStoreOrExit(cfg)
	defer st.Close()
	n, err := st.Nodes().Get(context.Background(), id)
	if err != nil || n == nil {
		die("no such node: %s", id)
	}
	n.State = "draining"
	if err := st.Nodes().Upsert(context.Background(), *n); err != nil {
		die("update node: %v", err)
	}
	ok(os.Stdout, "marked %s as draining", id)
}

func nodeRemove(id string) {
	cfg := loadConfigOrExit()
	st := openStoreOrExit(cfg)
	defer st.Close()
	if err := st.Nodes().Delete(context.Background(), id); err != nil {
		die("delete node: %v", err)
	}
	ok(os.Stdout, "removed %s", id)
}
