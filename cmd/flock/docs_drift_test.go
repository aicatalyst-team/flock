package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/hadihonarvar/flock/internal/models"
)

// TestDocsReferenceRealCommands verifies that every `flock <verb>` mentioned
// in README.md and QUICKSTART.md corresponds to a real cobra subcommand
// dispatched from cmd/flock/main.go. Catches "ghost commands" — doc claims
// that don't exist in the binary.
func TestDocsReferenceRealCommands(t *testing.T) {
	repoRoot := findRepoRoot(t)
	verbs := canonicalVerbs(t, filepath.Join(repoRoot, "cmd", "flock", "main.go"))

	docs := []string{
		filepath.Join(repoRoot, "README.md"),
		filepath.Join(repoRoot, "QUICKSTART.md"),
	}

	// Tokens that follow `flock ` in docs but are not commands (placeholders,
	// flag values, or self-references). Add new ones here when intentional.
	placeholders := map[string]bool{
		"<client>": true, "<name>": true, "<command>": true, "<tool>": true,
		"<url>": true, "<id>": true, "<model>": true, "<query>": true,
		"--help": true, "-h": true, "--version": true, "-v": true,
	}

	// Match `flock <verb>` on the same line, where verb starts lowercase
	// (real commands always do). Same-line constraint avoids matching
	// across code-block line breaks like `git clone …/flock\ncd flock`.
	verbRe := regexp.MustCompile(`\bflock[ \t]+([a-z][\w-]*)`)
	// Verb tokens that look like commands but are actually version
	// fragments captured before the "." breaks the regex (e.g. the "v0" in
	// "flock v0.4.0").
	versionish := regexp.MustCompile(`^v\d+$`)

	var ghosts []string
	for _, path := range docs {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, m := range verbRe.FindAllStringSubmatch(string(data), -1) {
			tok := m[1]
			if placeholders[tok] || verbs[tok] || versionish.MatchString(tok) {
				continue
			}
			ghosts = append(ghosts, filepath.Base(path)+": flock "+tok)
		}
	}

	if len(ghosts) > 0 {
		sort.Strings(ghosts)
		uniq := dedupe(ghosts)
		t.Errorf("docs reference commands not dispatched in cmd/flock/main.go:\n  %s\n\nFix: either add the command (or alias) or remove the doc reference.",
			strings.Join(uniq, "\n  "))
	}
}

// TestCatalogParses verifies every YAML file in catalog/ loads cleanly through
// the same parser the binary uses at startup. Catches malformed entries
// before they reach `flock up`.
func TestCatalogParses(t *testing.T) {
	repoRoot := findRepoRoot(t)
	catalogDir := filepath.Join(repoRoot, "catalog")

	entries, err := models.LoadCatalog(catalogDir)
	if err != nil {
		t.Fatalf("LoadCatalog(%s): %v", catalogDir, err)
	}
	if len(entries) == 0 {
		t.Fatal("catalog parsed to zero entries — wrong directory?")
	}

	// Every entry must have an id, and the filename must match the id.
	files, err := os.ReadDir(catalogDir)
	if err != nil {
		t.Fatalf("read catalog dir: %v", err)
	}
	fileIDs := map[string]bool{}
	for _, f := range files {
		name := f.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		id := strings.TrimSuffix(strings.TrimSuffix(name, ".yaml"), ".yml")
		fileIDs[id] = true
	}
	for _, e := range entries {
		if e.ID == "" {
			t.Errorf("entry with empty id")
			continue
		}
		if !fileIDs[e.ID] {
			t.Errorf("entry id %q has no matching <id>.yaml in catalog/", e.ID)
		}
	}
}

// TestVersionStampSane verifies the binary's version variable looks like a
// real semver-ish string. Cheap guard against accidentally landing
// "0.0.0" or an empty stamp.
func TestVersionStampSane(t *testing.T) {
	if version == "" {
		t.Fatal("version is empty")
	}
	semverish := regexp.MustCompile(`^\d+\.\d+\.\d+(-[A-Za-z0-9.-]+)?$`)
	if !semverish.MatchString(version) {
		t.Errorf("version %q does not look like semver (expected MAJOR.MINOR.PATCH[-suffix])", version)
	}
}

// canonicalVerbs returns the set of verbs dispatched from main.go's switch
// statement. Reads the file rather than parsing the AST — the switch is
// stable and simple.
func canonicalVerbs(t *testing.T, mainPath string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("read %s: %v", mainPath, err)
	}
	// Each `case "foo":` or `case "foo", "bar":` line registers one or more verbs.
	caseRe := regexp.MustCompile(`(?m)^\s*case\s+("[\w-]+"(?:\s*,\s*"[\w-]+")*)\s*:`)
	strRe := regexp.MustCompile(`"([\w-]+)"`)
	verbs := map[string]bool{}
	for _, m := range caseRe.FindAllStringSubmatch(string(data), -1) {
		for _, s := range strRe.FindAllStringSubmatch(m[1], -1) {
			verbs[s[1]] = true
		}
	}
	if len(verbs) == 0 {
		t.Fatalf("no `case \"verb\":` lines found in %s", mainPath)
	}
	return verbs
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate repo root (no go.mod above %s)", wd)
		}
		dir = parent
	}
}

func dedupe(s []string) []string {
	seen := map[string]bool{}
	out := s[:0]
	for _, x := range s {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
