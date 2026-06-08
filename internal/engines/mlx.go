// MLX-LM driver. `mlx_lm.server` exposes an OpenAI-compatible HTTP API on
// Apple Silicon. Like vLLM, the driver assumes the user runs the server
// externally; it acts as a thin OpenAI client.
//
// Example launch (Apple Silicon host):
//
//	pip install mlx-lm
//	mlx_lm.server --model mlx-community/Qwen2.5-Coder-14B-Instruct-4bit \
//	              --port 8080
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

// MLX is an Engine that proxies to a running mlx_lm.server.
type MLX struct {
	endpoint string
	client   *http.Client
}

// NewMLX returns a driver for an MLX-LM server at endpoint (e.g. http://localhost:8080).
func NewMLX(endpoint string) *MLX {
	return &MLX{
		endpoint: strings.TrimRight(endpoint, "/"),
		client:   &http.Client{Timeout: 0},
	}
}

func (m *MLX) Name() string     { return "mlx" }
func (m *MLX) Endpoint() string { return m.endpoint }

func (m *MLX) Health(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, m.endpoint+"/v1/models", nil)
	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("mlx unreachable at %s: %w", m.endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("mlx /v1/models returned %d", resp.StatusCode)
	}
	return nil
}

func (m *MLX) List(ctx context.Context) ([]string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, m.endpoint+"/v1/models", nil)
	resp, err := m.client.Do(req)
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

// Pull is a no-op (model is selected at mlx_lm.server launch time).
func (m *MLX) Pull(ctx context.Context, modelID string, onProgress func(string, int64, int64)) error {
	if onProgress != nil {
		onProgress("mlx: model selection happens at server launch — no pull required", 0, 0)
	}
	return nil
}

func (m *MLX) Delete(ctx context.Context, modelID string) error { return nil }

// Chat proxies an OpenAI chat completion to mlx_lm.server.
func (m *MLX) Chat(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	ctx, span := startChatSpan(ctx, "mlx", req.Model, m.endpoint, len(req.Messages))

	out := make(chan StreamEvent, 16)
	body := buildOpenAIChatBody(req)
	raw, _ := json.Marshal(body)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint+"/v1/chat/completions", bytes.NewReader(raw))
	if err != nil {
		span.markError("new request", err)
		span.End()
		close(out)
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(httpReq)
	if err != nil {
		span.markError("http do", err)
		span.End()
		close(out)
		return nil, fmt.Errorf("mlx chat: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		span.SetHTTPStatus(resp.StatusCode)
		span.markError(resp.Status, nil)
		span.End()
		close(out)
		return nil, fmt.Errorf("mlx chat: %s: %s", resp.Status, string(b))
	}
	span.SetHTTPStatus(resp.StatusCode)

	go consumeOpenAIStreamWithSpan(ctx, resp.Body, out, span)
	return out, nil
}

var _ Engine = (*MLX)(nil)
