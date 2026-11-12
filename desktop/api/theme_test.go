// SPDX-License-Identifier: MIT

package api

import (
	"os"
	"path/filepath"
	"testing"
)

// The native window reads this at launch, before the page exists, so a choice the
// page saved has to come back here or a viewer who picked against the OS sees the
// other theme's background flash on every start (#1355).
func TestTheme_SavedChoiceRoundTrips(t *testing.T) {
	home := filepath.Join(t.TempDir(), "not-yet-created")
	t.Setenv("HYDRA_HOME", home)
	a := New()
	if got := SavedTheme(); got != "" {
		t.Fatalf("SavedTheme() with nothing saved = %q, want \"\" (follow the OS)", got)
	}
	for _, mode := range []string{"dark", "light"} {
		if err := a.SaveTheme(mode); err != nil {
			t.Fatalf("SaveTheme(%q): %v", mode, err)
		}
		if got := SavedTheme(); got != mode {
			t.Fatalf("SavedTheme() after SaveTheme(%q) = %q", mode, got)
		}
	}
	if err := a.SaveTheme("system"); err != nil {
		t.Fatalf("SaveTheme(system): %v", err)
	}
	if got := SavedTheme(); got != "" {
		t.Fatalf("SavedTheme() after choosing system = %q, want \"\"", got)
	}
	if err := a.SaveTheme("system"); err != nil {
		t.Fatalf("clearing an already-clear choice must not fail: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "desktop-theme"), []byte("purple"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := SavedTheme(); got != "" {
		t.Fatalf("SavedTheme() of a corrupt file = %q, want \"\"", got)
	}
}
