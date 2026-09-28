// SPDX-License-Identifier: MIT

package build

import (
	"strings"
	"testing"
)

// Two binaries ship in one archive and both render from here, so the name has
// to come from the caller: `hyverify --version` reporting "hydra" would send a
// bug report to the wrong tool (#1058).
func TestText_NamesTheBinaryItWasGiven(t *testing.T) {
	for _, name := range []string{"hydra", "hyverify"} {
		got := Text(name)
		if !strings.Contains(got, name+" ") {
			t.Errorf("Text(%q) does not name it: %q", name, got)
		}
	}
	if Text("hydra") == Text("hyverify") {
		t.Error("two binaries render identically, so neither can be identified")
	}
}

// Every stamped field reaches the output. A field set by ldflags and printed
// nowhere is a value nobody can read back off a release archive.
func TestText_CarriesEveryStampedField(t *testing.T) {
	got := Text("hyverify")
	for label, value := range map[string]string{
		"version": Version, "commit": Commit, "date": Date, "built by": BuiltBy,
	} {
		if !strings.Contains(got, value) {
			t.Errorf("the %s (%q) is missing from %q", label, value, got)
		}
	}
}
