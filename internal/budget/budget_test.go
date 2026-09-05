// SPDX-License-Identifier: MIT

package budget

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestModeFor(t *testing.T) {
	cases := []struct {
		pct  int
		want Mode
	}{
		{0, ModeNormal},
		{49, ModeNormal},
		{50, ModeCompact},
		{64, ModeCompact},
		{65, ModeCaution},
		{69, ModeCaution},
		{70, ModeWarning},
		{74, ModeWarning},
		{75, ModeCritical},
		{79, ModeCritical},
		{80, ModeEmergency},
		{100, ModeEmergency},
	}
	for _, c := range cases {
		got := ModeFor(c.pct)
		if got != c.want {
			t.Errorf("ModeFor(%d) = %s, want %s", c.pct, got, c.want)
		}
	}
}

func TestModeString(t *testing.T) {
	if ModeNormal.String() != "normal" {
		t.Errorf("unexpected string for ModeNormal: %s", ModeNormal.String())
	}
	if ModeEmergency.String() != "emergency" {
		t.Errorf("unexpected string for ModeEmergency: %s", ModeEmergency.String())
	}
}

func TestTracker_Update(t *testing.T) {
	tr := &Tracker{modelID: "m1"}
	snap := tr.Update(150_000, 200_000, "real")

	if snap.ModelID != "m1" {
		t.Errorf("ModelID: want m1, got %s", snap.ModelID)
	}
	if snap.Pct != 75 {
		t.Errorf("Pct: want 75, got %d", snap.Pct)
	}
	if snap.Mode != ModeCritical {
		t.Errorf("Mode: want critical, got %s", snap.Mode)
	}
	if snap.Source != "real" {
		t.Errorf("Source: want real, got %s", snap.Source)
	}
}

func TestTracker_PctClamped(t *testing.T) {
	tr := &Tracker{modelID: "m"}
	snap := tr.Update(999_999, 100, "estimate")
	if snap.Pct != 100 {
		t.Errorf("Pct should be clamped to 100, got %d", snap.Pct)
	}
}

func TestTracker_ZeroWindow(t *testing.T) {
	tr := &Tracker{modelID: "m"}
	snap := tr.Update(1000, 0, "real")
	if snap.Pct != 0 {
		t.Errorf("zero window should produce 0 pct, got %d", snap.Pct)
	}
}

func TestRegistry_RecordAndGet(t *testing.T) {
	reg := NewRegistry(map[string]int{"claude-core": 200_000})

	snap := reg.Record("claude-core", 100_000, "real")
	if snap.Pct != 50 {
		t.Errorf("want 50%%, got %d%%", snap.Pct)
	}
	if snap.Mode != ModeCompact {
		t.Errorf("want compact mode, got %s", snap.Mode)
	}

	got := reg.Get("claude-core")
	if got.Pct != 50 {
		t.Errorf("Get: want 50%%, got %d%%", got.Pct)
	}
}

func TestRegistry_UnknownModelFallback(t *testing.T) {
	reg := NewRegistry(nil)
	snap := reg.Record("unknown-model", 180_000, "estimate")
	// fallback window = 200_000
	if snap.Window != fallbackCloud {
		t.Errorf("want fallback window %d, got %d", fallbackCloud, snap.Window)
	}
	if snap.Pct != 90 {
		t.Errorf("want 90%%, got %d%%", snap.Pct)
	}
}

func TestRegistry_All(t *testing.T) {
	reg := NewRegistry(map[string]int{"a": 100, "b": 200})
	reg.Record("a", 50, "real")
	reg.Record("b", 100, "real")

	all := reg.All()
	if len(all) != 2 {
		t.Fatalf("want 2 snapshots, got %d", len(all))
	}
}

func TestRegistry_GetMissing(t *testing.T) {
	reg := NewRegistry(nil)
	snap := reg.Get("no-such-model")
	if snap.ModelID != "no-such-model" {
		t.Errorf("ModelID: want no-such-model, got %s", snap.ModelID)
	}
	if snap.Pct != 0 {
		t.Errorf("missing model should have 0 pct, got %d", snap.Pct)
	}
}

func TestRegistry_Concurrent(t *testing.T) {
	reg := NewRegistry(map[string]int{"m": 1_000_000})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			reg.Record("m", i*1000, "real")
			reg.Get("m")
		}()
	}
	wg.Wait()
	snap := reg.Get("m")
	if snap.Window != 1_000_000 {
		t.Errorf("window corrupted under concurrency: %d", snap.Window)
	}
}

func TestAppendPctHistory(t *testing.T) {
	// Appends on change, ignores non-positive, dedups consecutive, bounds length.
	h := AppendPctHistory(nil, 40, 12)
	h = AppendPctHistory(h, 40, 12) // no change → no append
	h = AppendPctHistory(h, 0, 12)  // unknown → ignored
	h = AppendPctHistory(h, 47, 12)
	if len(h) != 2 || h[0] != 40 || h[1] != 47 {
		t.Fatalf("append/dedup failed: %v", h)
	}
	// Bounding keeps the newest max entries.
	h = nil
	for i := 1; i <= 20; i++ {
		h = AppendPctHistory(h, i, 5)
	}
	if len(h) != 5 || h[0] != 16 || h[4] != 20 {
		t.Errorf("bounding failed: %v", h)
	}
}

func TestFirstPassageProb_EdgeCases(t *testing.T) {
	if p := FirstPassageProb(0, 0.1, 0.1, 3); p != 1 {
		t.Errorf("already-at-barrier should be certain, got %v", p)
	}
	if p := FirstPassageProb(0.2, 0.1, 0.1, 0); p != 0 {
		t.Errorf("no horizon should be impossible, got %v", p)
	}
	if p := FirstPassageProb(0.2, 0, 0, 3); p != 0 {
		t.Errorf("no drift, no vol → 0, got %v", p)
	}
	if p := FirstPassageProb(0.2, 0.1, 0, 3); p != 1 { // μH = 0.3 ≥ 0.2
		t.Errorf("drift reaches barrier deterministically → 1, got %v", p)
	}
	if p := FirstPassageProb(0.5, 0.01, 0, 3); p != 0 { // μH = 0.03 < 0.5
		t.Errorf("drift too slow in horizon → 0, got %v", p)
	}
}

func TestFirstPassageProb_Monotonic(t *testing.T) {
	if !(FirstPassageProb(0.3, 0.05, 0.05, 3) > FirstPassageProb(0.3, 0.02, 0.05, 3)) {
		t.Error("probability should rise with drift")
	}
	if !(FirstPassageProb(0.2, 0.03, 0.05, 3) > FirstPassageProb(0.4, 0.03, 0.05, 3)) {
		t.Error("probability should fall with distance to barrier")
	}
}

func TestRiskFromHistory(t *testing.T) {
	// Fewer than two points → no signal.
	if br, r := RiskFromHistory([]int{55}); br != 0 || r != 0 {
		t.Errorf("single point should give no signal, got br=%v r=%v", br, r)
	}
	// Fast climb that reaches the 80% barrier within the horizon → high risk.
	brFast, rFast := RiskFromHistory([]int{60, 68, 74})
	if brFast <= 0 {
		t.Errorf("rising series should have positive burn rate, got %v", brFast)
	}
	if rFast < 0.5 {
		t.Errorf("fast climb near barrier should be high risk, got %v", rFast)
	}
	// Same distance to barrier but a slower rate → strictly lower risk.
	_, rSlower := RiskFromHistory([]int{72, 73, 74})
	if !(rFast > rSlower) {
		t.Errorf("faster climb should be riskier: fast=%v slower=%v", rFast, rSlower)
	}
	// Slow climb far from barrier → low risk.
	_, rSlow := RiskFromHistory([]int{20, 21, 22})
	if rSlow > 0.1 {
		t.Errorf("slow climb far from barrier should be low risk, got %v", rSlow)
	}
	// Flat series → zero burn, zero risk.
	if br, r := RiskFromHistory([]int{60, 60, 60}); br != 0 || r != 0 {
		t.Errorf("flat series should give no signal, got br=%v r=%v", br, r)
	}
}

func TestEffectiveMode(t *testing.T) {
	// No risk → exactly the level band (backward compatible).
	for _, pct := range []int{0, 55, 72, 90} {
		if EffectiveMode(pct, 0) != ModeFor(pct) {
			t.Errorf("EffectiveMode(%d, 0) should equal ModeFor", pct)
		}
	}
	// High risk floors the mode above a low band.
	if EffectiveMode(55, 0.9) != ModeCritical { // band=compact, risk≥0.8 → critical
		t.Errorf("high risk should floor to critical, got %s", EffectiveMode(55, 0.9))
	}
	if EffectiveMode(55, 0.6) != ModeWarning { // band=compact, risk≥0.5 → warning
		t.Errorf("moderate risk should floor to warning, got %s", EffectiveMode(55, 0.6))
	}
	// Never downgrades below the level band.
	if EffectiveMode(90, 0.6) != ModeEmergency { // band already emergency
		t.Errorf("risk floor must not lower a high band, got %s", EffectiveMode(90, 0.6))
	}
}

// This used to assert an empty map for a missing registry, which described the
// bug rather than a requirement: models.yaml was absent on every installed
// binary, so context windows silently fell back to a flat 200k for everything
// (#238). It is embedded now, so the honest contract is that a machine with no
// on-disk registry still gets the real windows.
func TestLoadWindows_UsesEmbeddedRegistryWhenNoneIsOnDisk(t *testing.T) {
	windows := LoadWindows("/no/such/path")
	if len(windows) == 0 {
		t.Fatal("no windows loaded, the embedded registry should always be readable")
	}
	for id, w := range windows {
		if w <= 0 {
			t.Errorf("%s has a non-positive context window (%d)", id, w)
		}
	}
}

// The override is the reason the files stay editable YAML rather than becoming
// Go constants; if it stops working, operators lose the ability to retune
// routing without a rebuild and would have no way to tell.
func TestLoadWindows_OnDiskRegistryOverridesTheEmbeddedCopy(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "registry"), 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := "models:\n  - id: only-model\n    provider: ollama\n    context_window: 4242\n"
	if err := os.WriteFile(filepath.Join(home, "registry", "models.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	windows := LoadWindows(home)
	if got := windows["only-model"]; got != 4242 {
		t.Errorf("on-disk models.yaml ignored: only-model window = %d, want 4242", got)
	}
	if len(windows) != 1 {
		t.Errorf("embedded entries leaked through the override: got %d windows, want 1", len(windows))
	}
}

func TestWindowFor_Fallback(t *testing.T) {
	w := windowFor(map[string]int{"a": 100}, "unknown")
	if w != fallbackCloud {
		t.Errorf("want fallback %d, got %d", fallbackCloud, w)
	}
}
