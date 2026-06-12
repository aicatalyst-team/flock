// Package engines defines the Engine interface and the available drivers
// (Ollama, vLLM, MLX-LM, llama.cpp). The control plane uses Engine to
// abstract over the underlying inference server.
package engines

import (
	"context"
	"errors"
)

// ErrUnloadNotSupported is returned by Engine.Unload when the engine
// has no protocol-level unload operation (vLLM, MLX-LM, llama-server).
// Callers should surface it as a soft warning, not a hard failure —
// the user can always restart the engine if they need the memory back.
var ErrUnloadNotSupported = errors.New("engine does not support unload")

// Engine is implemented by every inference backend driver.
type Engine interface {
	Name() string
	Endpoint() string
	Health(ctx context.Context) error

	List(ctx context.Context) ([]string, error)
	Pull(ctx context.Context, modelID string, onProgress func(status string, completed, total int64)) error
	Delete(ctx context.Context, modelID string) error

	// Unload asks the engine to drop the named model from memory without
	// uninstalling its weights. Engines that can't (vLLM, MLX-LM,
	// llama-server) return ErrUnloadNotSupported and the caller treats
	// it as a soft warning. Used by `flock model unload` and by
	// `flock up --unload-on-exit`.
	Unload(ctx context.Context, modelID string) error

	Chat(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)
}

// ResidentModel is one model currently occupying engine memory, with its
// actual byte footprint as reported by the engine. Distinct from List()
// (which returns *installed* models): residency is what memory-admission
// decisions are made against.
type ResidentModel struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	VRAMBytes int64  `json:"vram_bytes"`
}

// ResidentLister is implemented by engines that can report which models
// are resident in RAM/VRAM right now (Ollama via /api/ps). Engines
// without it fall back to catalog-size estimates in the lifecycle
// manager.
type ResidentLister interface {
	Resident(ctx context.Context) ([]ResidentModel, error)
}

// Loader is implemented by engines that can warm-load a model into
// memory ahead of the first request (Ollama via an empty generate).
// pin asks the engine to exempt the model from its idle eviction TTL
// (Ollama keep_alive=-1). Best-effort: an engine under memory pressure
// may still shuffle pinned models.
type Loader interface {
	Load(ctx context.Context, modelID string, pin bool) error
}

// ChatRequest is the engine-agnostic chat input.
type ChatRequest struct {
	Model       string
	Messages    []Message
	System      string
	Temperature *float32
	TopP        *float32
	MaxTokens   *int
	Stop        []string
	Stream      bool
}

// Message is a single chat turn.
//
// Images, when non-empty, are passed to vision-capable engines alongside
// Content. Each entry is either a base64-encoded image (without the
// "data:image/...;base64," prefix) or an absolute https URL — engines
// negotiate which they prefer.
type Message struct {
	Role    string // system | user | assistant | tool
	Content string
	Images  []string // optional, for vision-capable models (Ollama, vLLM, MLX-LM)
}

// StreamEvent is emitted by Engine.Chat as content arrives.
type StreamEvent struct {
	Delta  string
	Done   bool
	Err    error
	Usage  *Usage
	Reason string // finish reason on the final event
}

// Usage is the token accounting for a single completion.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}
