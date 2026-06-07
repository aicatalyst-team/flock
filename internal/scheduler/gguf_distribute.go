package scheduler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/hadihonarvar/flock/internal/store"
)

// ensureGGUFOnAllWorkers makes sure localPath is present on every worker
// in workers (matched by sha256). For each missing host, streams the file
// over the worker's /v1/process/upload endpoint. Skips upload when the
// worker reports the file is already there.
//
// Returns the first failure if any host can't be brought into sync; the
// caller (CreateSharded) treats this as fatal and aborts before launching
// rpc-server.
func (o *Orchestrator) ensureGGUFOnAllWorkers(ctx context.Context, workers []store.Node, localPath string) error {
	sum, err := sha256File(localPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", localPath, err)
	}
	name := filepath.Base(localPath)
	for _, w := range workers {
		if err := o.ensureGGUFOnNode(ctx, w, localPath, name, sum); err != nil {
			return fmt.Errorf("node %s (%s): %w", w.ID, w.Hostname, err)
		}
	}
	return nil
}

func (o *Orchestrator) ensureGGUFOnNode(ctx context.Context, node store.Node, localPath, name, sum string) error {
	// Cheap probe first.
	checkURL := workerURL(node.Address) + "/v1/process/file?" + url.Values{
		"name":   {name},
		"sha256": {sum},
	}.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodHead, checkURL, nil)
	req.Header.Set("Authorization", "Bearer "+node.WorkerToken)
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("file check: %w", err)
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		o.Log.Info("gguf already on worker — skipping upload",
			"node", node.ID, "name", name, "sha256", sum[:12])
		return nil
	case http.StatusNotFound:
		// fall through to upload
	case http.StatusServiceUnavailable:
		return fmt.Errorf("worker has no models_dir configured (set storage.models_dir on the worker)")
	default:
		return fmt.Errorf("file check returned %s", resp.Status)
	}

	// Upload. Stream the file body to avoid loading the whole GGUF into RAM
	// just to hand it to net/http — Request.Body is an io.Reader.
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", localPath, err)
	}
	defer f.Close()
	stat, _ := f.Stat()

	uploadURL := workerURL(node.Address) + "/v1/process/upload?" + url.Values{
		"name":   {name},
		"sha256": {sum},
	}.Encode()
	upReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, f)
	upReq.Header.Set("Authorization", "Bearer "+node.WorkerToken)
	upReq.Header.Set("Content-Type", "application/octet-stream")
	if stat != nil {
		upReq.ContentLength = stat.Size()
	}

	o.Log.Info("uploading gguf to worker",
		"node", node.ID, "name", name, "bytes", stat.Size(), "sha256", sum[:12])

	upResp, err := o.HTTP.Do(upReq)
	if err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	defer upResp.Body.Close()
	if upResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(upResp.Body, 1024))
		return fmt.Errorf("upload returned %s: %s", upResp.Status, bytes.TrimSpace(body))
	}
	o.Log.Info("gguf uploaded", "node", node.ID, "name", name)
	return nil
}

// sha256File mirrors agent.sha256File — duplicated here to avoid an awkward
// scheduler→agent dependency just for one helper.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
