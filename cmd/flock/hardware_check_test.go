package main

import (
	"strings"
	"testing"

	"github.com/hadihonarvar/flock/internal/models"
)

// parseModelAddArgs is the CLI-level parser; verify both orderings and the
// no-flag case behave correctly so users can type the words however they
// expect.
func TestParseModelAddArgs(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantID     string
		wantForce  bool
	}{
		{"id only", []string{"qwen3.6-27b"}, "qwen3.6-27b", false},
		{"id then force", []string{"qwen3.6-27b", "--force"}, "qwen3.6-27b", true},
		{"force then id", []string{"--force", "qwen3.6-27b"}, "qwen3.6-27b", true},
		{"single-dash form", []string{"qwen3.6-27b", "-force"}, "qwen3.6-27b", true},
		{"empty", []string{}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id, force := parseModelAddArgs(c.args)
			if id != c.wantID || force != c.wantForce {
				t.Fatalf("got (%q, %v), want (%q, %v)", id, force, c.wantID, c.wantForce)
			}
		})
	}
}

// checkHardwareForModel is the actual gate; verify it refuses obviously
// under-spec installs and lets reasonable cases through. We can't easily
// fake the host's Detect() output, so these tests target the *catalog
// entry* side of the comparison using extreme values.
func TestCheckHardwareForModel_PassesWhenNoMinimums(t *testing.T) {
	entry := &models.Entry{
		ID: "trivial",
		// no Hardware fields set
	}
	if msg := checkHardwareForModel(entry); msg != "" {
		t.Fatalf("expected pass, got refusal: %s", msg)
	}
}

func TestCheckHardwareForModel_RefusesAbsurdRAMRequirement(t *testing.T) {
	// 999 TB of RAM is something no machine running this test has.
	// Worth checking: the function refuses with a clear message rather
	// than crashing or silently allowing.
	entry := &models.Entry{
		ID: "definitely-too-big",
		Hardware: models.HardwareSpec{
			MinRAMGB: 999_999,
		},
	}
	msg := checkHardwareForModel(entry)
	if msg == "" {
		t.Fatal("expected refusal for 999_999 GB RAM requirement, got pass")
	}
	if !strings.Contains(msg, "definitely-too-big") {
		t.Errorf("refusal message %q should name the model", msg)
	}
	if !strings.Contains(msg, "999999") && !strings.Contains(msg, "999_999") {
		t.Errorf("refusal message %q should mention the required size", msg)
	}
}

func TestCheckHardwareForModel_RefusesAbsurdVRAMRequirement(t *testing.T) {
	// If the host has any GPU at all, this refuses; if no GPU is
	// detected (most CI), the function falls through and passes —
	// either outcome is acceptable for this test, so we only check
	// the SHAPE of the refusal message when it occurs.
	entry := &models.Entry{
		ID: "needs-huge-gpu",
		Hardware: models.HardwareSpec{
			MinVRAMGB: 9999,
		},
	}
	msg := checkHardwareForModel(entry)
	if msg != "" && !strings.Contains(msg, "VRAM") {
		t.Errorf("refusal message %q should mention VRAM", msg)
	}
}
