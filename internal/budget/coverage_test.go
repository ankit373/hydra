// SPDX-License-Identifier: MIT

package budget

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

// Mode.String labels the governor band shown in `hyctl status`. An unlabelled
// or mislabelled band tells the user the wrong thing about whether to compact.
func TestMode_StringCoversEveryBand(t *testing.T) {
	want := map[Mode]string{
		ModeNormal:    "normal",
		ModeCompact:   "compact",
		ModeCaution:   "caution",
		ModeWarning:   "warning",
		ModeCritical:  "critical",
		ModeEmergency: "emergency",
	}
	seen := map[string]bool{}
	for m, s := range want {
		got := m.String()
		if got != s {
			t.Errorf("Mode(%d).String() = %q, want %q", int(m), got, s)
		}
		if seen[got] {
			t.Errorf("two bands share the label %q — the user cannot tell them apart", got)
		}
		seen[got] = true
	}
	// An out-of-range mode must degrade to "normal" rather than print an empty
	// string, which would render as a blank governor state.
	if got := Mode(99).String(); got != "normal" {
		t.Errorf("Mode(99).String() = %q, want normal", got)
	}
}

// LoadWindows resolves each model's context window; a missing or unusable
// registry must yield an empty map, never a nil map a caller would panic on.
func TestLoadWindows_DegradesToAnEmptyMap(t *testing.T) {
	dir := t.TempDir()

	// No registry on disk at all → the embedded copy is used, which must parse.
	if got := LoadWindows(dir); got == nil {
		t.Fatal("LoadWindows returned a nil map")
	}

	// A malformed on-disk registry must not panic or return nil.
	reg := filepath.Join(dir, "registry")
	if err := os.MkdirAll(reg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reg, "models.yaml"), []byte("models: [oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := LoadWindows(dir)
	if got == nil {
		t.Fatal("malformed models.yaml returned a nil map")
	}
	if len(got) != 0 {
		t.Errorf("malformed models.yaml produced %d windows", len(got))
	}
}

func TestLoadWindows_EmbeddedRegistryHasUsableWindows(t *testing.T) {
	got := LoadWindows("")
	if len(got) == 0 {
		t.Fatal("no context windows from the embedded registry — the governor " +
			"cannot compute a percentage without them")
	}
	for id, w := range got {
		if w <= 0 {
			t.Errorf("%s has a context window of %d; a zero window makes claude_pct "+
				"divide by zero or report 0%% forever", id, w)
		}
	}
}

// LoadWindows skips entries with no id, and supplies a fallback window for
// ollama models that declare none.
func TestLoadWindows_SkipsUnusableEntriesAndFallsBack(t *testing.T) {
	dir := t.TempDir()
	reg := filepath.Join(dir, "registry")
	if err := os.MkdirAll(reg, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `
models:
  - {id: "",        provider: openai, context_window: 100}
  - {id: has-win,   provider: openai, context_window: 4096}
  - {id: ollama-no-win, provider: ollama}
`
	if err := os.WriteFile(filepath.Join(reg, "models.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	got := LoadWindows(dir)
	if _, ok := got[""]; ok {
		t.Error("an entry with no id produced a window keyed on the empty string")
	}
	if got["has-win"] != 4096 {
		t.Errorf("has-win = %d, want 4096", got["has-win"])
	}
	if got["ollama-no-win"] <= 0 {
		t.Errorf("an ollama model with no declared window got %d; it needs a fallback "+
			"or the governor divides by zero", got["ollama-no-win"])
	}
}

// logNormCDF returns the LOG of the standard normal CDF, not the CDF — the
// name says so and I read it as the latter first. It is computed in log space
// precisely so the deep tail stays finite instead of underflowing to zero,
// which is what would make the governor's risk estimate collapse to "no risk"
// exactly when the estimate matters least and the arithmetic is hardest.
//
// The contract, then: monotonically increasing, never positive (a log of a
// probability), and finite even a billion sigma out.
func TestLogNormCDF_IsAWellFormedLogCDF(t *testing.T) {
	prev := math.Inf(-1)
	for _, x := range []float64{-1e9, -100, -3, -1, 0, 1, 3, 100, 1e9} {
		got := logNormCDF(x)
		if math.IsNaN(got) || math.IsInf(got, 0) {
			t.Fatalf("logNormCDF(%v) = %v — the log-space form exists to keep this finite", x, got)
		}
		if got > 0 {
			t.Errorf("logNormCDF(%v) = %v; a log-probability cannot be positive", x, got)
		}
		if got < prev {
			t.Errorf("not monotonic: logNormCDF returned %v after %v", got, prev)
		}
		prev = got
	}
	// exp(logNormCDF(0)) must be Φ(0) = 0.5.
	if mid := math.Exp(logNormCDF(0)); mid < 0.49 || mid > 0.51 {
		t.Errorf("exp(logNormCDF(0)) = %v, want ~0.5", mid)
	}
	// …and the far tail must be a very small probability, not zero.
	if tail := math.Exp(logNormCDF(-10)); tail <= 0 || tail > 1e-20 {
		t.Errorf("exp(logNormCDF(-10)) = %v, want a tiny positive probability", tail)
	}
}

// FirstPassageProb estimates the chance of crossing the ceiling. Degenerate
// inputs must produce a usable probability rather than NaN, which would render
// as "NaN%" in hyctl status.
func TestFirstPassageProb_DegenerateInputsStayFinite(t *testing.T) {
	cases := []struct{ cur, rate, ceil, horizon float64 }{
		{0, 0, 80, 60},
		{80, 0, 80, 60},
		{90, 5, 80, 60},  // already past the ceiling
		{10, -5, 80, 60}, // shrinking
		{50, 1, 50, 0},   // no horizon
		{50, 1, 0, 60},   // zero ceiling
	}
	for _, c := range cases {
		got := FirstPassageProb(c.cur, c.rate, c.ceil, c.horizon)
		if math.IsNaN(got) || math.IsInf(got, 0) {
			t.Errorf("FirstPassageProb(%v, %v, %v, %v) = %v", c.cur, c.rate, c.ceil, c.horizon, got)
		}
		if got < 0 || got > 1 {
			t.Errorf("FirstPassageProb(%v, %v, %v, %v) = %v, outside [0,1]",
				c.cur, c.rate, c.ceil, c.horizon, got)
		}
	}
}
