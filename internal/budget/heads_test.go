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

// A ceiling is a hard bound, so it caps a declaration rather than losing to
// one: a models.yaml entry claiming 32768 for a 2048-token model is wrong
// whoever wrote it.
func TestWindowsForHeads_CeilingCapsADeclaredWindow(t *testing.T) {
	home := writeModels(t, `models:
  - id: ollama/small
    provider: ollama
    context_window: 32768
  - id: ollama/large
    provider: ollama
    context_window: 2048
`)
	heads := []provider.Head{
		ollamaHead("ollama/small", 2048),  // declared above its ceiling
		ollamaHead("ollama/large", 40960), // declared below its ceiling
	}
	w := WindowsForHeads(home, heads)

	if got := windowFor(w, "ollama/small"); got != 2048 {
		t.Errorf("declared 32768 over a 2048 ceiling gave %d, want the ceiling", got)
	}
	// The other direction must NOT be raised: a ceiling of 40960 does not mean
	// the server will allocate it, which is the whole reason min is used.
	if got := windowFor(w, "ollama/large"); got != 2048 {
		t.Errorf("declared 2048 under a 40960 ceiling gave %d, want the declaration", got)
	}
}

// Nothing declared and nothing reported: a local head still must not be
// treated as cloud-sized.
func TestWindowsForHeads_UndeclaredLocalHeadGetsTheLocalDefault(t *testing.T) {
	w := WindowsForHeads(t.TempDir(), []provider.Head{ollamaHead("ollama/mystery:1b", 0)})
	got := windowFor(w, "ollama/mystery:1b")
	if got != ollamaDefaultCtx {
		t.Errorf("window %d, want the local default %d", got, ollamaDefaultCtx)
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
