package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"
)

func cmdStatus(_ []string) {
	cfg := loadConfigOrExit()
	addr := cfg.Listen
	if addr == "" {
		addr = ":8080"
	}
	url := "http://localhost" + addr + "/healthz"

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		warn(os.Stdout, "control plane not reachable at %s: %v", url, err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	ok(os.Stdout, "control plane: %s (%s)", resp.Status, url)

	// engine
	eng := newEngineFromConfig(cfg)
	if err := eng.Health(ctx); err != nil {
		warn(os.Stdout, "engine: not reachable: %v", err)
	} else {
		ok(os.Stdout, "engine: %s ok at %s", eng.Name(), eng.Endpoint())
	}

	// installed models
	st := openStoreOrExit(cfg)
	defer st.Close()
	ms, err := st.Models().List(context.Background())
	if err != nil {
		warn(os.Stdout, "could not list installed models: %v", err)
		return
	}
	if len(ms) == 0 {
		note(os.Stdout, "no models installed yet — try `flock model add llama-3.2-3b`")
		return
	}
	fmt.Println()
	fmt.Println("  Installed models:")
	for _, m := range ms {
		fmt.Printf("    %-22s  status=%s  source=%s\n", m.CatalogID, m.Status, m.Source)
	}
}
