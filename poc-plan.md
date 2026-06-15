# PoC Plan: Flock

## Project Classification
- **Type:** api-service (LLM gateway/control plane)
- **Key Technologies:** Go 1.25, chi router, SQLite (pure Go), OpenAI/Anthropic API compatibility, Prometheus metrics, OpenTelemetry tracing
- **ODH Relevance:** Flock serves as a self-hosted LLM gateway that can front-end inference servers deployed on OpenShift AI (vLLM, Ollama). It provides API key management, usage quotas, audit logging, and a unified API endpoint — capabilities that complement RHOAI model serving.

## PoC Objectives
1. Verify that Flock can be containerized as a UBI-based single-binary Go application and deployed on OpenShift
2. Demonstrate that the admin dashboard and OpenAI/Anthropic-compatible API endpoints are accessible via OpenShift Services
3. Validate health check, Prometheus metrics, and API key management functionality
4. Confirm Flock can operate in vendor-proxy mode (forwarding to external LLM APIs) without requiring a local inference engine

## Infrastructure Requirements
- **Resource Profile:** small (256Mi RAM, 250m CPU)
- **GPU Required:** No
- **Persistent Storage:** 1Gi PVC for SQLite state database and config
- **Sidecar Containers:** None
- **Deployment Model:** deployment (long-running server)
- **Listens On Port:** 8080
- **LLM API Dependency:** No (operates as a gateway, does not require its own LLM API key)

## Test Scenarios

### Scenario 1: health-check
- **Description:** Verify the /healthz endpoint returns 200 OK
- **Type:** http
- **Endpoint:** /healthz
- **Expected:** Returns 200 with health status
- **Timeout:** 30 seconds

### Scenario 2: admin-dashboard
- **Description:** Verify the embedded web dashboard is served at the root URL
- **Type:** http
- **Endpoint:** /
- **Expected:** Returns 200 with HTML content containing dashboard elements
- **Timeout:** 30 seconds

### Scenario 3: models-api
- **Description:** Verify the OpenAI-compatible /v1/models endpoint responds
- **Type:** http
- **Endpoint:** /v1/models
- **Expected:** Returns 200 with JSON array of available models (may be empty without inference engine)
- **Timeout:** 30 seconds

### Scenario 4: prometheus-metrics
- **Description:** Verify Prometheus metrics are exposed at /metrics
- **Type:** http
- **Endpoint:** /metrics
- **Expected:** Returns 200 with text/plain Prometheus metrics output
- **Timeout:** 30 seconds

## Dockerfile Considerations
- **Multi-stage build required:** Use Go 1.25 builder image (official golang:1.25) + UBI9 minimal runtime
- **CGO_ENABLED=0:** The project uses pure-Go SQLite (modernc.org/sqlite), no CGO needed
- **Catalog files:** The `catalog/` directory with YAML model definitions must be copied into the runtime image at a known path
- **Embedded UI:** The web dashboard (internal/ui/index.html) is compiled into the binary via go:embed, no separate frontend build needed
- **Data directory:** Create /opt/app-root/data for SQLite state and config, make it writable by group 0

## Deployment Considerations
- **Single Deployment + Service:** One Deployment running the `flock` binary, one ClusterIP Service on port 8080
- **PVC:** Mount at /opt/app-root/data for persistent SQLite state
- **Environment variables:** FLOCK_LISTEN=:8080, FLOCK_DATA_DIR=/opt/app-root/data
- **Health check:** Use /healthz for readiness and liveness probes
- **No secrets required:** Flock generates its own API keys at first startup
