package engines

import "fmt"

// New returns an Engine by name. As of v0.2: ollama, vllm, mlx.
//
// For vllm, the endpoint may include an API key after a '|', e.g.
//
//	vllm,http://host:8000|my-api-key
//
// (the parsing is left to higher-level config to avoid leaking parsing here).
func New(name, endpoint string) (Engine, error) {
	switch name {
	case "ollama":
		return NewOllama(endpoint), nil
	case "vllm":
		return NewVLLM(endpoint, ""), nil
	case "mlx", "mlx-lm":
		return NewMLX(endpoint), nil
	default:
		return nil, fmt.Errorf("unknown engine %q", name)
	}
}
