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
	out := make(chan StreamEvent, 16)
	body := buildOllamaChatBody(req)
	raw, _ := json.Marshal(body)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint+"/api/chat", bytes.NewReader(raw))
	if err != nil {
		close(out)
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(httpReq)
	if err != nil {
		close(out)
		return nil, fmt.Errorf("ollama chat: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		close(out)
		return nil, fmt.Errorf("ollama chat: %s: %s", resp.Status, string(b))
	}

	go func() {
		defer close(out)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
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
				out <- StreamEvent{Err: fmt.Errorf("decode: %w", err)}
				return
			}
			if ev.Error != "" {
				out <- StreamEvent{Err: fmt.Errorf("ollama: %s", ev.Error)}
				return
			}
			if ev.Done {
				usage := &Usage{
					PromptTokens:     ev.PromptEvalCount,
					CompletionTokens: ev.EvalCount,
					TotalTokens:      ev.PromptEvalCount + ev.EvalCount,
				}
				out <- StreamEvent{Done: true, Usage: usage, Reason: reasonFrom(ev.DoneReason)}
				return
			}
			if ev.Message.Content != "" {
				out <- StreamEvent{Delta: ev.Message.Content}
			}
		}
		if err := sc.Err(); err != nil {
			out <- StreamEvent{Err: fmt.Errorf("stream: %w", err)}
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
	msgs := make([]map[string]string, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, map[string]string{"role": "system", "content": req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, map[string]string{"role": m.Role, "content": m.Content})
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
