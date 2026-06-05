// Package api implements the public HTTP surface. v0.1 ships the OpenAI-
// compatible subset (/v1/models, /v1/chat/completions); the Anthropic surface
// (/v1/messages) lands in v0.2.
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/hadihonarvar/flock/internal/engines"
	"github.com/hadihonarvar/flock/internal/models"
	"github.com/hadihonarvar/flock/internal/store"
)

// Handler holds dependencies for the OpenAI-compatible routes.
type Handler struct {
	Engine  engines.Engine
	Store   store.Store
	Catalog []models.Entry
	Default string // default model when request.model is "" or "auto"
}

// ---- /v1/models ----

type modelObj struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type modelList struct {
	Object string     `json:"object"`
	Data   []modelObj `json:"data"`
}

// ListModels handles GET /v1/models.
func (h *Handler) ListModels(w http.ResponseWriter, r *http.Request) {
	installed, err := h.Store.Models().List(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	out := modelList{Object: "list"}
	for _, m := range installed {
		out.Data = append(out.Data, modelObj{
			ID:      m.CatalogID,
			Object:  "model",
			Created: m.InstalledAt.Unix(),
			OwnedBy: "flock",
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- /v1/chat/completions ----

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Stream      bool          `json:"stream,omitempty"`
	Temperature *float32      `json:"temperature,omitempty"`
	TopP        *float32      `json:"top_p,omitempty"`
	MaxTokens   *int          `json:"max_tokens,omitempty"`
	Stop        []string      `json:"stop,omitempty"`
	User        string        `json:"user,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	ID                string       `json:"id"`
	Object            string       `json:"object"`
	Created           int64        `json:"created"`
	Model             string       `json:"model"`
	SystemFingerprint string       `json:"system_fingerprint,omitempty"`
	Choices           []chatChoice `json:"choices"`
	Usage             usage        `json:"usage"`
}

type chatChoice struct {
	Index        int         `json:"index"`
	Message      chatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type chatChunk struct {
	ID      string            `json:"id"`
	Object  string            `json:"object"`
	Created int64             `json:"created"`
	Model   string            `json:"model"`
	Choices []chatChunkChoice `json:"choices"`
	Usage   *usage            `json:"usage,omitempty"`
}

type chatChunkChoice struct {
	Index        int            `json:"index"`
	Delta        map[string]any `json:"delta"`
	FinishReason *string        `json:"finish_reason"`
}

// ChatCompletions handles POST /v1/chat/completions, both streaming and not.
func (h *Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "Invalid JSON body: "+err.Error())
		return
	}
	if len(req.Messages) == 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "messages cannot be empty")
		return
	}

	requested := req.Model
	if requested == "" || requested == "auto" {
		requested = h.Default
	}
	resolved, err := h.resolveModel(requested)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "model_not_found", err.Error())
		return
	}

	engineReq := engines.ChatRequest{
		Model:       resolved,
		Messages:    toEngineMessages(req.Messages),
		Temperature: req.Temperature,
		TopP:        req.TopP,
		MaxTokens:   req.MaxTokens,
		Stop:        req.Stop,
		Stream:      true, // we always stream from the engine; aggregate if needed
	}

	start := time.Now()
	stream, err := h.Engine.Chat(r.Context(), engineReq)
	if err != nil {
		recordUsage(r.Context(), h.Store, "openai", requested, nil, time.Since(start), "error")
		writeJSONError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}

	id := "chatcmpl-" + randID()
	created := time.Now().Unix()

	if req.Stream {
		h.streamResponse(w, r, stream, id, created, requested, start)
		return
	}
	h.aggregateResponse(w, r, stream, id, created, requested, start)
}

func (h *Handler) streamResponse(w http.ResponseWriter, r *http.Request,
	stream <-chan engines.StreamEvent, id string, created int64, modelOut string, start time.Time) {

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, _ := w.(http.Flusher)

	// first chunk announces the assistant role
	sendChunk(w, flusher, chatChunk{
		ID: id, Object: "chat.completion.chunk", Created: created, Model: modelOut,
		Choices: []chatChunkChoice{{
			Index: 0,
			Delta: map[string]any{"role": "assistant"},
		}},
	})

	for ev := range stream {
		if ev.Err != nil {
			recordUsage(r.Context(), h.Store, "openai", modelOut, nil, time.Since(start), "error")
			writeSSEError(w, flusher, ev.Err)
			return
		}
		if ev.Done {
			reason := ev.Reason
			if reason == "" {
				reason = "stop"
			}
			finalChunk := chatChunk{
				ID: id, Object: "chat.completion.chunk", Created: created, Model: modelOut,
				Choices: []chatChunkChoice{{
					Index:        0,
					Delta:        map[string]any{},
					FinishReason: &reason,
				}},
			}
			if ev.Usage != nil {
				finalChunk.Usage = &usage{
					PromptTokens:     ev.Usage.PromptTokens,
					CompletionTokens: ev.Usage.CompletionTokens,
					TotalTokens:      ev.Usage.TotalTokens,
				}
			}
			sendChunk(w, flusher, finalChunk)
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			recordUsage(r.Context(), h.Store, "openai", modelOut, ev.Usage, time.Since(start), "ok")
			return
		}
		if ev.Delta != "" {
			sendChunk(w, flusher, chatChunk{
				ID: id, Object: "chat.completion.chunk", Created: created, Model: modelOut,
				Choices: []chatChunkChoice{{
					Index: 0,
					Delta: map[string]any{"content": ev.Delta},
				}},
			})
		}
		if r.Context().Err() != nil {
			return
		}
	}
}

func (h *Handler) aggregateResponse(w http.ResponseWriter, r *http.Request, stream <-chan engines.StreamEvent,
	id string, created int64, modelOut string, start time.Time) {

	var text string
	var u *engines.Usage
	reason := "stop"
	for ev := range stream {
		if ev.Err != nil {
			recordUsage(r.Context(), h.Store, "openai", modelOut, nil, time.Since(start), "error")
			writeJSONError(w, http.StatusBadGateway, "upstream_error", ev.Err.Error())
			return
		}
		if ev.Done {
			u = ev.Usage
			if ev.Reason != "" {
				reason = ev.Reason
			}
			break
		}
		text += ev.Delta
	}
	resp := chatResponse{
		ID: id, Object: "chat.completion", Created: created, Model: modelOut,
		Choices: []chatChoice{{
			Index:        0,
			Message:      chatMessage{Role: "assistant", Content: text},
			FinishReason: reason,
		}},
	}
	if u != nil {
		resp.Usage = usage{
			PromptTokens:     u.PromptTokens,
			CompletionTokens: u.CompletionTokens,
			TotalTokens:      u.TotalTokens,
		}
	}
	writeJSON(w, http.StatusOK, resp)
	recordUsage(r.Context(), h.Store, "openai", modelOut, u, time.Since(start), "ok")
}

// resolveModel maps the OpenAI "model" field to the engine-native identifier.
// If the requested ID matches a catalog entry, the engine-specific name
// (e.g. ollama_name) is returned; otherwise the input is passed through so users
// can specify raw engine model names directly.
func (h *Handler) resolveModel(catalogID string) (string, error) {
	if catalogID == "" {
		return "", fmt.Errorf("no model specified and no default configured")
	}
	if eng, ok := h.lookupModelByCatalogID(catalogID); ok {
		return eng, nil
	}
	return catalogID, nil
}

func (h *Handler) lookupModelByCatalogID(catalogID string) (engineModel string, ok bool) {
	for _, e := range h.Catalog {
		if e.ID == catalogID {
			if e.Source.Type == "ollama" && e.Source.OllamaName != "" {
				return e.Source.OllamaName, true
			}
			return e.ID, true
		}
	}
	return "", false
}

// ---- helpers ----

func toEngineMessages(in []chatMessage) []engines.Message {
	out := make([]engines.Message, 0, len(in))
	for _, m := range in {
		out = append(out, engines.Message{Role: m.Role, Content: m.Content})
	}
	return out
}

func sendChunk(w http.ResponseWriter, flusher http.Flusher, c chatChunk) {
	b, _ := json.Marshal(c)
	_, _ = io.WriteString(w, "data: ")
	_, _ = w.Write(b)
	_, _ = io.WriteString(w, "\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func writeSSEError(w http.ResponseWriter, flusher http.Flusher, err error) {
	body := map[string]any{"error": map[string]any{"type": "upstream_error", "message": err.Error()}}
	b, _ := json.Marshal(body)
	_, _ = io.WriteString(w, "data: ")
	_, _ = w.Write(b)
	_, _ = io.WriteString(w, "\n\n")
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, code, msg string) {
	body := map[string]any{"error": map[string]any{"type": code, "message": msg}}
	writeJSON(w, status, body)
}

func randID() string {
	buf := make([]byte, 9)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

