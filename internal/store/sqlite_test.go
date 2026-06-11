package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// TestAPIKeyAllowedModelsRoundtrip verifies the three states of the
// allowed_models column round-trip correctly: nil ("any model"), empty
// ([]string{}, "deny all"), and an explicit list. Caught two latent
// JSON-decode bugs in early drafts; keep the explicit cases.
func TestAPIKeyAllowedModelsRoundtrip(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenSQLite(filepath.Join(dir, "x.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	cases := []struct {
		name string
		list []string
	}{
		{"nil → unrestricted", nil},
		{"empty → deny all", []string{}},
		{"single literal", []string{"qwen3-14b"}},
		{"multiple + wildcards", []string{"qwen-coder-7b", "claude-*", "gpt-4o-mini"}},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := "k_test_" + c.name
			rec := APIKey{
				ID:            id,
				Hash:          "hash_" + c.name,
				Name:          c.name,
				Scope:         "user",
				UserID:        "alice",
				AllowedModels: c.list,
				CreatedAt:     time.Unix(int64(1_700_000_000+i), 0),
			}
			if err := st.APIKeys().Create(ctx, rec); err != nil {
				t.Fatalf("Create: %v", err)
			}
			got, err := st.APIKeys().GetByID(ctx, id)
			if err != nil {
				t.Fatalf("GetByID: %v", err)
			}
			if got == nil {
				t.Fatalf("GetByID: got nil")
			}
			if !sameSlice(got.AllowedModels, c.list) {
				t.Errorf("AllowedModels round-trip: got %#v (nil=%v) want %#v (nil=%v)",
					got.AllowedModels, got.AllowedModels == nil, c.list, c.list == nil)
			}

			// And via UpdateAllowedModels.
			if err := st.APIKeys().UpdateAllowedModels(ctx, id, []string{"updated"}); err != nil {
				t.Fatalf("UpdateAllowedModels: %v", err)
			}
			got, _ = st.APIKeys().GetByID(ctx, id)
			if !reflect.DeepEqual(got.AllowedModels, []string{"updated"}) {
				t.Errorf("after Update: %#v", got.AllowedModels)
			}
			// Clear back to nil.
			if err := st.APIKeys().UpdateAllowedModels(ctx, id, nil); err != nil {
				t.Fatalf("UpdateAllowedModels nil: %v", err)
			}
			got, _ = st.APIKeys().GetByID(ctx, id)
			if got.AllowedModels != nil {
				t.Errorf("after clear: got %#v want nil", got.AllowedModels)
			}
		})
	}
}

// sameSlice treats nil and []string{} as distinct (the allowlist
// semantics depend on the distinction), but slice equality is by value.
func sameSlice(a, b []string) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
