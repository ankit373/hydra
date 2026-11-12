// SPDX-License-Identifier: MIT

package trust

import (
	"errors"
	"strings"
	"testing"
	"testing/quick"
)

// The rule lived privately in three callers, and the one that missed it printed
// `--domain ` with no value in the message telling the user how to fix things.
func TestDomain_EmptyBecomesTheDefault(t *testing.T) {
	for in, want := range map[string]string{
		"":       DefaultDomain,
		"   ":    DefaultDomain,
		"\t\n":   DefaultDomain,
		"go":     "go",
		"  go  ": "go",
	} {
		if got := Domain(in); got != want {
			t.Errorf("Domain(%q) = %q, want %q", in, got, want)
		}
	}
}

// A domain is a map key, so " go" and "go" would be two calibration histories
// that render identically in every report.
func TestDomain_TrimsSoOneDomainIsOneHistory(t *testing.T) {
	if Domain(" go") != Domain("go ") || Domain("go") != Domain(" go ") {
		t.Error("whitespace produces distinct calibration domains that look the same")
	}
}

func TestTaskClass_Grammar(t *testing.T) {
	for in, want := range map[string]string{
		"classification.banking77":      "classification.banking77",
		"Classification.Banking77 ":     "classification.banking77",
		"a.b.c":                         "a.b.c",
		"extract_v2.cord-receipts":      "extract_v2.cord-receipts",
		"x." + strings.Repeat("y", 126): "x." + strings.Repeat("y", 126),
	} {
		got, err := TaskClass(in)
		if err != nil || got != want {
			t.Errorf("TaskClass(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "go", "default", "classify/banking77", ".x", "x.", "a..b", "a.b c",
		"_a.b", "a.-b", "x." + strings.Repeat("y", 127),
	} {
		if got, err := TaskClass(in); !errors.Is(err, ErrTaskClass) {
			t.Errorf("TaskClass(%q) = %q, %v; want ErrTaskClass", in, got, err)
		}
	}
}

// G6. A served class sharing a key with a coding cell would mix two kinds of
// work in one calibration history, #888's split in reverse.
func TestTaskClass_CannotCollideWithAFileDomain(t *testing.T) {
	refused := func(path string) bool {
		d := DomainForFile(path)
		if _, err := TaskClass(d); err == nil {
			t.Errorf("TaskClass accepted %q, which DomainForFile also produces", d)
			return false
		}
		return true
	}
	for _, p := range []string{"x.go", "x.GO", "Makefile", "a.tar.gz", "dir.v2/file.py", ".env", "x."} {
		refused(p)
	}
	if err := quick.Check(refused, &quick.Config{MaxCount: 2000}); err != nil {
		t.Error(err)
	}
}
