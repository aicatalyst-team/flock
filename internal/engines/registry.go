package engines

import "fmt"

// New returns an Engine by name with no upstream auth. Convenience wrapper
// around NewWithAuth for backends that don't need an API key (Ollama, MLX).
func New(name, endpoint string) (Engine, error) {
	return NewWithAuth(name, endpoint, "")
}

// NewWithAuth returns an Engine by name. Supported: ollama, vllm, mlx,
// llamacpp / llamacpp-rpc. The apiKey is forwarded to upstreams that
// support Bearer auth (vLLM).
func NewWithAuth(name, endpoint, apiKey string) (Engine, error) {
	switch name {
	case "ollama":
		return NewOllama(endpoint), nil
	case "vllm":
		return NewVLLM(endpoint, apiKey), nil
	case "mlx", "mlx-lm":
		return NewMLX(endpoint), nil
	case "llamacpp", "llama-cpp", "llamacpp-rpc":
		return NewLlamaCppRPC(endpoint), nil
	default:
		return nil, fmt.Errorf("unknown engine %q", name)
	}
}
