// SPDX-License-Identifier: MIT

package sysinfo

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// These cover the memory-sizing logic that decides which local model a user is
// told to pull. Getting it wrong either recommends a model that swaps the
// machine to a halt, or refuses a model that would have run fine.

// sandboxHome points $HOME at a temp dir so the history file under test is not
// the developer's real one. testutil is not importable here (it would be an
// import cycle through config), and the surface needed is one env var.
func sandboxHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	return home
}

// EffectiveVRAMGB has four sources in priority order; each has a different
// failure mode, so each is pinned separately.
func TestEffectiveVRAM_PriorityOrder(t *testing.T) {
	tests := []struct {
		name  string
		specs Specs
		want  float64
	}{{
		// A discrete GPU's VRAM is separate from system RAM and is the most
		// reliable figure available; 10% goes to driver overhead.
		name:  "discrete gpu wins outright",
		specs: Specs{TotalRAMGB: 64, FreeRAMGB: 40, GPUVRAMGB: 24},
		want:  24 * 0.90,
	}, {
		// Apple Silicon has unified memory: GPUVRAMGB is not a separate pool,
		// so treating it as one would double-count the same bytes.
		name:  "apple silicon ignores the vram field",
		specs: Specs{TotalRAMGB: 32, FreeRAMGB: 20, GPUVRAMGB: 32, IsAppleSilicon: true},
		want:  20 - osMinBufferGB,
	}, {
		// Historical P75 beats the instantaneous snapshot: a momentary dip
		// while a build runs must not permanently downgrade the recommendation.
		name: "reliable history beats the current snapshot",
		specs: Specs{
			TotalRAMGB: 32, FreeRAMGB: 4,
			History: &HistoricalStats{Reliable: true, P75FreeGB: 18},
		},
		want: 18 - osMinBufferGB,
	}, {
		name: "unreliable history is ignored",
		specs: Specs{
			TotalRAMGB: 32, FreeRAMGB: 10,
			History: &HistoricalStats{Reliable: false, P75FreeGB: 18},
		},
		want: 10 - osMinBufferGB,
	}, {
		// Nothing known: a conservative fraction, not the full machine.
		name:  "no free reading falls back to a fraction of total",
		specs: Specs{TotalRAMGB: 32},
		want:  32*0.55 - osMinBufferGB,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.specs.EffectiveVRAMGB()
			if diff := got - tt.want; diff > 0.001 || diff < -0.001 {
				t.Errorf("EffectiveVRAMGB() = %v, want %v", got, tt.want)
			}
		})
	}
}

// The OS buffer and the 85%-of-total ceiling are the two guards against
// recommending a model that pushes the machine into swap.
func TestEffectiveVRAM_ClampsAtBothEnds(t *testing.T) {
	// Free memory below the OS buffer must floor at 0, never go negative — a
	// negative budget compares as "smaller than every model", which happens to
	// be right, but the number is then printed to the user.
	tight := Specs{TotalRAMGB: 16, FreeRAMGB: 0.5}
	if got := tight.EffectiveVRAMGB(); got != 0 {
		t.Errorf("EffectiveVRAMGB() = %v with %.1fGB free, want 0", got, tight.FreeRAMGB)
	}

	// A free reading larger than makes sense (a broken /proc, a VM balloon)
	// must not hand out the whole machine.
	bogus := Specs{TotalRAMGB: 16, FreeRAMGB: 99}
	if got, ceiling := bogus.EffectiveVRAMGB(), 16*0.85; got != ceiling {
		t.Errorf("EffectiveVRAMGB() = %v with a bogus free reading, want the %.1f ceiling",
			got, ceiling)
	}
}

// Summary and MemoryNote are the strings the user actually reads. Each branch
// must name real numbers and must never render an unknown machine as an empty
// one (#258).
func TestSummaryAndMemoryNote_EveryBranch(t *testing.T) {
	tests := []struct {
		name        string
		specs       Specs
		wantSummary []string
		wantNote    []string
	}{{
		name:        "unknown hardware says so in both",
		specs:       Specs{},
		wantSummary: []string{"unknown"},
		wantNote:    []string{"could not be detected"},
	}, {
		name:        "apple silicon",
		specs:       Specs{TotalRAMGB: 32, FreeRAMGB: 20, IsAppleSilicon: true},
		wantSummary: []string{"Apple Silicon", "32GB total"},
		wantNote:    []string{"usable", "OS buffer"},
	}, {
		name:        "apple silicon with no headroom left",
		specs:       Specs{TotalRAMGB: 32, FreeRAMGB: 1.0, IsAppleSilicon: true},
		wantSummary: []string{"fully occupied"},
		wantNote:    []string{"close other apps"},
	}, {
		name:        "discrete gpu names the card",
		specs:       Specs{TotalRAMGB: 64, FreeRAMGB: 40, GPUVRAMGB: 24, GPUName: "RTX 4090"},
		wantSummary: []string{"RTX 4090", "24GB VRAM"},
		wantNote:    []string{"VRAM usable", "driver overhead"},
	}, {
		name:        "cpu only",
		specs:       Specs{TotalRAMGB: 16, FreeRAMGB: 9},
		wantSummary: []string{"CPU inference"},
		wantNote:    []string{"in use by other processes"},
	}, {
		name:        "cpu only with memory exhausted",
		specs:       Specs{TotalRAMGB: 16, FreeRAMGB: 1.2},
		wantSummary: []string{"fully occupied"},
		wantNote:    []string{"close other apps"},
	}, {
		// Total known but no free reading at all: the note must say the usage
		// is unknown rather than presenting the estimate as measured.
		name:        "total known, current usage unknown",
		specs:       Specs{TotalRAMGB: 16},
		wantSummary: []string{"16GB RAM"},
		wantNote:    []string{"current usage unknown"},
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary := tt.specs.Summary()
			for _, want := range tt.wantSummary {
				if !strings.Contains(summary, want) {
					t.Errorf("Summary() = %q, want it to mention %q", summary, want)
				}
			}
			note := tt.specs.MemoryNote()
			for _, want := range tt.wantNote {
				if !strings.Contains(note, want) {
					t.Errorf("MemoryNote() = %q, want it to mention %q", note, want)
				}
			}
		})
	}
}

// PressureWarning gates whether the user is told to close apps. An unknown
// reading must produce the "cannot size local models" message, not silence —
// silence reads as "everything is fine".
func TestPressureWarning_EveryLevel(t *testing.T) {
	tests := []struct {
		pressure Pressure
		want     string
	}{
		{PressureUnknown, "could not be detected"},
		{PressureModerate, "close other apps"},
		{PressureHigh, "smaller models recommended"},
		{PressureLow, ""},
	}
	for _, tt := range tests {
		s := Specs{TotalRAMGB: 16, FreeRAMGB: 4, MemPressure: tt.pressure}
		got := s.PressureWarning()
		if tt.want == "" {
			if got != "" {
				t.Errorf("PressureWarning() at %v = %q, want no warning", tt.pressure, got)
			}
			continue
		}
		if !strings.Contains(got, tt.want) {
			t.Errorf("PressureWarning() at %v = %q, want it to mention %q", tt.pressure, got, tt.want)
		}
	}
}

// computePressure's thresholds decide the warning above. They are a function of
// the ratio, so they are checked at the ratio boundaries.
func TestComputePressure_Thresholds(t *testing.T) {
	tests := []struct {
		name string
		s    Specs
		want Pressure
	}{
		{"no reading at all is unknown, not low", Specs{}, PressureUnknown},
		{"half the machine free is low", Specs{TotalRAMGB: 32, FreeRAMGB: 18}, PressureLow},
		{"a third free is moderate", Specs{TotalRAMGB: 32, FreeRAMGB: 10}, PressureModerate},
		{"almost nothing free is high", Specs{TotalRAMGB: 32, FreeRAMGB: 3}, PressureHigh},
	}
	for _, tt := range tests {
		if got := tt.s.computePressure(); got != tt.want {
			t.Errorf("%s: computePressure() = %v, want %v (effective %.1f of %.0f)",
				tt.name, got, tt.want, tt.s.EffectiveVRAMGB(), tt.s.TotalRAMGB)
		}
	}
}

// The recommendation list is ordered by capability, so the first model that
// fits is the best one that fits. The reason and cost strings are what the
// wizard prints next to each entry.
func TestOllamaRecommendations_RankAndExplain(t *testing.T) {
	// 12GB free on an Apple Silicon machine: the 10GB models fit, the 20GB
	// ones do not.
	s := Specs{TotalRAMGB: 32, FreeRAMGB: 12, IsAppleSilicon: true, MemPressure: PressureModerate}
	recs := s.OllamaRecommendations()
	if len(recs) != len(ollamaModels) {
		t.Fatalf("got %d recommendations, want one per model", len(recs))
	}

	var firstFit *ModelRecommendation
	for i := range recs {
		m := recs[i]
		if m.Fits && firstFit == nil {
			firstFit = &recs[i]
		}
		if m.Fits && m.RAMNeededGB > s.EffectiveVRAMGB() {
			t.Errorf("%s is marked Fits but needs %.0fGB of %.1fGB",
				m.Model, m.RAMNeededGB, s.EffectiveVRAMGB())
		}
		if m.Reason == "" || m.MemoryCost == "" {
			t.Errorf("%s has no reason/cost string to show the user: %+v", m.Model, m)
		}
		if !m.Fits && !strings.Contains(m.MemoryCost, "you have") {
			t.Errorf("%s does not fit but its cost string does not say what is available: %q",
				m.Model, m.MemoryCost)
		}
	}
	if firstFit == nil {
		t.Fatal("nothing fits in 10.5GB usable — the 3GB models should")
	}
	if !strings.Contains(firstFit.Reason, "unified memory") {
		t.Errorf("Apple Silicon reason = %q, want it to name unified memory", firstFit.Reason)
	}
	if !strings.Contains(firstFit.Reason, "memory moderate") {
		t.Errorf("reason = %q, want the moderate-pressure note appended", firstFit.Reason)
	}

	if best := s.BestOllamaModel(); best.Model != firstFit.Model {
		t.Errorf("BestOllamaModel() = %s, want the first fitting model %s", best.Model, firstFit.Model)
	}
	if !s.AnyLocalModelFits() {
		t.Error("AnyLocalModelFits() = false when models fit")
	}
}

func TestOllamaRecommendations_ReasonNamesWhereTheMemoryIs(t *testing.T) {
	gpu := Specs{TotalRAMGB: 64, FreeRAMGB: 40, GPUVRAMGB: 24, GPUName: "RTX 4090"}
	if r := gpu.BestOllamaModel().Reason; !strings.Contains(r, "RTX 4090") {
		t.Errorf("GPU reason = %q, want it to name the card", r)
	}
	cpu := Specs{TotalRAMGB: 32, FreeRAMGB: 20}
	if r := cpu.BestOllamaModel().Reason; !strings.Contains(r, "CPU inference") {
		t.Errorf("CPU reason = %q, want it to say CPU inference", r)
	}
	// Enough for a small model, but flagged high — the reason must still carry
	// the swap warning rather than reading as a clean fit.
	tight := Specs{TotalRAMGB: 16, FreeRAMGB: 4.6, MemPressure: PressureHigh}
	if r := tight.BestOllamaModel().Reason; !strings.Contains(r, "may swap") {
		t.Errorf("high-pressure reason = %q, want the swap warning", r)
	}
}

// Nothing fitting is a normal outcome on a small machine and must be reported
// as a memory verdict, not as an unknown-hardware one.
func TestBestOllamaModel_DistinguishesTooSmallFromUnknown(t *testing.T) {
	small := Specs{TotalRAMGB: 2, FreeRAMGB: 1.6}
	best := small.BestOllamaModel()
	if best.Fits {
		t.Fatalf("a 2GB machine fits %s", best.Model)
	}
	if !strings.Contains(best.Reason, "insufficient memory") {
		t.Errorf("Reason = %q, want it to say memory is insufficient", best.Reason)
	}
	if small.AnyLocalModelFits() {
		t.Error("AnyLocalModelFits() = true on a 2GB machine")
	}

	unknown := Specs{}
	if r := unknown.BestOllamaModel().Reason; !strings.Contains(r, "could not be detected") {
		t.Errorf("unknown hardware Reason = %q, want it to say hardware was unreadable, "+
			"not that memory is insufficient", r)
	}
}

// The history file drives the P75 estimate. It is written by every Detect(),
// so it must be rate-limited, tolerate a corrupt line, and drop stale samples.
func TestHistory_RateLimitedAppendAndParse(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, ".hydra", historyFile)

	recordSample(&Specs{TotalRAMGB: 32, FreeRAMGB: 12, WiredRAMGB: 4})
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no history was written: %v", err)
	}

	// A second call inside the sample interval must not append — Detect() runs
	// on every command, and an unbounded append would grow without limit.
	recordSample(&Specs{TotalRAMGB: 32, FreeRAMGB: 99})
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != len(first) {
		t.Errorf("a second sample was written %v after the first; the interval is %v",
			0, sampleInterval)
	}

	// Backdate the file past the interval and it must append again.
	old := time.Now().Add(-2 * sampleInterval)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	recordSample(&Specs{TotalRAMGB: 32, FreeRAMGB: 14})
	third, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(third) <= len(second) {
		t.Error("no sample was appended after the interval elapsed")
	}
}

func TestLoadHistory_StatsAndReliabilityThreshold(t *testing.T) {
	home := sandboxHome(t)

	// No file yet: nil, so EffectiveVRAMGB falls through to the snapshot.
	if got := LoadHistory(); got != nil {
		t.Errorf("LoadHistory() = %+v with no file, want nil", got)
	}

	dir := filepath.Join(home, ".hydra")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	lines := []string{
		`{"ts":"` + now.Add(-72*time.Hour).Format(time.RFC3339Nano) + `","total_gb":32,"free_gb":8}`,
		`{"ts":"` + now.Add(-48*time.Hour).Format(time.RFC3339Nano) + `","total_gb":32,"free_gb":12}`,
		`not json at all`,
		`{"ts":"` + now.Add(-24*time.Hour).Format(time.RFC3339Nano) + `","total_gb":32,"free_gb":16}`,
		// Older than historyDays — must be dropped, not averaged in.
		`{"ts":"` + now.AddDate(0, 0, -30).Format(time.RFC3339Nano) + `","total_gb":32,"free_gb":0.1}`,
	}
	path := filepath.Join(dir, historyFile)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := LoadHistory()
	if h == nil {
		t.Fatal("LoadHistory() = nil with three valid samples")
	}
	if h.Samples != 3 {
		t.Errorf("Samples = %d, want 3 (corrupt line skipped, 30-day-old sample dropped)", h.Samples)
	}
	if h.MinFreeGB != 8 || h.MaxFreeGB != 16 {
		t.Errorf("range = [%v, %v], want [8, 16] — the stale 0.1 sample leaked in",
			h.MinFreeGB, h.MaxFreeGB)
	}
	if h.AvgFreeGB != 12 {
		t.Errorf("AvgFreeGB = %v, want 12", h.AvgFreeGB)
	}
	if h.P75FreeGB < h.AvgFreeGB || h.P75FreeGB > h.MaxFreeGB {
		t.Errorf("P75 = %v, outside [avg %v, max %v]", h.P75FreeGB, h.AvgFreeGB, h.MaxFreeGB)
	}
	if !h.Reliable {
		t.Errorf("Reliable = false with %d samples, threshold is %d", h.Samples, minSamples)
	}
	if h.Days < 3 {
		t.Errorf("Days = %d, want the ~3-day span of the samples", h.Days)
	}
}

func TestLoadHistory_TooFewSamplesIsNotReliable(t *testing.T) {
	home := sandboxHome(t)
	dir := filepath.Join(home, ".hydra")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	line := `{"ts":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","total_gb":32,"free_gb":9}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, historyFile), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	h := LoadHistory()
	if h == nil {
		t.Fatal("LoadHistory() = nil with one sample")
	}
	if h.Reliable {
		t.Errorf("one sample was treated as reliable; %d are required", minSamples)
	}
	// And an unreliable history must not be used as the estimate.
	s := Specs{TotalRAMGB: 32, FreeRAMGB: 20, History: h}
	if got, want := s.EffectiveVRAMGB(), 20-osMinBufferGB; got != want {
		t.Errorf("EffectiveVRAMGB() = %v, want the snapshot-based %v — an unreliable "+
			"history was used", got, want)
	}
}

// percentile interpolates; the boundary cases are where an off-by-one shows up.
func TestPercentile_BoundariesAndInterpolation(t *testing.T) {
	if got := percentile(nil, 75); got != 0 {
		t.Errorf("percentile(nil) = %v, want 0", got)
	}
	one := []float64{7}
	if got := percentile(one, 75); got != 7 {
		t.Errorf("percentile([7], 75) = %v, want 7", got)
	}
	sorted := []float64{0, 10}
	if got := percentile(sorted, 100); got != 10 {
		t.Errorf("percentile(_, 100) = %v, want the max", got)
	}
	if got := percentile(sorted, 0); got != 0 {
		t.Errorf("percentile(_, 0) = %v, want the min", got)
	}
	if got := percentile(sorted, 50); got != 5 {
		t.Errorf("percentile([0,10], 50) = %v, want the interpolated 5", got)
	}
}

// Detect() is called on every command. It must never panic and never return
// nil, whatever the platform reports.
func TestDetect_IsAlwaysUsable(t *testing.T) {
	sandboxHome(t)

	s := Detect()
	if s == nil {
		t.Fatal("Detect() = nil")
	}
	if s.OS == "" || s.Arch == "" {
		t.Errorf("Detect() did not record the platform: %+v", s)
	}
	if s.MemPressure == PressureLow && !s.HardwareKnown() {
		t.Error("unknown hardware was reported as low memory pressure (#258)")
	}
	// A sample was recorded, so a second Detect() has history to load.
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".hydra", historyFile)); err != nil {
		if _, werr := os.Stat(filepath.Join(os.Getenv("USERPROFILE"), ".hydra", historyFile)); werr != nil {
			t.Errorf("Detect() recorded no memory sample: %v", err)
		}
	}
}

// system_profiler's output is free-form text; the VRAM line differs between
// Intel Macs (dedicated card, "VRAM (Total): 8 GB") and older ones reporting
// MB. A misparse here reports the wrong budget for local models.
func TestParseSPDisplays(t *testing.T) {
	tests := []struct {
		name     string
		out      string
		wantVRAM float64
		wantName string
	}{{
		name: "dedicated card in GB",
		out: `Graphics/Displays:
    AMD Radeon Pro 5500M:
      Chipset Model: AMD Radeon Pro 5500M
      VRAM (Total): 8 GB
      Vendor: AMD`,
		wantVRAM: 8,
		wantName: "AMD Radeon Pro 5500M",
	}, {
		name: "older card reporting MB is converted to GB",
		out: `      Chipset Model: Intel Iris Plus
      VRAM (Dynamic, Max): 1536 MB`,
		wantVRAM: 1.5,
		wantName: "Intel Iris Plus",
	}, {
		// Apple Silicon has no VRAM line at all — unified memory. The name is
		// still worth reporting; the size must be 0, not a guess.
		name: "apple silicon reports a name but no vram",
		out: `      Chipset Model: Apple M3 Max
      Type: GPU
      Bus: Built-In`,
		wantVRAM: 0,
		wantName: "Apple M3 Max",
	}, {
		name:     "empty output yields nothing rather than a default",
		out:      "",
		wantVRAM: 0,
		wantName: "",
	}, {
		// A VRAM line whose number will not parse must be skipped, not counted
		// as zero and not panicked on.
		name:     "unparsable vram value is skipped",
		out:      "      VRAM (Total): lots GB",
		wantVRAM: 0,
		wantName: "",
	}, {
		name:     "a unit with nothing before it does not index out of range",
		out:      "VRAM GB",
		wantVRAM: 0,
		wantName: "",
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotVRAM, gotName := parseSPDisplays([]byte(tt.out))
			if gotVRAM != tt.wantVRAM {
				t.Errorf("VRAM = %v, want %v", gotVRAM, tt.wantVRAM)
			}
			if gotName != tt.wantName {
				t.Errorf("name = %q, want %q", gotName, tt.wantName)
			}
		})
	}
}

// nvidia-smi reports MiB with --nounits. Treating that number as GB would
// over-report a 24GB card as 24576GB and recommend every model.
func TestParseNvidiaSMI(t *testing.T) {
	tests := []struct {
		name     string
		out      string
		wantVRAM float64
		wantName string
	}{{
		name:     "single card, MB converted to GB",
		out:      "24564, NVIDIA GeForce RTX 4090\n",
		wantVRAM: 24564.0 / 1024,
		wantName: "NVIDIA GeForce RTX 4090",
	}, {
		name:     "multi-GPU takes the first row only",
		out:      "8192, NVIDIA A10\n40960, NVIDIA A100\n",
		wantVRAM: 8,
		wantName: "NVIDIA A10",
	}, {
		name: "no output at all",
		out:  "",
	}, {
		name: "a row with no name is rejected rather than half-read",
		out:  "24564\n",
	}, {
		name: "an unparsable size is rejected",
		out:  "N/A, NVIDIA GeForce RTX 4090\n",
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotVRAM, gotName := parseNvidiaSMI([]byte(tt.out))
			if diff := gotVRAM - tt.wantVRAM; diff > 0.001 || diff < -0.001 {
				t.Errorf("VRAM = %v, want %v", gotVRAM, tt.wantVRAM)
			}
			if gotName != tt.wantName {
				t.Errorf("name = %q, want %q", gotName, tt.wantName)
			}
		})
	}
}

// Every hardware probe shells out. With the tool missing they must report 0 —
// "unreadable" — and never a fabricated default, which is the #258/#261 defect
// class. An empty PATH is the portable way to make every one of them fail.
func TestProbes_MissingToolReportsUnreadableNotADefault(t *testing.T) {
	t.Setenv("PATH", "")

	if got := darwinRAM(); got != 0 {
		t.Errorf("darwinRAM() = %v with no sysctl on PATH, want 0", got)
	}
	if free, wired := darwinMemoryState(); free != 0 || wired != 0 {
		t.Errorf("darwinMemoryState() = (%v, %v) with no vm_stat, want (0, 0)", free, wired)
	}
	if vram, name := darwinGPUVRAM(); vram != 0 || name != "" {
		t.Errorf("darwinGPUVRAM() = (%v, %q) with no system_profiler, want (0, \"\")", vram, name)
	}
	if vram, name := nvidiaVRAM(); vram != 0 || name != "" {
		t.Errorf("nvidiaVRAM() = (%v, %q) with no nvidia-smi, want (0, \"\")", vram, name)
	}

	// windowsMemory is a stub off Windows and the real GlobalMemoryStatusEx call
	// on it. Either way it must not report negative memory, and off Windows it
	// must report nothing rather than a plausible-looking number.
	total, avail := windowsMemory()
	if total < 0 || avail < 0 {
		t.Errorf("windowsMemory() = (%v, %v), negative memory", total, avail)
	}
	if runtime.GOOS != "windows" && (total != 0 || avail != 0) {
		t.Errorf("windowsMemory() = (%v, %v) off Windows, want (0, 0)", total, avail)
	}
}

// Detect must not fabricate hardware when every probe fails — the machine is
// then reported as unknown, which is what stops a 0GB budget being presented as
// a measurement (#258).
func TestDetect_WithNoProbesAvailableReportsUnknown(t *testing.T) {
	sandboxHome(t)
	t.Setenv("PATH", "")
	if runtime.GOOS == "linux" {
		// The Linux path reads /proc rather than shelling out; point it at a
		// path that does not exist so it fails the same way.
		orig := meminfoPath
		meminfoPath = filepath.Join(t.TempDir(), "absent")
		t.Cleanup(func() { meminfoPath = orig })
	}
	if runtime.GOOS == "windows" {
		t.Skip("windows reads memory through the kernel, not a subprocess — " +
			"there is no way to make it fail from here")
	}

	s := Detect()
	if s.HardwareKnown() {
		t.Fatalf("hardware was reported as known with every probe failing: %+v", s)
	}
	if s.MemPressure != PressureUnknown {
		t.Errorf("MemPressure = %v with no reading, want Unknown", s.MemPressure)
	}
	if !strings.Contains(s.Summary(), "unknown") {
		t.Errorf("Summary() = %q, want it to say the hardware is unknown", s.Summary())
	}
	if s.AnyLocalModelFits() {
		t.Error("a model was said to fit on a machine whose memory could not be read")
	}
}
