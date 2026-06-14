package budget

import (
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

func TestLoadWindows_Fallback(t *testing.T) {
	// Non-existent path → empty map, no panic.
	windows := LoadWindows("/no/such/path")
	if windows == nil {
		t.Fatal("LoadWindows should return empty map, not nil")
	}
	if len(windows) != 0 {
		t.Errorf("want empty map for missing registry, got %d entries", len(windows))
	}
}

func TestWindowFor_Fallback(t *testing.T) {
	w := windowFor(map[string]int{"a": 100}, "unknown")
	if w != fallbackCloud {
		t.Errorf("want fallback %d, got %d", fallbackCloud, w)
	}
}
