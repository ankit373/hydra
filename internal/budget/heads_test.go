// SPDX-License-Identifier: MIT

package budget

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
)

// writeModels puts a models.yaml under home's registry dir, which registry.Read
// prefers over the embedded copy, so a test declares exactly what it means to.
func writeModels(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "registry"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "registry", "models.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

func ollamaHead(id string, ctxMax int) provider.Head {
	h := provider.Head{
		ID: id, Name: id, Provider: "local", Source: "port",
		CapScore: 63, LocalOnly: true, AuthReady: true,
		Meta: map[string]string{"model_source": "ollama"},
	}
	if ctxMax > 0 {
		h.Meta["model_ctx_max"] = strconv.Itoa(ctxMax)
	}
	return h
}

// The bug: Record is called with a head id, LoadWindows keys on models.yaml
// ids, and the two only partly overlap, so a local head missed and windowFor
// returned fallbackCloud. Measured on this machine, every Ollama head was
// budgeted at 200000 against a real 4096 (#764).
func TestWindowsForHeads_LocalHeadsResolveInsteadOfFallingThroughToCloud(t *testing.T) {
	heads := []provider.Head{
		ollamaHead("ollama/qwen3:0.6b", 40960),
		ollamaHead("ollama/Qwen2.5-Coder:7b", 32768),
		ollamaHead("ollama/nomic-embed-text:latest", 2048),
	}
	w := WindowsForHeads(t.TempDir(), heads)

	// The ceiling caps the assumed default, which is what makes nomic-embed
	// 2048 and the other two 4096, exactly what /api/ps reports for each.
	want := map[string]int{
		"ollama/qwen3:0.6b":              4096,
		"ollama/Qwen2.5-Coder:7b":        4096,
		"ollama/nomic-embed-text:latest": 2048,
	}
	for id, expect := range want {
		if got := windowFor(w, id); got != expect {
			t.Errorf("%s: window %d, want %d", id, got, expect)
		}
		if got := windowFor(w, id); got == fallbackCloud {
			t.Errorf("%s: still falling through to the cloud fallback", id)
		}
	}
}

// A ceiling is a hard bound, so it caps the assumed window, and a ceiling
// above the assumption does NOT raise it: 40960 architectural does not mean
// the server will allocate 40960, which is the whole reason min is used.
func TestWindowsForHeads_CeilingCapsTheAssumedWindow(t *testing.T) {
	heads := []provider.Head{
		ollamaHead("ollama/small", 2048),  // ceiling under the local default
		ollamaHead("ollama/large", 40960), // ceiling over it
	}
	w := WindowsForHeads(t.TempDir(), heads)

	if got := windowFor(w, "ollama/small"); got != 2048 {
		t.Errorf("a 2048 ceiling gave %d, want the ceiling", got)
	}
	if got := windowFor(w, "ollama/large"); got != localDefaultCtx {
		t.Errorf("a 40960 ceiling gave %d, want the local default %d", got, localDefaultCtx)
	}
}

// The #764 regression guard, and the reason #777 exists. models.yaml already
// carries 32768 for Qwen2.5-Coder:7b, which is measured at 4096. #764's fix
// held only because no models.yaml *id* happened to equal a local head id:
// rename that entry's id and the head silently went back to 8x over.
//
// A local head's window is decided by its server at runtime and the registry
// cannot know it, so a declaration is ignored rather than merely absent.
func TestWindowsForHeads_ALocalDeclarationCannotUndoTheMeasuredDefault(t *testing.T) {
	home := writeModels(t, `models:
  - id: ollama/Qwen2.5-Coder:7b
    name: Qwen2.5-Coder 7b
    provider: ollama
    model_flag: Qwen2.5-Coder:7b
    context_window: 32768
`)
	// Named by id, by provider/model_flag and by display name at once: no
	// route into the declaration may reach a local head.
	head := ollamaHead("ollama/Qwen2.5-Coder:7b", 32768)
	head.Name = "Qwen2.5-Coder 7b"

	got := windowFor(WindowsForHeads(home, []provider.Head{head}), head.ID)
	if got == 32768 {
		t.Fatal("a models.yaml declaration overrode the measured local default, undoing #764")
	}
	if got != localDefaultCtx {
		t.Errorf("window %d, want the measured local default %d", got, localDefaultCtx)
	}
}

// The other half of #777: `claude` is the head id, `claude-core` is the entry
// declaring the window, so keying on id alone missed and fell through to
// fallbackCloud, which is also 200000. The right answer with no mechanism
// behind it. A declaration has to be followed, so changing it must change the
// budget, which is why the fixture uses a number nothing else could produce.
func TestWindowsForHeads_CloudHeadFollowsItsDeclarationByName(t *testing.T) {
	home := writeModels(t, `models:
  - id: claude-core
    name: Claude Code
    provider: claude
    context_window: 123456
`)
	head := provider.Head{
		ID: "claude", Name: "Claude Code", Provider: "anthropic",
		Source: "cli", Meta: map[string]string{},
	}
	got := windowFor(WindowsForHeads(home, []provider.Head{head}), "claude")
	if got == fallbackCloud {
		t.Fatal("still landing on fallbackCloud, so the declaration is not being followed")
	}
	if got != 123456 {
		t.Errorf("window %d, want the declared 123456", got)
	}
}

// A port-discovered head is named provider/model_flag, the third key, and the
// one internal/cost already resolves entries by. Tested on a cloud-ish head
// because a local one ignores declarations entirely.
func TestWindowsForHeads_CloudHeadFollowsItsDeclarationByProviderModelFlag(t *testing.T) {
	home := writeModels(t, `models:
  - id: registry-local-id
    name: Some Display Name
    provider: acme
    model_flag: turbo-9
    context_window: 54321
`)
	head := provider.Head{
		ID: "acme/turbo-9", Name: "something else entirely",
		Provider: "acme", Source: "env", Meta: map[string]string{},
	}
	got := windowFor(WindowsForHeads(home, []provider.Head{head}), "acme/turbo-9")
	if got != 54321 {
		t.Errorf("window %d, want the declared 54321 resolved by provider/model_flag", got)
	}
}

// Resolution order has to be fixed, not decided by map iteration: id beats
// provider/model_flag beats name.
func TestWindowsForHeads_ResolutionOrderIsIdThenFlagThenName(t *testing.T) {
	home := writeModels(t, `models:
  - id: acme/turbo-9
    name: unrelated
    provider: none
    context_window: 111
  - id: by-flag
    name: Turbo Nine
    provider: acme
    model_flag: turbo-9
    context_window: 222
  - id: by-name
    name: acme/turbo-9
    provider: other
    context_window: 333
`)
	head := provider.Head{
		ID: "acme/turbo-9", Name: "Turbo Nine",
		Provider: "acme", Source: "env", Meta: map[string]string{},
	}
	// id (111) must win over provider/model_flag (222) and over the entry
	// whose *name* is this head's id (333).
	if got := windowFor(WindowsForHeads(home, []provider.Head{head}), head.ID); got != 111 {
		t.Errorf("window %d, want 111: id must outrank flag and name", got)
	}
}

// A repeated key resolves in file order, so the same registry always gives
// the same answer.
func TestLoadDeclarations_FirstEntryWinsOnARepeatedName(t *testing.T) {
	home := writeModels(t, `models:
  - id: first
    name: Same Name
    provider: acme
    context_window: 1000
  - id: second
    name: Same Name
    provider: acme
    context_window: 2000
`)
	d := loadDeclarations(home)
	for i := 0; i < 50; i++ {
		if got := d.byName["Same Name"]; got != 1000 {
			t.Fatalf("repeated name resolved to %d, want the first entry's 1000", got)
		}
	}
}

// Nothing declared and nothing reported: a local head still must not be
// treated as cloud-sized.
func TestWindowsForHeads_UndeclaredLocalHeadGetsTheLocalDefault(t *testing.T) {
	w := WindowsForHeads(t.TempDir(), []provider.Head{ollamaHead("ollama/mystery:1b", 0)})
	got := windowFor(w, "ollama/mystery:1b")
	if got != localDefaultCtx {
		t.Errorf("window %d, want the local default %d", got, localDefaultCtx)
	}
	if got == fallbackCloud {
		t.Error("an undeclared local head is still budgeted as cloud-sized")
	}
}

// Cloud heads are deliberately untouched by this change.
func TestWindowsForHeads_CloudHeadsAreUnchanged(t *testing.T) {
	home := writeModels(t, `models:
  - id: opus-thinking
    provider: antigravity
    context_window: 200000
  - id: flash-high
    provider: antigravity
    context_window: 1000000
`)
	heads := []provider.Head{
		{ID: "opus-thinking", Provider: "antigravity", Source: "registry", Meta: map[string]string{}},
		{ID: "flash-high", Provider: "antigravity", Source: "registry", Meta: map[string]string{}},
		{ID: "claude", Provider: "anthropic", Source: "cli", Meta: map[string]string{}},
	}
	w := WindowsForHeads(home, heads)

	for id, expect := range map[string]int{
		"opus-thinking": 200000,
		"flash-high":    1000000,
		// Undeclared and not local: the cloud fallback, same as before.
		"claude": fallbackCloud,
	} {
		if got := windowFor(w, id); got != expect {
			t.Errorf("%s: window %d, want %d", id, got, expect)
		}
	}
}

// The point of the whole change: the governor has to actually fire. 3000
// tokens is 73% of a real 4096 window and 1.5% of the 200000 it used to be
// assumed to have, so the same dispatch went from "normal" to "warning".
func TestRegistry_GovernorFiresOnARealLocalWindow(t *testing.T) {
	const used = 3000
	head := ollamaHead("ollama/qwen3:0.6b", 40960)

	fixed := NewRegistry(WindowsForHeads(t.TempDir(), []provider.Head{head}))
	got := fixed.Record(head.ID, used, "real")
	if got.Window != 4096 {
		t.Fatalf("window %d, want 4096", got.Window)
	}
	if got.Mode != ModeWarning {
		t.Errorf("%d of %d is %d%%, mode %v, want %v",
			used, got.Window, got.Pct, got.Mode, ModeWarning)
	}

	// And the same input against the old behaviour, to show this test would
	// have passed for the wrong reason if it only asserted "not normal".
	old := NewRegistry(map[string]int{})
	if was := old.Record(head.ID, used, "real"); was.Mode != ModeNormal || was.Window != fallbackCloud {
		t.Fatalf("the pre-fix path no longer reproduces: window %d mode %v", was.Window, was.Mode)
	}
}

func TestContextCeiling_OnlyReportsWhatWasActuallyReported(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
		ok   bool
	}{
		{"40960", 40960, true},
		{"2048", 2048, true},
		{"", 0, false},
		{"0", 0, false},
		{"-1", 0, false},
		{"lots", 0, false},
	} {
		h := provider.Head{Meta: map[string]string{}}
		if tc.raw != "" {
			h.Meta["model_ctx_max"] = tc.raw
		}
		got, ok := ContextCeiling(h)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ContextCeiling(%q) = (%d, %v), want (%d, %v)", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
	// A head with no Meta at all must not panic or invent a ceiling.
	if _, ok := ContextCeiling(provider.Head{}); ok {
		t.Error("a head with no Meta reported a ceiling")
	}
}

// LM Studio heads are LocalOnly too and report no ceiling, so they take the
// same local default. Covered explicitly because the constant is measured on
// Ollama and only assumed here, and because a local head must never reach the
// cloud fallback whichever server it came from.
func TestWindowsForHeads_LMStudioHeadsAreLocalToo(t *testing.T) {
	head := provider.Head{
		ID: "lmstudio/some-model", Name: "some-model (LM Studio)",
		Provider: "local", Source: "port", LocalOnly: true, AuthReady: true,
		Meta: map[string]string{},
	}
	got := windowFor(WindowsForHeads(t.TempDir(), []provider.Head{head}), head.ID)
	if got == fallbackCloud {
		t.Fatal("an LM Studio head is budgeted as cloud-sized")
	}
	if got != localDefaultCtx {
		t.Errorf("window %d, want the local default %d", got, localDefaultCtx)
	}
}
