package scheduler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/hadihonarvar/flock/internal/models"
)

// ensureLocalGGUF resolves the local filesystem path of a sharded model's
// GGUF, downloading from HuggingFace when needed. Returns the absolute path
// to the file on the leader's disk; caller hands that to
// ensureGGUFOnAllWorkers for downstream distribution.
//
// Three input shapes:
//
//	source.type: file         → return source.path verbatim, just check it exists
//	source.type: huggingface  → download to <modelsDir>/<source.file>, return
//	                            that path. Skip download if already present.
//	other                     → error (Bedrock / Vertex / Ollama don't apply
//	                            to sharded entries)
//
// The download is a single streaming GET to huggingface.co/<repo>/resolve/main/<file>
// with a generous timeout — GGUFs can be 10s of GB. Resume support is left
// for a follow-up; for now an interrupted download leaves the .partial file
// behind and the next call starts over.
func (o *Orchestrator) ensureLocalGGUF(ctx context.Context, entry models.Entry) (string, error) {
	switch entry.Source.Type {
	case "file":
		if entry.Source.Path == "" {
			return "", fmt.Errorf("catalog %s: source.type=file requires source.path", entry.ID)
		}
		if _, err := os.Stat(entry.Source.Path); err != nil {
			return "", fmt.Errorf("catalog %s: source.path %q not present and source.type is file (not huggingface — can't auto-download). Place the GGUF manually or change source.type to huggingface.", entry.ID, entry.Source.Path)
		}
		return entry.Source.Path, nil

	case "huggingface":
		if entry.Source.Repo == "" || entry.Source.File == "" {
			return "", fmt.Errorf("catalog %s: source.type=huggingface requires both source.repo and source.file for auto-download", entry.ID)
		}
		if o.ModelsDir == "" {
			return "", fmt.Errorf("catalog %s wants HF auto-download but Orchestrator.ModelsDir is unset; configure storage.models_dir or pre-place at source.path with type=file", entry.ID)
		}
		target := filepath.Join(o.ModelsDir, entry.Source.File)
		// Already present? Skip the multi-GB pull.
		if st, err := os.Stat(target); err == nil && st.Size() > 0 {
			o.Log.Info("gguf already on leader — skipping HF download",
				"id", entry.ID, "path", target, "size", st.Size())
			return target, nil
		}
		if err := o.downloadFromHF(ctx, entry, target); err != nil {
			return "", err
		}
		return target, nil

	default:
		return "", fmt.Errorf("catalog %s: source.type=%q can't be auto-resolved for sharding (need file or huggingface)", entry.ID, entry.Source.Type)
	}
}

// downloadFromHF streams a single GGUF from HuggingFace to target, writing
// to a .partial sibling and renaming on success so an interrupted download
// doesn't leave a half-file the next caller might mistake for complete.
func (o *Orchestrator) downloadFromHF(ctx context.Context, entry models.Entry, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(target), err)
	}
	url := fmt.Sprintf("https://huggingface.co/%s/resolve/main/%s",
		entry.Source.Repo, entry.Source.File)

	// Use a per-download HTTP client with a much longer timeout than
	// o.HTTP (which is 60s, fine for control-plane calls but not for a
	// 40 GB tarball). 6h cap is enough for ~12 Mbps which is a realistic
	// floor for "the operator forgot to plug in to gigabit."
	client := &http.Client{Timeout: 6 * time.Hour}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("User-Agent", "flock/"+entry.ID)

	o.Log.Info("downloading gguf from huggingface",
		"id", entry.ID, "repo", entry.Source.Repo, "file", entry.Source.File, "url", url)
	t0 := time.Now()

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s → %s", url, resp.Status)
	}

	tmp := target + ".partial"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	n, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("download %s: %w", url, copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close %s: %w", tmp, closeErr)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s → %s: %w", tmp, target, err)
	}
	o.Log.Info("gguf downloaded",
		"id", entry.ID, "path", target, "bytes", n,
		"duration_s", time.Since(t0).Seconds())
	return nil
}
