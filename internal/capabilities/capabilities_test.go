// SPDX-License-Identifier: MIT

package capabilities

import (
	"path/filepath"
	"testing"
)

func TestLoad_BuiltinOnly(t *testing.T) {
	db, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if db.Score("claude") < 90 {
		t.Errorf("claude score = %d, want ≥90", db.Score("claude"))
	}
	if db.Name("claude") != "Claude Code" {
		t.Errorf("claude name = %q", db.Name("claude"))
	}
	// Unknown model → default score.
	if db.Score("kimi-k2") == 0 {
		t.Error("unknown model should get DefaultScore, not 0")
	}
}

func TestOverlay_AddMergesOverBuiltin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")

	// Add a brand-new model (Kimi K2) and override a built-in (claude).
	if _, err := AddModel(path, Entry{ID: "kimi-k2", Name: "Kimi K2", Provider: "moonshot", CapScore: 82}); err != nil {
		t.Fatal(err)
	}
	if _, err := AddModel(path, Entry{ID: "claude", Name: "Claude Code (tuned)", CapScore: 99}); err != nil {
		t.Fatal(err)
	}

	db, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if db.Score("kimi-k2") != 82 {
		t.Errorf("kimi-k2 score = %d, want 82 (added via overlay)", db.Score("kimi-k2"))
	}
	if db.Score("claude") != 99 {
		t.Errorf("claude score = %d, want 99 (overlay overrides built-in)", db.Score("claude"))
	}
	if db.Name("claude") != "Claude Code (tuned)" {
		t.Errorf("claude name = %q, want overridden", db.Name("claude"))
	}
}

func TestOverlay_AddIsUpsert(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	if replaced, _ := AddModel(path, Entry{ID: "kimi-k2", CapScore: 80}); replaced {
		t.Error("first add should not report replaced")
	}
	if replaced, _ := AddModel(path, Entry{ID: "kimi-k2", CapScore: 85}); !replaced {
		t.Error("second add of same ID should report replaced")
	}
	entries, _ := LoadOverlay(path)
	if len(entries) != 1 || entries[0].CapScore != 85 {
		t.Errorf("upsert failed: %+v", entries)
	}
}

func TestOverlay_Remove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	_, _ = AddModel(path, Entry{ID: "kimi-k2", CapScore: 82})

	if removed, _ := RemoveModel(path, "kimi-k2"); !removed {
		t.Error("remove of existing model should report removed")
	}
	if removed, _ := RemoveModel(path, "nonexistent"); removed {
		t.Error("remove of missing model should report not-removed")
	}
	entries, _ := LoadOverlay(path)
	if len(entries) != 0 {
		t.Errorf("overlay should be empty after removal, got %+v", entries)
	}
}

func TestEntries_MarksSourceAndSorts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	_, _ = AddModel(path, Entry{ID: "kimi-k2", Name: "Kimi K2", CapScore: 100}) // top score

	db, _ := Load(path)
	entries := db.Entries()
	if len(entries) < 2 {
		t.Fatal("expected merged entries")
	}
	if entries[0].ID != "kimi-k2" || entries[0].Source != "user" {
		t.Errorf("highest-score entry should be the user-added kimi-k2, got %+v", entries[0])
	}
	// A built-in should be marked "builtin".
	for _, e := range entries {
		if e.ID == "claude" && e.Source != "builtin" {
			t.Errorf("claude should be source=builtin, got %q", e.Source)
		}
	}
}

func TestHeuristicCapScore(t *testing.T) {
	cases := map[string]int{
		"anthropic/claude-opus-4": 92,
		"openai/gpt-4o":           86,
		"moonshot/kimi-k2":        78,
		"meta-llama/llama-3.1-8b": 66,
		"qwen/qwen-3b":            55,
		"some-unknown-model":      70,
	}
	for id, want := range cases {
		if got := HeuristicCapScore(id); got != want {
			t.Errorf("HeuristicCapScore(%q) = %d, want %d", id, got, want)
		}
	}
}
