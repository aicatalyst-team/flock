# Reference Grafana dashboards

Importable JSON for the Prometheus metrics that `flock` exposes at `/metrics`.

| File | What it shows |
|---|---|
| `cluster-overview.json` | Total RPS, p50/p95/p99 latency across all models, error rate, tokens/s (prompt vs completion), nodes up, loaded-model inventory |
| `per-model.json` | Same questions filtered to one model (selectable from a Grafana template variable) — useful for capacity planning or debugging a hot model |
| `per-node.json` | Per-node fleet view — which nodes are up, how many models each is hosting, full loaded-model table |

All three render against the metrics declared in `internal/metrics/metrics.go`:

- `flock_requests_total{model,protocol,outcome}` — counter
- `flock_request_duration_seconds{model,protocol,outcome}` — histogram
- `flock_request_tokens_total{model,direction}` — counter (`direction` is `prompt`|`completion`)
- `flock_model_loaded{model,node}` — gauge (0/1)
- `flock_node_up{node,hostname}` — gauge (0/1)

## Importing

1. In Grafana, **Dashboards → New → Import**.
2. Upload the JSON file or paste its contents.
3. When prompted for a Prometheus data source, pick the one that scrapes your Flock leader's `/metrics`.

That's it — no edits needed. The dashboards use a `${DS_PROMETHEUS}` variable so they bind to whichever data source you pick.

## Scraping Flock

Minimal Prometheus scrape config (`prometheus.yml`):

```yaml
scrape_configs:
  - job_name: flock
    metrics_path: /metrics
    scrape_interval: 15s
    static_configs:
      - targets: ['localhost:8080']
```

If your Flock leader requires an API key on the metrics endpoint (set `auth.require_keys: true` in `~/.flock/config.yaml`), add a bearer header:

```yaml
    authorization:
      type: Bearer
      credentials: sk-orc-...
```

## Compatibility

Tested against Grafana 10.x and 11.x with `schemaVersion: 39`. Older Grafana (≤ 9.x) may need a one-time export-and-reimport to upgrade the schema.

## Modifying

These are intentionally minimal — five metrics, three dashboards. Fork them if you want richer cuts (per-protocol breakdowns, alert rules, multi-cluster mixins). Upstream PRs welcome if you find a panel that's broadly useful.
