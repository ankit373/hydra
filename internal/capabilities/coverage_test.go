// SPDX-License-Identifier: MIT

package capabilities

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultOverlayPath_IsUnderTheUsersHydraDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HYDRA_HOME", "")

	want := filepath.Join(home, ".hydra", "models.json")
	if got := DefaultOverlayPath(); got != want {
		t.Errorf("DefaultOverlayPath() = %q, want %q", got, want)
	}
}

// $HYDRA_HOME must win over $HOME (#442).
func TestDefaultOverlayPath_PrefersHydraHomeOverHome(t *testing.T) {
	home := t.TempDir()
	hydraHome := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HYDRA_HOME", hydraHome)

	want := filepath.Join(hydraHome, "models.json")
	if got := DefaultOverlayPath(); got != want {
		t.Errorf("DefaultOverlayPath() = %q, want %q ($HYDRA_HOME, not $HOME)", got, want)
	}
}

// ScoreOllama maps a local model name to a capability score. Local model names
// carry a tag ("qwen3:8b"), and an unknown model must still get a usable score
// rather than 0 — a 0 would sort it below everything and never be routed to.
func TestScoreOllama_AlwaysReturnsAUsableScore(t *testing.T) {
	db, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"qwen3:8b", "qwen2.5-coder:7b", "llama3.2:3b",
		"totally-unknown-model:99b", "", "weird/name:tag",
	} {
		got := db.ScoreOllama(name)
		if got <= 0 {
			t.Errorf("ScoreOllama(%q) = %d; a zero score sorts below every head and "+
				"the model could never be routed to", name, got)
		}
		if got > 100 {
			t.Errorf("ScoreOllama(%q) = %d, above the 0–100 scale", name, got)
		}
	}
}

// Name falls back to the id when nothing is known, so a head is never displayed
// with an empty label.
func TestName_FallsBackToTheID(t *testing.T) {
	db, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if got := db.Name("definitely-not-a-known-id"); got != "definitely-not-a-known-id" {
		t.Errorf("Name(unknown) = %q, want the id echoed back", got)
	}
	if got := db.Name(""); got != "" {
		t.Errorf("Name(\"\") = %q", got)
	}
	// A known id resolves to a human label.
	if got := db.Name("claude"); got == "" {
		t.Error("Name(claude) is empty; the probe would render a blank row")
	}
}

// The overlay is how `hyctl models add` works without a rebuild. A missing file
// is an empty overlay, not an error — that is the state before the first add.
func TestLoadOverlay_MissingFileIsEmpty(t *testing.T) {
	entries, err := LoadOverlay(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("a missing overlay errored: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("got %d entries from a missing overlay", len(entries))
	}
}

func TestLoadOverlay_MalformedJSONIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOverlay(path); err == nil {
		t.Error("a malformed overlay loaded without error — the user's added models " +
			"would silently vanish")
	}
}

func TestSaveLoadOverlay_RoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "models.json")
	want := []Entry{
		{ID: "kimi-k3", Name: "Kimi K3", Provider: "moonshot", CapScore: 85},
		{ID: "other", Name: "Other", Provider: "x", CapScore: 40},
	}
	if err := SaveOverlay(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadOverlay(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "kimi-k3" || got[0].CapScore != 85 {
		t.Errorf("round trip changed the overlay: %+v", got)
	}
}

func TestSaveOverlay_UnwritablePathIsAnError(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("i am a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveOverlay(filepath.Join(blocker, "models.json"), nil); err == nil {
		t.Error("saving under a path blocked by a regular file reported success")
	}
}

// AddModel replaces by id rather than appending a duplicate, or `hyctl models
// add` twice would leave two entries and an ambiguous lookup.
func TestAddModel_ReplacesByIDAndReportsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")

	replaced, err := AddModel(path, Entry{ID: "m", Name: "First", CapScore: 10})
	if err != nil {
		t.Fatal(err)
	}
	if replaced {
		t.Error("the first add reported a replacement")
	}

	replaced, err = AddModel(path, Entry{ID: "m", Name: "Second", CapScore: 20})
	if err != nil {
		t.Fatal(err)
	}
	if !replaced {
		t.Error("re-adding the same id did not report a replacement")
	}

	entries, err := LoadOverlay(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries after re-adding one id, want 1: %+v", len(entries), entries)
	}
	if entries[0].Name != "Second" || entries[0].CapScore != 20 {
		t.Errorf("the replacement did not take: %+v", entries[0])
	}
}

func TestRemoveModel_ReportsWhetherAnythingWasRemoved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	if _, err := AddModel(path, Entry{ID: "m", Name: "M", CapScore: 10}); err != nil {
		t.Fatal(err)
	}

	removed, err := RemoveModel(path, "not-there")
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Error("removing an absent id reported success")
	}

	removed, err = RemoveModel(path, "m")
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Error("removing a present id reported nothing removed")
	}

	entries, err := LoadOverlay(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("%d entries survived removal: %+v", len(entries), entries)
	}
}

// The overlay merges over the embedded data: a user's score for a known id
// wins, and an unknown id is added.
func TestLoad_OverlayWinsOverEmbedded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	if err := SaveOverlay(path, []Entry{
		{ID: "claude", Name: "My Claude", CapScore: 42},
		{ID: "brand-new", Name: "Brand New", CapScore: 77},
	}); err != nil {
		t.Fatal(err)
	}

	db, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := db.Score("claude"); got != 42 {
		t.Errorf("Score(claude) = %d, want the overlay's 42 — an operator's retune "+
			"must beat the compiled-in default", got)
	}
	if got := db.Name("claude"); got != "My Claude" {
		t.Errorf("Name(claude) = %q, want the overlay's label", got)
	}
	if got := db.Score("brand-new"); got != 77 {
		t.Errorf("Score(brand-new) = %d, want 77 — a model added at runtime must be "+
			"usable without a rebuild", got)
	}
}

// A broken overlay must not take the embedded catalogue down with it.
func TestLoad_MalformedOverlayIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("a malformed overlay loaded silently; the user would never learn " +
			"their added models are being ignored")
	}
}

// Load caches its result per overlay path, keyed on mtime+size — but an error
// must never be cached past a fix. mtime alone would usually change on a
// rewrite anyway, which would mask this bug by accident (a fresh mtime is a
// fresh cache key regardless of whether errors are cached) — os.Chtimes
// forces the second write to share the first write's exact mtime, and the
// replacement is deliberately the same byte length too ("{broken" and
// "[]     " are both 7 bytes — trailing whitespace after a JSON value is
// valid), so the fingerprint is provably identical across both calls and the
// only thing that can make the second Load succeed is NOT caching the error.
func TestLoad_ErrorIsNotCachedPastAFix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")

	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("malformed overlay loaded without error — the fixture is wrong")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	firstModTime := info.ModTime()

	fixed := []byte("[]     ") // valid empty JSON array + trailing whitespace, same 7 bytes
	if len(fixed) != len("{broken") {
		t.Fatalf("test fixture bug: replacement is %d bytes, want %d", len(fixed), len("{broken"))
	}
	if err := os.WriteFile(path, fixed, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, firstModTime, firstModTime); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.ModTime() != firstModTime {
		t.Fatalf("test fixture bug: mtime did not stick (got %v, want %v)", info.ModTime(), firstModTime)
	}

	db, err := Load(path)
	if err != nil {
		t.Fatalf("Load still errors after the overlay was fixed (same mtime+size as the broken write): %v — a stale cached error is masking the fix", err)
	}
	if db == nil {
		t.Fatal("Load returned a nil DB after the overlay was fixed — a stale cached error entry is masking the fix")
	}
}

func TestLoad_EmbeddedCatalogueIsPopulated(t *testing.T) {
	db, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"claude", "codex"} {
		if db.Score(id) <= 0 {
			t.Errorf("embedded catalogue scores %q at %d", id, db.Score(id))
		}
		if strings.TrimSpace(db.Name(id)) == "" {
			t.Errorf("embedded catalogue has no name for %q", id)
		}
	}
}
