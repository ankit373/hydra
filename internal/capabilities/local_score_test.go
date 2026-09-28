// SPDX-License-Identifier: MIT

package capabilities

import (
	"path/filepath"
	"testing"
)

// A capability score recorded for a local head was written to the overlay,
// shown by `hyctl models list` as the head's score, and then ignored by every
// local provider, which scored the head off a family pattern instead. Two
// surfaces reported different numbers for the same live head (#989).
func TestScoreLocal_AnOverlayEntryForTheHeadBeatsItsFamilyPattern(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	if _, err := AddModel(path, Entry{
		ID: "ollama/qwen3:0.6b", Name: "qwen3 tuned", Provider: "ollama", CapScore: 98,
	}); err != nil {
		t.Fatal(err)
	}
	db, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	family := db.scoreFamily("qwen3:0.6b")
	if family == 98 {
		t.Fatalf("the family pattern already scores 98, so this test cannot tell the two paths apart")
	}
	if got := db.ScoreLocal("ollama/qwen3:0.6b", "qwen3:0.6b"); got != 98 {
		t.Errorf("ScoreLocal = %d, want 98: the user named this exact head, the pattern only guessed from a substring", got)
	}
	if got := db.SourceLocal("ollama/qwen3:0.6b", "qwen3:0.6b"); got != "user" {
		t.Errorf("SourceLocal = %q, want \"user\": the number came from the overlay, not the curated catalog", got)
	}
}

// The pattern is still what scores a model nobody has an opinion about, or
// every local head on a fresh machine would drop to DefaultScore.
func TestScoreLocal_NoOverlayEntryKeepsTheFamilyScore(t *testing.T) {
	db, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	want := db.scoreFamily("qwen2.5-coder:7b")
	if want == db.d.DefaultScore {
		t.Fatalf("qwen2.5-coder must match a family pattern for this test to mean anything")
	}
	if got := db.ScoreLocal("ollama/qwen2.5-coder:7b", "qwen2.5-coder:7b"); got != want {
		t.Errorf("ScoreLocal = %d, want the family score %d", got, want)
	}
	if got := db.SourceLocal("ollama/qwen2.5-coder:7b", "qwen2.5-coder:7b"); got != "builtin" {
		t.Errorf("SourceLocal = %q, want \"builtin\"", got)
	}
}

// One spelling, not two. The bare model name is what `models add` invites and
// what `models list` then reports as "not discovered on this machine", so
// honouring it here would leave the score and the discovery answer disagreeing
// about the same entry.
func TestScoreLocal_TheBareModelNameIsNotAHeadID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	if _, err := AddModel(path, Entry{
		ID: "qwen3:0.6b", Name: "bare", Provider: "ollama", CapScore: 99,
	}); err != nil {
		t.Fatal(err)
	}
	db, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := db.ScoreLocal("ollama/qwen3:0.6b", "qwen3:0.6b"); got == 99 {
		t.Error("a bare model name scored a head; that is a second key for the same thing, and models list joins on the head id only")
	}
}

// Every id in the embedded catalog is either bare or env/-prefixed, so reading
// the index first can only ever surface an overlay entry. If a curated entry
// ever took a local head's shape it would silently restate every score on the
// machine, so the property is asserted rather than assumed.
func TestScoreLocal_NoBuiltinEntryIsShapedLikeALocalHead(t *testing.T) {
	db, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range db.Entries() {
		for _, prefix := range []string{"ollama/", "lmstudio/", "litellm/", "llamacpp/"} {
			if len(e.ID) > len(prefix) && e.ID[:len(prefix)] == prefix {
				t.Errorf("built-in entry %q is shaped like a local head id, so ScoreLocal would override the family score for it", e.ID)
			}
		}
	}
}
