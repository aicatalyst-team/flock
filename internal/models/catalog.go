// Package models loads catalog entries (YAML files describing known models)
// and implements model-selection helpers.
package models

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Entry is a single catalog entry, loaded from catalog/<id>.yaml.
type Entry struct {
	ID                 string       `yaml:"id"`
	DisplayName        string       `yaml:"display_name"`
	Source             SourceSpec   `yaml:"source"`
	SizeBytes          int64        `yaml:"size_bytes"`
	Quant              string       `yaml:"quant"`
	ContextWindow      int          `yaml:"context_window"`
	Capabilities       []string     `yaml:"capabilities"`
	RecommendedEngines []string     `yaml:"recommended_engines"`
	Hardware           HardwareSpec `yaml:"hardware"`
	Tags               []string     `yaml:"tags"`
	Sharding           ShardingSpec `yaml:"sharding,omitempty"`
}

// SourceSpec describes where to fetch model weights from.
type SourceSpec struct {
	Type       string `yaml:"type"` // ollama | huggingface | file
	Repo       string `yaml:"repo,omitempty"`
	File       string `yaml:"file,omitempty"`        // specific file within an HF repo (for GGUF)
	OllamaName string `yaml:"ollama_name,omitempty"`
	Path       string `yaml:"path,omitempty"`        // local filesystem path (for GGUF / safetensors)
}

// ShardingSpec is set when a model is too large for any single node and must
// be split across several. When Required is true, the model can only be
// served via the auto-orchestrator: rpc-server on each shard host + a
// coordinator running `llama-server --rpc <list>`.
type ShardingSpec struct {
	Required        bool   `yaml:"required"`
	DefaultShards   int    `yaml:"default_shards"`
	Engine          string `yaml:"engine"`            // "llamacpp" (only supported in v0.4)
	RPCPortBase     int    `yaml:"rpc_port_base"`     // workers bind rpc-server to this + shard index
	CoordinatorPort int    `yaml:"coordinator_port"`  // coordinator binds llama-server to this
}

// HardwareSpec describes the minimum hardware a model needs to run reasonably.
type HardwareSpec struct {
	MinRAMGB  int `yaml:"min_ram_gb"`
	MinVRAMGB int `yaml:"min_vram_gb,omitempty"`
}

// LoadCatalog reads every *.yaml file in dir (non-recursive) and returns parsed entries.
// If dir is empty, the built-in resolution order is attempted:
//
//  1. $FLOCK_CATALOG_DIR
//  2. ./catalog (relative to cwd)
//  3. <exe-dir>/catalog
//  4. /usr/local/share/flock/catalog
func LoadCatalog(dir string) ([]Entry, error) {
	if dir == "" {
		dir = resolveCatalogDir()
	}
	if dir == "" {
		return nil, fmt.Errorf("no catalog directory found")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read catalog %s: %w", dir, err)
	}
	var out []Entry
	for _, de := range entries {
		if de.IsDir() {
			continue
		}
		if !strings.HasSuffix(de.Name(), ".yaml") && !strings.HasSuffix(de.Name(), ".yml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, de.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", de.Name(), err)
		}
		var e Entry
		if err := yaml.Unmarshal(data, &e); err != nil {
			return nil, fmt.Errorf("parse %s: %w", de.Name(), err)
		}
		if e.ID == "" {
			return nil, fmt.Errorf("%s: missing id", de.Name())
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SizeBytes < out[j].SizeBytes })
	return out, nil
}

// FindByID returns the entry with the given id, or nil.
func FindByID(entries []Entry, id string) *Entry {
	for i := range entries {
		if entries[i].ID == id {
			return &entries[i]
		}
	}
	return nil
}

func resolveCatalogDir() string {
	if d := os.Getenv("FLOCK_CATALOG_DIR"); d != "" {
		if _, err := os.Stat(d); err == nil {
			return d
		}
	}
	candidates := []string{"catalog"}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "catalog"))
	}
	candidates = append(candidates, "/usr/local/share/flock/catalog")
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c
		}
	}
	return ""
}
