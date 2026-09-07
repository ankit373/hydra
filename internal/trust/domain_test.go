// SPDX-License-Identifier: MIT

package trust

import "testing"

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
