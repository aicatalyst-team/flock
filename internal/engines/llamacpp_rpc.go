// llama.cpp driver. Two modes, one driver:
//
//   - Single-node: `llama-server -m model.gguf --port 8089` on the same box
//     as Flock. Set `engine.preferred: llamacpp` (or `FLOCK_ENGINE=llamacpp`)
//     and point `engine.llamacpp_endpoint` at it. Lower RAM and cold-start
//     latency than Ollama on weak hardware — bare llama.cpp, no daemon
//     layer in between.
//
//   - Distributed (RPC): `llama-server --rpc <backends>` shards a single
//     model across multiple machines via `rpc-server` processes. The
//     sharding orchestrator (`internal/scheduler/sharding.go`) launches
//     `rpc-server` on workers automatically and starts the coordinator
//     `llama-server`, then points an internal llamacpp driver at the
//     coordinator. You can also run it by hand and point Flock at the
//     result via `engine.preferred: llamacpp`.
//
// This driver is a thin OpenAI-compatible client (same shape as VLLM/MLX);
// from its perspective the upstream is just an OpenAI server, --rpc or not.
//
// Manual RPC setup (if you prefer to manage processes yourself):
//
//	# On each worker node:
//	rpc-server -p 50052 &
//
//	# On the coordinator (the machine that exposes the OpenAI API):
//	llama-server -m /path/to/model.gguf \
//	    --rpc worker1.local:50052,worker2.local:50052 \
//	    --gpu-layers 999 --port 8089
//
//	# On the Flock leader, configure the catalog entry with
//	# source.type: llamacpp_rpc
//	# source.path: /path/to/model.gguf
//	# and set engine.preferred: llamacpp on the coordinator.
//
// Reference: https://github.com/ggerganov/llama.cpp/tree/master/examples/rpc
package engines

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// LlamaCppRPC is an Engine pointing at a `llama-server` HTTP endpoint.
// The server may or may not be configured with --rpc — the driver doesn't
// care. From its perspective the upstream is just OpenAI-compatible.
type LlamaCppRPC struct {
	endpoint string
	client   *http.Client
}

// NewLlamaCppRPC returns a driver for a llama-server at endpoint.
func NewLlamaCppRPC(endpoint string) *LlamaCppRPC {
	return &LlamaCppRPC{
		endpoint: strings.TrimRight(endpoint, "/"),
		client:   &http.Client{Timeout: 0},
	}
}

func (l *LlamaCppRPC) Name() string     { return "llamacpp" }
func (l *LlamaCppRPC) Endpoint() string { return l.endpoint }

func (l *LlamaCppRPC) Health(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, l.endpoint+"/health", nil)
	resp, err := l.client.Do(req)
	if err != nil {
		return fmt.Errorf("llamacpp unreachable at %s: %w", l.endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("llamacpp /health returned %d", resp.StatusCode)
	}
	return nil
}

// List queries /v1/models. llama-server returns the loaded model under the id
// "default" unless --alias was passed.
func (l *LlamaCppRPC) List(ctx context.Context) ([]string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, l.endpoint+"/v1/models", nil)
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	defer resp.Body.Close()
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	out := make([]string, 0, len(body.Data))
	for _, d := range body.Data {
		out = append(out, d.ID)
	}
	return out, nil
}

// Pull is a no-op (model is selected at llama-server launch time).
// A future scheduler may shell out to download GGUFs to coordinator + worker
// nodes, but that's coordination, not driver responsibility.
func (l *LlamaCppRPC) Pull(ctx context.Context, modelID string, onProgress func(string, int64, int64)) error {
	if onProgress != nil {
		onProgress("llamacpp: model selection happens at llama-server launch", 0, 0)
	}
	return nil
}

// Delete is a no-op for the same reason.
func (l *LlamaCppRPC) Delete(ctx context.Context, modelID string) error { return nil }

// Unload is not supported by llama-server — it owns one model per process.
// When Flock auto-spawned llama-server, `flock up`'s supervisor stops the
// process on shutdown (which frees memory). User-managed llama-server must
// be restarted to free RAM.
func (l *LlamaCppRPC) Unload(ctx context.Context, modelID string) error { return ErrUnloadNotSupported }

// Chat proxies an OpenAI chat completion to llama-server and adapts the
// streamed SSE response back into Flock's StreamEvent channel.
func (l *LlamaCppRPC) Chat(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	ctx, span := startChatSpan(ctx, "llamacpp", req.Model, l.endpoint, len(req.Messages))

	out := make(chan StreamEvent, 16)
	body := buildOpenAIChatBody(req)
	raw, _ := json.Marshal(body)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, l.endpoint+"/v1/chat/completions", bytes.NewReader(raw))
	if err != nil {
		span.markError("new request", err)
		span.End()
		close(out)
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := l.client.Do(httpReq)
	if err != nil {
		span.markError("http do", err)
		span.End()
		close(out)
		return nil, fmt.Errorf("llamacpp chat: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		span.SetHTTPStatus(resp.StatusCode)
		span.markError(resp.Status, nil)
		span.End()
		close(out)
		return nil, fmt.Errorf("llamacpp chat: %s: %s", resp.Status, string(b))
	}
	span.SetHTTPStatus(resp.StatusCode)

	go consumeOpenAIStreamWithSpan(ctx, resp.Body, out, span)
	return out, nil
}

var _ Engine = (*LlamaCppRPC)(nil)
