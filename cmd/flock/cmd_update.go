package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const flockRepo = "hadihonarvar/flock"

// cmdUpdate checks the latest Flock release on GitHub and, unless --check,
// downloads + verifies + installs it in place of the running binary.
//
// Examples:
//
//	flock update                 # check + install latest
//	flock update --check         # just check, don't install
//	flock update --version v0.2  # pin a specific version
//	flock update --force         # reinstall even if up to date
func cmdUpdate(args []string) {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	check := fs.Bool("check", false, "only check for an update, don't install")
	pinned := fs.String("version", "", "install a specific version (e.g. v0.1.1) instead of latest")
	force := fs.Bool("force", false, "install even if already on the latest version")
	fs.Usage = func() {
		showHelp(helpSpec{
			name:    "update",
			summary: "check for and install the latest Flock release",
			usage:   "flock update [--check] [--version <vX.Y.Z>] [--force]",
			flags:   fs,
			examples: []string{
				"flock update                    # upgrade to latest",
				"flock update --check            # just check, no install",
				"flock update --version v0.1.1   # pin specific version",
				"flock upgrade                   # alias of `update`",
			},
			notes: []string{
				"After installing, restart with `flock down && flock up` if it was running.",
				"If the install path needs sudo (e.g. /usr/local/bin), you'll be told the exact command to run.",
			},
		})
	}
	if wantsHelp(args) {
		fs.Usage()
	}
	_ = fs.Parse(args)

	// 1. Find the current binary so we know where to install over.
	exe, err := os.Executable()
	if err != nil {
		die("could not locate current binary: %v", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	// else: keep the un-resolved path; better than the empty string we'd get
	// from swallowing the error.

	// 2. Resolve the target version.
	target := *pinned
	if target == "" {
		note(os.Stdout, "checking for updates…")
		latest, err := fetchLatestVersion()
		if err != nil {
			die("could not fetch latest version: %v", err)
		}
		target = latest
	}

	current := normalizeVersion("v" + version)
	wanted := normalizeVersion(target)
	upToDate := current == wanted

	// 3. Decide what to do based on --check / --force / version compare.
	switch {
	case upToDate && *check && *force:
		note(os.Stdout, "would force-reinstall %s (already on latest)", target)
		return
	case upToDate && *check:
		ok(os.Stdout, "already on the latest version (%s)", target)
		return
	case upToDate && !*force:
		ok(os.Stdout, "already on the latest version (%s)", target)
		return
	case *check:
		note(os.Stdout, "update available: %s → %s", current, target)
		note(os.Stdout, "run: flock update")
		return
	}

	// 4. Download + verify + install.
	note(os.Stdout, "updating: %s → %s", current, target)
	platform := runtime.GOOS + "-" + runtime.GOARCH
	if err := downloadAndInstall(target, platform, exe); err != nil {
		die("update failed: %v", err)
	}

	ok(os.Stdout, "installed %s at %s", target, exe)
	fmt.Println()
	fmt.Println("  To use the new version, restart flock:")
	fmt.Println("    flock down")
	fmt.Println("    flock up")
}

// userAgent identifies us to GitHub. Anonymous requests without a UA hit a
// stricter 60/hour rate limit and GitHub's docs explicitly ask for one.
const userAgent = "flock-update/" + flockRepo

// fetchLatestVersion returns the latest release's tag_name (e.g. "v0.1.0").
func fetchLatestVersion() (string, error) {
	req, _ := http.NewRequest(http.MethodGet,
		"https://api.github.com/repos/"+flockRepo+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", userAgent)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("%s: %s", resp.Status, string(b))
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.TagName == "" {
		return "", fmt.Errorf("no tag_name in API response")
	}
	return body.TagName, nil
}

// downloadAndInstall pulls the right release artifact, verifies its sha256
// against checksums.txt, extracts the binary, and renames it over `target`.
func downloadAndInstall(version, platform, target string) error {
	tmpdir, err := os.MkdirTemp("", "flock-update-*")
	if err != nil {
		return fmt.Errorf("tmpdir: %w", err)
	}
	defer os.RemoveAll(tmpdir)

	asset := fmt.Sprintf("flock-%s.tar.gz", platform)
	base := fmt.Sprintf("https://github.com/%s/releases/download/%s", flockRepo, version)

	note(os.Stdout, "downloading %s/%s", version, asset)
	tarPath := filepath.Join(tmpdir, asset)
	if err := download(base+"/"+asset, tarPath); err != nil {
		return fmt.Errorf("download: %w", err)
	}

	// Verify SHA-256. checksums.txt missing entirely is best-effort (older
	// releases didn't ship it). But if it downloads and our artifact isn't
	// listed, fail closed — installing an unverified binary is worse than
	// failing loudly.
	sumPath := filepath.Join(tmpdir, "checksums.txt")
	if err := download(base+"/checksums.txt", sumPath); err == nil {
		expected, found := lookupChecksum(sumPath, asset)
		if !found {
			return fmt.Errorf("checksums.txt for %s does not list %s — refusing to install unverified binary", version, asset)
		}
		actual, err := sha256File(tarPath)
		if err != nil {
			return fmt.Errorf("sha256: %w", err)
		}
		if expected != actual {
			return fmt.Errorf("checksum MISMATCH (expected %s, got %s) — aborting", expected, actual)
		}
		ok(os.Stdout, "checksum verified (sha256)")
	} else {
		warn(os.Stdout, "checksums.txt not available for %s — skipping verification", version)
	}

	// Extract.
	if err := extractTarGz(tarPath, tmpdir); err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	newBin := filepath.Join(tmpdir, "flock")
	st, err := os.Stat(newBin)
	if err != nil {
		return fmt.Errorf("no flock binary in archive: %w", err)
	}
	if st.IsDir() {
		return fmt.Errorf("flock entry in archive is a directory, not a file")
	}
	if err := os.Chmod(newBin, 0o755); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}

	// Replace the running binary. On Unix os.Rename works even while the
	// old binary is being executed (the old inode stays open until exit).
	if err := os.Rename(newBin, target); err != nil {
		// Permission denied → suggest the exact sudo command.
		stagedPath := filepath.Join(filepath.Dir(target), ".flock.new")
		if copyErr := copyFile(newBin, stagedPath); copyErr == nil {
			return fmt.Errorf(
				"could not install to %s (likely needs sudo)\n"+
					"  the new binary is staged at: %s\n"+
					"  to finish, run:\n"+
					"    sudo mv %s %s",
				target, stagedPath, stagedPath, target)
		}
		return fmt.Errorf("install: %w (you may need sudo)", err)
	}
	return nil
}

func download(url, dst string) error {
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("User-Agent", userAgent)
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s: %s", resp.Status, url)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func lookupChecksum(sumFile, artifact string) (string, bool) {
	data, err := os.ReadFile(sumFile)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == artifact {
			return fields[0], true
		}
	}
	return "", false
}

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

func extractTarGz(src, dstDir string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		// Only extract regular files; skip directories and ignore the
		// leading "./" some tar formats include.
		clean := filepath.Clean(hdr.Name)
		if strings.Contains(clean, "..") {
			continue
		}
		outPath := filepath.Join(dstDir, clean)
		if hdr.Typeflag == tar.TypeDir {
			_ = os.MkdirAll(outPath, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(outPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			_ = out.Close()
			return err
		}
		_ = out.Close()
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// normalizeVersion strips leading "v" and trailing "-dev" / "-snapshot" so
// "v0.1.0", "0.1.0", and "0.1.0-dev" can be loosely compared.
func normalizeVersion(s string) string {
	s = strings.TrimPrefix(s, "v")
	if idx := strings.IndexAny(s, "-+"); idx >= 0 {
		s = s[:idx]
	}
	return "v" + s
}
