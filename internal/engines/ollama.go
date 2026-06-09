// Ollama driver: talks HTTP to a local or remote Ollama daemon.
// See https://github.com/ollama/ollama/blob/main/docs/api.md for the protocol.
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

	"go.opentelemetry.io/otel/codes"
)

// Ollama is an Engine that talks to an Ollama HTTP server.
type Ollama struct {
	endpoint string
	client   *http.Client
}

// NewOllama returns a driver pointing at endpoint (e.g. http://127.0.0.1:11434).
func NewOllama(endpoint string) *Ollama {
	return &Ollama{
		endpoint: strings.TrimRight(endpoint, "/"),
		client:   &http.Client{Timeout: 0}, // streaming — no overall deadline
	}
}

func (o *Ollama) Name() string     { return "ollama" }
func (o *Ollama) Endpoint() string { return o.endpoint }

// Health returns nil if Ollama is reachable.
func (o *Ollama) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.endpoint+"/api/version", nil)
	if err != nil {
		return err
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return fmt.Errorf("ollama unreachable at %s: %w", o.endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama version returned %d", resp.StatusCode)
	}
	return nil
}

// List returns the model tags installed in Ollama.
func (o *Ollama) List(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.endpoint+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer resp.Body.Close()
	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode models: %w", err)
	}
	out := make([]string, 0, len(body.Models))
	for _, m := range body.Models {
		out = append(out, m.Name)
	}
	return out, nil
}

// Pull pulls a model. onProgress is called with intermediate updates (may be nil).
func (o *Ollama) Pull(ctx context.Context, modelID string, onProgress func(status string, completed, total int64)) error {
	body, _ := json.Marshal(map[string]any{"name": modelID, "stream": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint+"/api/pull", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return fmt.Errorf("pull request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("pull failed: %s: %s", resp.Status, string(b))
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev struct {
			Status    string `json:"status"`
			Digest    string `json:"digest,omitempty"`
			Total     int64  `json:"total,omitempty"`
			Completed int64  `json:"completed,omitempty"`
			Error     string `json:"error,omitempty"`
		}
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Error != "" {
			return fmt.Errorf("ollama pull: %s", ev.Error)
		}
		if onProgress != nil {
			onProgress(ev.Status, ev.Completed, ev.Total)
		}
	}
	return sc.Err()
}

// Delete removes a model from Ollama.
// Embed calls Ollama's POST /api/embed.
//
// Ollama accepts either a single input string or a list; we always send the
// list form for predictability. The response shape is:
//
//	{
//	  "model": "nomic-embed-text",
//	  "embeddings": [[...], [...]],
//	  "prompt_eval_count": 12
//	}
func (o *Ollama) Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error) {
	if len(req.Inputs) == 0 {
		return EmbedResponse{}, fmt.Errorf("embed: at least one input is required")
	}
	body, err := json.Marshal(map[string]any{
		"model": req.Model,
		"input": req.Inputs,
	})
	if err != nil {
		return EmbedResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return EmbedResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return EmbedResponse{}, fmt.Errorf("embed request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return EmbedResponse{}, fmt.Errorf("ollama embed %s: %s", resp.Status, string(b))
	}

	var out struct {
		Embeddings      [][]float32 `json:"embeddings"`
		PromptEvalCount int         `json:"prompt_eval_count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return EmbedResponse{}, fmt.Errorf("decode embed response: %w", err)
	}
	if len(out.Embeddings) != len(req.Inputs) {
		return EmbedResponse{}, fmt.Errorf("ollama embed: expected %d vectors, got %d", len(req.Inputs), len(out.Embeddings))
	}
	return EmbedResponse{
		Vectors: out.Embeddings,
		Usage: &Usage{
			PromptTokens: out.PromptEvalCount,
			TotalTokens:  out.PromptEvalCount,
		},
	}, nil
}

// Unload asks Ollama to drop the model from memory by sending a no-op
// generate request with keep_alive=0. This is Ollama's documented
// unload mechanism — it does not delete the weights from disk.
func (o *Ollama) Unload(ctx context.Context, modelID string) error {
	body, _ := json.Marshal(map[string]any{
		"model":      modelID,
		"keep_alive": 0,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return fmt.Errorf("unload request: %w", err)
	}
	defer resp.Body.Close()
	// 404 from /api/generate means Ollama doesn't recognize the model
	// name — for an unload caller, the desired post-state ("not in RAM")
	// already holds. Treat as success so `flock model unload` doesn't
	// red-error when the user calls it on a not-currently-loaded model
	// (the whole point of the command is to free RAM, which is already
	// the case).
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unload failed: %s: %s", resp.Status, string(b))
	}
	return nil
}

func (o *Ollama) Delete(ctx context.Context, modelID string) error {
	body, _ := json.Marshal(map[string]any{"name": modelID})
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, o.endpoint+"/api/delete", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return fmt.Errorf("delete request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete failed: %s: %s", resp.Status, string(b))
	}
	return nil
}

// Chat runs a chat completion. Events are emitted on the returned channel
// until Done or an error.
func (o *Ollama) Chat(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	ctx, span := startChatSpan(ctx, "ollama", req.Model, o.endpoint, len(req.Messages))
	// span.End() is deferred in the streaming goroutine so its duration
	// covers the whole streamed response. Synchronous errors close it
	// inline below.

	out := make(chan StreamEvent, 16)
	body := buildOllamaChatBody(req)
	raw, _ := json.Marshal(body)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint+"/api/chat", bytes.NewReader(raw))
	if err != nil {
		span.markError("new request", err)
		span.End()
		close(out)
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(httpReq)
	if err != nil {
		span.markError("http do", err)
		span.End()
		close(out)
		return nil, fmt.Errorf("ollama chat: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		span.SetHTTPStatus(resp.StatusCode)
		span.markError(resp.Status, nil)
		span.End()
		close(out)
		return nil, fmt.Errorf("ollama chat: %s: %s", resp.Status, string(b))
	}
	span.SetHTTPStatus(resp.StatusCode)

	go func() {
		defer close(out)
		defer resp.Body.Close()
		defer span.End() // closes once the stream is drained or ctx cancels
		var promptTokens, completionTokens int
		defer func() { span.SetTokens(promptTokens, completionTokens) }()
		send := func(ev StreamEvent) bool {
			select {
			case out <- ev:
				return true
			case <-ctx.Done():
				span.SetStatus(codes.Error, "client disconnected")
				return false
			}
		}
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			if ctx.Err() != nil {
				return
			}
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			var ev struct {
				Message struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"message"`
				Done            bool   `json:"done"`
				DoneReason      string `json:"done_reason,omitempty"`
				PromptEvalCount int    `json:"prompt_eval_count,omitempty"`
				EvalCount       int    `json:"eval_count,omitempty"`
				Error           string `json:"error,omitempty"`
			}
			if err := json.Unmarshal(line, &ev); err != nil {
				send(StreamEvent{Err: fmt.Errorf("decode: %w", err)})
				return
			}
			if ev.Error != "" {
				send(StreamEvent{Err: fmt.Errorf("ollama: %s", ev.Error)})
				return
			}
			if ev.Done {
				// With Stream:false, Ollama returns a single JSON object
				// that has both done:true AND the full message content.
				// Emit the Delta first so callers running non-streaming
				// see the text in the stream, then close with Done+Usage.
				if ev.Message.Content != "" {
					if !send(StreamEvent{Delta: ev.Message.Content}) {
						return
					}
				}
				promptTokens = ev.PromptEvalCount
				completionTokens = ev.EvalCount
				usage := &Usage{
					PromptTokens:     ev.PromptEvalCount,
					CompletionTokens: ev.EvalCount,
					TotalTokens:      ev.PromptEvalCount + ev.EvalCount,
				}
				span.SetStatus(codes.Ok, "")
				send(StreamEvent{Done: true, Usage: usage, Reason: reasonFrom(ev.DoneReason)})
				return
			}
			if ev.Message.Content != "" {
				if !send(StreamEvent{Delta: ev.Message.Content}) {
					return
				}
			}
		}
		if err := sc.Err(); err != nil {
			send(StreamEvent{Err: fmt.Errorf("stream: %w", err)})
		}
	}()

	return out, nil
}

func reasonFrom(s string) string {
	switch s {
	case "stop", "":
		return "stop"
	case "length":
		return "length"
	default:
		return s
	}
}

func buildOllamaChatBody(req ChatRequest) map[string]any {
	// Ollama's chat schema:
	//   {"role": "...", "content": "...", "images": ["<base64 or url>", ...]}
	// We use map[string]any (not map[string]string) so we can include the
	// optional "images" field only when the caller actually attached one.
	msgs := make([]map[string]any, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": req.System})
	}
	for _, m := range req.Messages {
		entry := map[string]any{"role": m.Role, "content": m.Content}
		if len(m.Images) > 0 {
			entry["images"] = m.Images
		}
		msgs = append(msgs, entry)
	}
	options := map[string]any{}
	if req.Temperature != nil {
		options["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		options["top_p"] = *req.TopP
	}
	if req.MaxTokens != nil {
		options["num_predict"] = *req.MaxTokens
	}
	if len(req.Stop) > 0 {
		options["stop"] = req.Stop
	}
	body := map[string]any{
		"model":    req.Model,
		"messages": msgs,
		"stream":   req.Stream,
	}
	if len(options) > 0 {
		body["options"] = options
	}
	return body
}

// ensure interface compliance at compile time
var _ Engine = (*Ollama)(nil)
