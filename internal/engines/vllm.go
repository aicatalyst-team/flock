// vLLM driver. vLLM exposes an OpenAI-compatible HTTP server when launched
// with `python -m vllm.entrypoints.openai.api_server` or the official Docker
// image. The driver speaks OpenAI to it and adapts to Flock's Engine
// interface.
//
// v0.2 assumes the user runs vLLM externally (Docker or bare); the driver
// does not start/stop the process. The endpoint and served model are
// configured via flock config / env.
//
// Example launch (NVIDIA host):
//
//	docker run --gpus all -p 8000:8000 \
//	  vllm/vllm-openai:latest \
//	  --model Qwen/Qwen3-Coder-30B-A3B-Instruct-AWQ \
//	  --tensor-parallel-size 1
package engines

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// (consumeOpenAIStream uses context.Context for cancellation; ensures the
// `context` import is referenced even when build tags exclude code paths.)

// VLLM is an Engine that proxies to a running vLLM OpenAI-compatible server.
type VLLM struct {
	endpoint string
	apiKey   string // optional, if vLLM was launched with --api-key
	client   *http.Client
}

// NewVLLM returns a driver for a vLLM server at endpoint (e.g. http://gpu:8000).
func NewVLLM(endpoint, apiKey string) *VLLM {
	return &VLLM{
		endpoint: strings.TrimRight(endpoint, "/"),
		apiKey:   apiKey,
		client:   &http.Client{Timeout: 0},
	}
}

func (v *VLLM) Name() string     { return "vllm" }
func (v *VLLM) Endpoint() string { return v.endpoint }

func (v *VLLM) Health(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, v.endpoint+"/v1/models", nil)
	v.auth(req)
	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("vllm unreachable at %s: %w", v.endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("vllm /v1/models returned %d", resp.StatusCode)
	}
	return nil
}

func (v *VLLM) List(ctx context.Context) ([]string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, v.endpoint+"/v1/models", nil)
	v.auth(req)
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer resp.Body.Close()
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode models: %w", err)
	}
	out := make([]string, 0, len(body.Data))
	for _, m := range body.Data {
		out = append(out, m.ID)
	}
	return out, nil
}

// Pull is a no-op for vLLM: the model is fixed at server-launch time.
// The function returns nil immediately (the configured model is assumed loaded).
// A future version may shell out to `huggingface-cli download` to warm the
// HF cache before the user restarts vLLM with the new model.
func (v *VLLM) Pull(ctx context.Context, modelID string, onProgress func(string, int64, int64)) error {
	if onProgress != nil {
		onProgress("vllm: model selection happens at server launch — no pull required", 0, 0)
	}
	return nil
}

// Delete is a no-op (same reasoning as Pull).
func (v *VLLM) Delete(ctx context.Context, modelID string) error {
	return nil
}

// Chat proxies an OpenAI chat completion to vLLM and adapts the streamed
// SSE response back into Flock's StreamEvent channel.
func (v *VLLM) Chat(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	ctx, span := startChatSpan(ctx, "vllm", req.Model, v.endpoint, len(req.Messages))
	// span.End() runs in consumeOpenAIStreamWithSpan so duration covers
	// the full streamed response, not just stream-start.

	out := make(chan StreamEvent, 16)
	body := buildOpenAIChatBody(req)
	raw, _ := json.Marshal(body)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, v.endpoint+"/v1/chat/completions", bytes.NewReader(raw))
	if err != nil {
		span.markError("new request", err)
		span.End()
		close(out)
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	v.auth(httpReq)

	resp, err := v.client.Do(httpReq)
	if err != nil {
		span.markError("http do", err)
		span.End()
		close(out)
		return nil, fmt.Errorf("vllm chat: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		span.SetHTTPStatus(resp.StatusCode)
		span.markError(resp.Status, nil)
		span.End()
		close(out)
		return nil, fmt.Errorf("vllm chat: %s: %s", resp.Status, string(b))
	}
	span.SetHTTPStatus(resp.StatusCode)

	go consumeOpenAIStreamWithSpan(ctx, resp.Body, out, span)
	return out, nil
}

func (v *VLLM) auth(req *http.Request) {
	if v.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+v.apiKey)
	}
}

// ---- shared helpers for OpenAI-compatible engines (vLLM, MLX-LM) ----

func buildOpenAIChatBody(req ChatRequest) map[string]any {
	msgs := make([]map[string]any, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, map[string]any{"role": m.Role, "content": m.Content})
	}
	body := map[string]any{
		"model":    req.Model,
		"messages": msgs,
		"stream":   req.Stream,
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if req.MaxTokens != nil {
		body["max_tokens"] = *req.MaxTokens
	}
	if len(req.Stop) > 0 {
		body["stop"] = req.Stop
	}
	if req.Stream {
		body["stream_options"] = map[string]bool{"include_usage": true}
	}
	return body
}

// consumeOpenAIStream reads an SSE response from an OpenAI-compatible server
// and translates each chunk into a StreamEvent. It correctly handles servers
// (vLLM, MLX) that send a separate usage-only chunk AFTER the finish_reason
// chunk: the function captures finish_reason but continues reading until
// it sees `[DONE]` or a chunk carrying Usage.
func consumeOpenAIStream(ctx context.Context, body io.ReadCloser, out chan<- StreamEvent) {
	defer body.Close()
	defer close(out)
	send := func(ev StreamEvent) bool {
		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}

	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var finishReason string
	var emittedFinal bool

	emitFinal := func(u *Usage) {
		if emittedFinal {
			return
		}
		emittedFinal = true
		send(StreamEvent{Done: true, Reason: finishReason, Usage: u})
	}

	for sc.Scan() {
		if ctx.Err() != nil {
			return
		}
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			emitFinal(nil)
			return
		}
		var ev struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			send(StreamEvent{Err: fmt.Errorf("decode openai chunk: %w", err)})
			return
		}
		if len(ev.Choices) > 0 {
			ch := ev.Choices[0]
			if ch.Delta.Content != "" {
				if !send(StreamEvent{Delta: ch.Delta.Content}) {
					return
				}
			}
			if ch.FinishReason != nil {
				finishReason = *ch.FinishReason
				// Don't return — wait for the usage-only chunk or [DONE].
				// If the same chunk carries Usage, emit final immediately.
				if ev.Usage != nil {
					emitFinal(&Usage{
						PromptTokens:     ev.Usage.PromptTokens,
						CompletionTokens: ev.Usage.CompletionTokens,
						TotalTokens:      ev.Usage.TotalTokens,
					})
					return
				}
			}
		}
		if ev.Usage != nil && len(ev.Choices) == 0 {
			emitFinal(&Usage{
				PromptTokens:     ev.Usage.PromptTokens,
				CompletionTokens: ev.Usage.CompletionTokens,
				TotalTokens:      ev.Usage.TotalTokens,
			})
			return
		}
	}
	if err := sc.Err(); err != nil {
		send(StreamEvent{Err: fmt.Errorf("stream: %w", err)})
		return
	}
	// stream ended without explicit [DONE] — synthesize one
	emitFinal(nil)
}

var _ Engine = (*VLLM)(nil)

// consumeOpenAIStreamWithSpan wraps consumeOpenAIStream and additionally
// records the per-request token counts + closes the span once the stream
// is drained or the context cancels. Spans are no-op when the global
// TracerProvider isn't set (FLOCK_OTLP_ENDPOINT unset), so this path
// has zero overhead in the default config.
func consumeOpenAIStreamWithSpan(ctx context.Context, body io.ReadCloser, out chan<- StreamEvent, span *chatSpan) {
	defer span.End()
	intermediate := make(chan StreamEvent, 16)
	go consumeOpenAIStream(ctx, body, intermediate)
	var promptTokens, completionTokens int
	for ev := range intermediate {
		if ev.Done && ev.Usage != nil {
			promptTokens = ev.Usage.PromptTokens
			completionTokens = ev.Usage.CompletionTokens
		}
		select {
		case out <- ev:
		case <-ctx.Done():
			// drain so the upstream producer doesn't block
			go func() {
				for range intermediate {
				}
			}()
			span.SetTokens(promptTokens, completionTokens)
			return
		}
	}
	span.SetTokens(promptTokens, completionTokens)
	close(out)
}
