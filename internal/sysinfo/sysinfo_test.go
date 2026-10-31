// SPDX-License-Identifier: MIT

package sysinfo

import (
	"runtime"
	"strings"
	"testing"
)

// The contract this package exists to keep: Detect either reads this machine's
// memory or says it could not. It must never produce a number it did not
// measure.
//
// Before #258 there was no windows case in Detect's switch, so on Windows every
// field kept its zero value and the package reported "0GB RAM · memory fully
// occupied (0.0GB free)" with MemPressure = low, simultaneously claiming the
// machine was full and that it had plenty of headroom, from no reading at all.
// This test runs on all three OSes in CI and would have caught that on day one.
func TestDetect_ReadsThisMachinesMemory(t *testing.T) {
	s := Detect()

	if !s.HardwareKnown() {
		t.Fatalf("Detect() on %s/%s reported unknown hardware, this platform has no working "+
			"detection path, and every memory-derived number it produces is a placeholder.\n"+
			"  Summary()   = %q\n  MemPressure = %v",
			runtime.GOOS, runtime.GOARCH, s.Summary(), s.MemPressure)
	}
	if s.TotalRAMGB < 0.5 {
		t.Errorf("TotalRAMGB = %.2f, no machine that can run this test has under 0.5GB", s.TotalRAMGB)
	}
	if s.FreeRAMGB > s.TotalRAMGB {
		t.Errorf("FreeRAMGB (%.2f) exceeds TotalRAMGB (%.2f)", s.FreeRAMGB, s.TotalRAMGB)
	}
	if s.MemPressure == PressureUnknown {
		t.Errorf("MemPressure = unknown despite TotalRAMGB = %.2f", s.TotalRAMGB)
	}
	if s.OS != runtime.GOOS || s.Arch != runtime.GOARCH {
		t.Errorf("OS/Arch = %s/%s, want %s/%s", s.OS, s.Arch, runtime.GOOS, runtime.GOARCH)
	}
}

// The zero value is exactly what a platform with no detection path produces.
// Every accessor must present it as absence, not as a reading of an empty box.
func TestUndetectedHardware_IsReportedAsUnknown(t *testing.T) {
	s := &Specs{Arch: "amd64", OS: "some-future-os"}
	s.MemPressure = s.computePressure()

	if s.HardwareKnown() {
		t.Fatal("HardwareKnown() = true with TotalRAMGB = 0")
	}
	if s.MemPressure != PressureUnknown {
		t.Errorf("MemPressure = %v, want unknown. PressureLow is the most permissive verdict "+
			"the type can express and it would be drawn from no data", s.MemPressure)
	}

	summary := s.Summary()
	if strings.Contains(summary, "0GB") || strings.Contains(summary, "0.0GB") {
		t.Errorf("Summary() = %q, prints a zero as though it were a measurement", summary)
	}
	if !strings.Contains(strings.ToLower(summary), "unknown") {
		t.Errorf("Summary() = %q, want it to say the hardware is unknown", summary)
	}
	if strings.Contains(strings.ToLower(summary), "fully occupied") {
		t.Errorf("Summary() = %q, claims the machine is full, from no reading", summary)
	}

	if note := s.MemoryNote(); !strings.Contains(strings.ToLower(note), "unknown") &&
		!strings.Contains(strings.ToLower(note), "could not") {
		t.Errorf("MemoryNote() = %q, want it to state that detection failed", note)
	}
	if w := s.PressureWarning(); w == "" {
		t.Error("PressureWarning() is empty for unknown hardware, the user is told nothing")
	}
}

// Ranking models against a 0GB budget answers a hardware question with the
// absence of a hardware reading: every model "does not fit", which reads as
// "your machine is too small" rather than "I could not measure your machine".
func TestUndetectedHardware_DoesNotRankModels(t *testing.T) {
	s := &Specs{Arch: "amd64", OS: "some-future-os"}
	s.MemPressure = s.computePressure()

	recs := s.OllamaRecommendations()
	if len(recs) == 0 {
		t.Fatal("no recommendations returned at all")
	}
	for _, r := range recs {
		if r.Fits {
			t.Errorf("%s reported as fitting with no memory reading", r.Model)
		}
		if strings.Contains(strings.ToLower(r.Reason), "only 0.0gb") {
			t.Errorf("%s reason = %q, states a measured 0GB budget", r.Model, r.Reason)
		}
		if !strings.Contains(strings.ToLower(r.Reason), "could not") &&
			!strings.Contains(strings.ToLower(r.Reason), "unknown") {
			t.Errorf("%s reason = %q, want it to say memory is unknown", r.Model, r.Reason)
		}
	}

	if best := s.BestOllamaModel(); best.Fits {
		t.Error("BestOllamaModel() claims a fit with no memory reading")
	} else if strings.Contains(best.Reason, "insufficient memory") {
		t.Errorf("BestOllamaModel().Reason = %q, a hardware verdict from no hardware reading", best.Reason)
	}
	if s.AnyLocalModelFits() {
		t.Error("AnyLocalModelFits() = true with no memory reading")
	}
}

// A detected machine must still rank normally, the guard above must not have
// turned recommendations off for everyone.
func TestKnownHardware_StillRanksModels(t *testing.T) {
	s := &Specs{Arch: "amd64", OS: "linux", TotalRAMGB: 64, FreeRAMGB: 48}
	s.MemPressure = s.computePressure()

	if !s.HardwareKnown() {
		t.Fatal("HardwareKnown() = false for a machine with 64GB")
	}
	if s.MemPressure == PressureUnknown {
		t.Error("MemPressure = unknown for a machine with 64GB")
	}
	if !s.AnyLocalModelFits() {
		t.Error("nothing fits in 48GB free, the unknown-hardware guard is firing on known hardware")
	}
	if best := s.BestOllamaModel(); !best.Fits {
		t.Errorf("BestOllamaModel() = %+v, want a fit", best)
	}
	if strings.Contains(strings.ToLower(s.Summary()), "unknown") {
		t.Errorf("Summary() = %q for known hardware", s.Summary())
	}
}

func TestPressure_StringCoversEveryValue(t *testing.T) {
	want := map[Pressure]string{
		PressureLow:      "low",
		PressureModerate: "moderate",
		PressureHigh:     "high",
		PressureUnknown:  "unknown",
	}
	for p, s := range want {
		if got := p.String(); got != s {
			t.Errorf("Pressure(%d).String() = %q, want %q", int(p), got, s)
		}
	}
	// Out of range must not panic or index past the table.
	if got := Pressure(99).String(); got != "unknown" {
		t.Errorf("Pressure(99).String() = %q, want %q", got, "unknown")
	}
}

// PressureUnknown is appended, not inserted: the three real levels keep their
// numbering. Nothing persists a Pressure today, but a reordering would silently
// reinterpret any that ever is.
func TestPressure_ValuesAreStable(t *testing.T) {
	for _, tc := range []struct {
		p    Pressure
		want int
	}{{PressureLow, 0}, {PressureModerate, 1}, {PressureHigh, 2}, {PressureUnknown, 3}} {
		if int(tc.p) != tc.want {
			t.Errorf("%v = %d, want %d, inserting a value renumbers the others", tc.p, int(tc.p), tc.want)
		}
	}
}
