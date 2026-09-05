// SPDX-License-Identifier: MIT

package sysinfo

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A real /proc/meminfo head, verbatim. Parsing is tested against this fixture
// rather than the host's memory so the test is deterministic and runs on every
// OS: the previous implementation shelled out to grep and could only be
// exercised on Linux, with a machine-dependent answer.
const meminfoFixture = `MemTotal:       16007748 kB
MemFree:         1234567 kB
MemAvailable:    8388608 kB
Buffers:          123456 kB
Cached:          4194304 kB
SwapCached:            0 kB
Active:          6291456 kB
HugePages_Total:       0
HugePages_Free:        0
Hugepagesize:       2048 kB
`

func TestParseMeminfo_ReadsTheFieldsWeUse(t *testing.T) {
	got := parseMeminfo(meminfoFixture)

	// 16007748 kB / 2^20 = 15.266… GB
	if v, ok := got["MemTotal"]; !ok {
		t.Error("MemTotal missing")
	} else if v < 15.2 || v > 15.3 {
		t.Errorf("MemTotal = %.4f GB, want ~15.266", v)
	}
	// 8388608 kB is exactly 8 GB.
	if v, ok := got["MemAvailable"]; !ok {
		t.Error("MemAvailable missing")
	} else if v != 8 {
		t.Errorf("MemAvailable = %v GB, want exactly 8", v)
	}
}

// Unitless fields must not be read as memory. HugePages_Total: 0 parses as a
// number but is a count, not kB, treating it as memory would be silent
// nonsense if it were ever added to the fields we read.
func TestParseMeminfo_SkipsUnitlessFields(t *testing.T) {
	got := parseMeminfo(meminfoFixture)
	for _, k := range []string{"HugePages_Total", "HugePages_Free"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s was parsed as a memory field; it has no kB unit", k)
		}
	}
}

// Absent must be distinguishable from zero. The old `grep | Fields` pipeline
// collapsed both to 0, so a kernel that did not report MemAvailable was
// indistinguishable from one reporting none available.
func TestParseMeminfo_MissingFieldIsAbsentNotZero(t *testing.T) {
	got := parseMeminfo("MemTotal:       16007748 kB\n")
	if _, ok := got["MemAvailable"]; ok {
		t.Error("MemAvailable present in a fixture that does not contain it")
	}
	if _, ok := got["MemFree"]; ok {
		t.Error("MemFree present in a fixture that does not contain it")
	}
}

func TestParseMeminfo_GarbageIsIgnoredNotFatal(t *testing.T) {
	for _, in := range []string{
		"",
		"not a meminfo file at all\n",
		"MemTotal:\n",             // no value
		"MemTotal: notanumber kB", // unparsable value
		"MemTotal 16007748 kB\n",  // no colon
	} {
		got := parseMeminfo(in)
		if v, ok := got["MemTotal"]; ok {
			t.Errorf("parseMeminfo(%q) yielded MemTotal = %v, want it skipped", in, v)
		}
	}
}

// The readers must return 0, not a fabricated number, when the file cannot be
// read. linuxRAM used to return a hardcoded 8, which flowed into model-fit
// recommendations as though someone had measured it (#261, same class as #258).
func TestLinuxReaders_UnreadableFileYieldsZeroNotAGuess(t *testing.T) {
	orig := meminfoPath
	t.Cleanup(func() { meminfoPath = orig })
	meminfoPath = filepath.Join(t.TempDir(), "definitely-not-here")

	if got := linuxRAM(); got != 0 {
		t.Errorf("linuxRAM() = %v with no readable meminfo, want 0 so it reports unknown", got)
	}
	if got := linuxFreeRAM(); got != 0 {
		t.Errorf("linuxFreeRAM() = %v with no readable meminfo, want 0", got)
	}

	// And that zero must surface as unknown rather than as an empty machine.
	s := &Specs{OS: "linux", Arch: runtime.GOARCH, TotalRAMGB: linuxRAM()}
	s.MemPressure = s.computePressure()
	if s.HardwareKnown() {
		t.Error("HardwareKnown() = true after an unreadable meminfo")
	}
	if s.MemPressure != PressureUnknown {
		t.Errorf("MemPressure = %v, want unknown", s.MemPressure)
	}
}

func TestLinuxReaders_ReadFromTheConfiguredPath(t *testing.T) {
	orig := meminfoPath
	t.Cleanup(func() { meminfoPath = orig })

	path := filepath.Join(t.TempDir(), "meminfo")
	if err := os.WriteFile(path, []byte(meminfoFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	meminfoPath = path

	if got := linuxRAM(); got < 15.2 || got > 15.3 {
		t.Errorf("linuxRAM() = %.4f, want ~15.266 from the fixture", got)
	}
	if got := linuxFreeRAM(); got != 8 {
		t.Errorf("linuxFreeRAM() = %v, want exactly 8 from the fixture", got)
	}
}
