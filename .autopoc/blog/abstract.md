# Blog Abstract: Flock on OpenShift

## Thesis
Deploying Flock, a self-hosted LLM gateway, on OpenShift proves that teams can run their own inference control plane with API key management, usage quotas, and audit logging alongside Red Hat OpenShift AI model serving infrastructure.

## Target Audience
Platform engineers and ML engineers managing LLM inference infrastructure.

## Blog Type
Red Hat Developer Blog

## Key Points
1. Flock compiles to a single Go binary with embedded dashboard, making containerization straightforward
2. The UBI multi-stage build produces a minimal runtime image with no CGO dependencies
3. All 4 PoC test scenarios passed: health checks, dashboard, authenticated API, and Prometheus metrics

## Products/Projects
Red Hat OpenShift AI, Open Data Hub, Flock

## CTA
Try deploying Flock on your OpenShift cluster to add API key management and usage tracking to your LLM serving stack.

## Section Outline
1. What is Flock?
2. Why a self-hosted LLM gateway matters
3. Containerizing Flock for OpenShift
4. Deploying and testing on the cluster
5. What we learned
6. Try it yourself
