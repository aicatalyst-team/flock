// Package engines defines the Engine interface and the available drivers
// (Ollama, vLLM, MLX-LM, llama.cpp). The control plane uses Engine to
// abstract over the underlying inference server.
package engines

import "context"

// Engine is implemented by every inference backend driver.
type Engine interface {
	Name() string
	Endpoint() string
	Health(ctx context.Context) error

	List(ctx context.Context) ([]string, error)
	Pull(ctx context.Context, modelID string, onProgress func(status string, completed, total int64)) error
	Delete(ctx context.Context, modelID string) error

	Chat(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)
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
type Message struct {
	Role    string // system | user | assistant | tool
	Content string
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
