package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// Agent is the per-node loop on a worker. It registers with the leader on
// startup, then sends a heartbeat at HeartbeatInterval.
type Agent struct {
	NodeID       string
	LeaderURL    string
	Token        string
	Address      string
	Capabilities Capabilities

	HTTP              *http.Client
	HeartbeatInterval time.Duration
	Log               *slog.Logger
}

// Register POSTs node info to /admin/v1/nodes/register on the leader.
func (a *Agent) Register(ctx context.Context) error {
	body, _ := json.Marshal(map[string]any{
		"id":            a.NodeID,
		"hostname":      a.Capabilities.Hostname,
		"os":            a.Capabilities.OS,
		"arch":          a.Capabilities.Arch,
		"ram_gb":        a.Capabilities.RAMGB,
		"address":       a.Address,
		"hardware_json": mustJSON(a.Capabilities),
	})
	return a.post(ctx, "/admin/v1/nodes/register", body)
}

// Heartbeat sends a lightweight ping to keep the leader informed we're alive.
func (a *Agent) Heartbeat(ctx context.Context) error {
	body, _ := json.Marshal(map[string]any{"id": a.NodeID})
	return a.post(ctx, "/admin/v1/nodes/heartbeat", body)
}

// Loop blocks running register + periodic heartbeat until ctx is done.
func (a *Agent) Loop(ctx context.Context) error {
	if a.HTTP == nil {
		a.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	if a.HeartbeatInterval == 0 {
		a.HeartbeatInterval = 5 * time.Second
	}
	if err := a.Register(ctx); err != nil {
		a.Log.Warn("register failed", "err", err)
	} else {
		a.Log.Info("registered with leader", "leader", a.LeaderURL, "node", a.NodeID)
	}
	t := time.NewTicker(a.HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := a.Heartbeat(ctx); err != nil {
				a.Log.Warn("heartbeat failed", "err", err)
			}
		}
	}
}

func (a *Agent) post(ctx context.Context, path string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.LeaderURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.Token)
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s %s: %s: %s", req.Method, path, resp.Status, string(b))
	}
	return nil
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
