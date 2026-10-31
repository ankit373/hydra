// SPDX-License-Identifier: MIT

package update

import (
	"sort"
	"testing"
)

// internal/update was at 0%. semverGT decides whether a user is told a new
// release exists, so a wrong answer is silent: nobody sees a notification that
// was never printed.

func TestSemverGT_ReleaseBeatsItsOwnPrerelease(t *testing.T) {
	// The bug this package shipped with: parseVer discarded everything after
	// "-", so a release and its pre-releases compared equal and semverGT
	// returned false. Every user running v1.1.0-rc.9 would never be told that
	// v1.1.0 exists.
	cases := []struct {
		a, b string
		want bool
	}{
		{"v1.1.0", "v1.1.0-rc.9", true},
		{"v1.1.0-rc.9", "v1.1.0", false},
		{"v1.1.0-rc.9", "v1.1.0-rc.8", true},
		{"v1.1.0-rc.8", "v1.1.0-rc.9", false},
		{"v1.1.0-rc.10", "v1.1.0-rc.9", true}, // numeric, not lexical
		{"v1.2.0-rc.1", "v1.1.0", true},       // a newer minor, even as an rc
	}
	for _, tc := range cases {
		if got := semverGT(tc.a, tc.b); got != tc.want {
			t.Errorf("semverGT(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestSemverGT_NumericCore(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v2.0.0", "v1.9.9", true},
		{"v1.2.0", "v1.1.9", true},
		{"v1.1.2", "v1.1.1", true},
		{"v1.1.1", "v1.1.1", false},
		{"v1.1.1", "v1.1.2", false},
		{"1.2.0", "v1.1.0", true},   // the v prefix is optional
		{"v1.10.0", "v1.9.0", true}, // numeric, not lexical
	}
	for _, tc := range cases {
		if got := semverGT(tc.a, tc.b); got != tc.want {
			t.Errorf("semverGT(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// Build metadata is explicitly excluded from precedence by the spec.
func TestSemverGT_BuildMetadataIsIgnored(t *testing.T) {
	for _, pair := range [][2]string{
		{"v1.1.0+build.1", "v1.1.0"},
		{"v1.1.0", "v1.1.0+build.1"},
		{"v1.1.0+a", "v1.1.0+b"},
	} {
		if semverGT(pair[0], pair[1]) {
			t.Errorf("semverGT(%q, %q) = true; build metadata must not affect precedence",
				pair[0], pair[1])
		}
	}
	// …but it must not swallow the pre-release next to it.
	if !semverGT("v1.1.0+build", "v1.1.0-rc.1+build") {
		t.Error("build metadata masked the pre-release comparison")
	}
}

// The SemVer spec's own example ordering, end to end. Sorting with semverGT
// must reproduce it exactly.
func TestSemverGT_SpecExampleOrdering(t *testing.T) {
	want := []string{
		"1.0.0-alpha",
		"1.0.0-alpha.1",
		"1.0.0-alpha.beta",
		"1.0.0-beta",
		"1.0.0-beta.2",
		"1.0.0-beta.11",
		"1.0.0-rc.1",
		"1.0.0",
	}
	got := append([]string(nil), want...)
	// Shuffle deterministically, then sort back with semverGT.
	for i := range got {
		j := (i * 7) % len(got)
		got[i], got[j] = got[j], got[i]
	}
	sort.Slice(got, func(i, j int) bool { return semverGT(got[j], got[i]) })

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ordering wrong at %d:\n got  %v\n want %v", i, got, want)
		}
	}
}

func TestParseVer(t *testing.T) {
	cases := []struct {
		in      string
		wantNum [3]int
		wantPre string
	}{
		{"v1.2.3", [3]int{1, 2, 3}, ""},
		{"1.2.3", [3]int{1, 2, 3}, ""},
		{"v1.2.3-rc.1", [3]int{1, 2, 3}, "rc.1"},
		{"v1.2.3+build", [3]int{1, 2, 3}, ""},
		{"v1.2.3-rc.1+build", [3]int{1, 2, 3}, "rc.1"},
		{"v1.2", [3]int{1, 2, 0}, ""},
		{"v1", [3]int{1, 0, 0}, ""},
		{"", [3]int{0, 0, 0}, ""},
		{"garbage", [3]int{0, 0, 0}, ""},
		{"  v1.2.3  ", [3]int{1, 2, 3}, ""},
	}
	for _, tc := range cases {
		num, pre := parseVer(tc.in)
		if num != tc.wantNum || pre != tc.wantPre {
			t.Errorf("parseVer(%q) = %v/%q, want %v/%q", tc.in, num, pre, tc.wantNum, tc.wantPre)
		}
	}
}

// Nothing unparsable may be reported as newer, that would show an update
// notice pointing at a version that does not exist.
func TestSemverGT_GarbageIsNeverNewer(t *testing.T) {
	for _, junk := range []string{"", "garbage", "vv1", "...", "-rc.1", "v"} {
		if semverGT(junk, "v1.0.0") {
			t.Errorf("semverGT(%q, v1.0.0) = true", junk)
		}
	}
}

// The real scenario, spelled out: this repo's own release history.
func TestSemverGT_HydraReleaseHistory(t *testing.T) {
	// A user on each of these must be offered v1.1.0.
	for _, installed := range []string{
		"v1.0.0", "v1.0.1", "v1.1.0-rc.1", "v1.1.0-rc.8", "v1.1.0-rc.9",
	} {
		if !semverGT("v1.1.0", installed) {
			t.Errorf("a user on %s is never told about v1.1.0", installed)
		}
	}
	// …and a user already on v1.1.0 is not nagged.
	if semverGT("v1.1.0", "v1.1.0") {
		t.Error("v1.1.0 offered as an update to itself")
	}
}

// ── ordering axioms, fuzzed ──────────────────────────────────────────────────

// A comparison used for sorting must be a strict weak ordering, or a sort built
// on it can produce nonsense (or, in Go, panic).
func FuzzSemverGT_IsAStrictOrdering(f *testing.F) {
	for _, s := range []string{
		"v1.0.0", "v1.0.1", "1.0.0-rc.1", "v2.0.0-alpha.beta",
		"", "v", "0.0.0", "v1.1.0-rc.10", "v1.1.0+build",
	} {
		f.Add(s, s)
	}

	f.Fuzz(func(t *testing.T, a, b string) {
		if len(a) > 64 || len(b) > 64 {
			t.Skip()
		}
		// Irreflexive: nothing is greater than itself.
		if semverGT(a, a) {
			t.Fatalf("semverGT(%q, %q) = true, not irreflexive", a, a)
		}
		// Asymmetric: both directions cannot hold.
		if semverGT(a, b) && semverGT(b, a) {
			t.Fatalf("semverGT is true in both directions for %q and %q", a, b)
		}
	})
}

func FuzzSemverGT_IsTransitive(f *testing.F) {
	f.Add("v1.0.0", "v1.0.1", "v1.0.2")
	f.Add("v1.0.0-rc.1", "v1.0.0", "v1.0.1")

	f.Fuzz(func(t *testing.T, a, b, c string) {
		if len(a) > 32 || len(b) > 32 || len(c) > 32 {
			t.Skip()
		}
		if semverGT(a, b) && semverGT(b, c) && !semverGT(a, c) {
			t.Fatalf("not transitive: %q > %q > %q but not %q > %q", a, b, c, a, c)
		}
	})
}

// What an edge build sees. The edge channel names its version from the next
// patch after the last release, not the last release itself, and that choice
// is only visible here: SemVer puts any prerelease *before* its own version,
// so 1.4.2-edge.<sha> sorts below the released 1.4.2 and `hyctl` would tell
// an edge user to "upgrade" to code older than the one they are running.
//
// Before #759 an edge binary reported 0.0.0-SNAPSHOT-none, which was wrong in
// the same direction and for a duller reason. These cases are why edge.yml
// increments (#705, #759).
func TestSemverGT_EdgeBuildIsNotToldToDowngrade(t *testing.T) {
	const released = "v1.4.2"

	// The bug, kept as a case so the ordering that causes it stays documented.
	for _, behind := range []string{"0.0.0-SNAPSHOT-none", "1.4.2-edge.g0f001bb"} {
		if !semverGT(released, behind) {
			t.Errorf("semverGT(%q, %q) = false; the case this guards against no longer holds "+
				"and the comment above is stale", released, behind)
		}
	}

	// The scheme edge actually uses: ahead of the release it is built past.
	const edge = "1.4.3-edge.g0f001bb"
	if semverGT(released, edge) {
		t.Errorf("an edge build reporting %q is told to upgrade to %q, which is older code",
			edge, released)
	}
	// And once the release that carries its code lands, it must be told.
	for _, next := range []string{"v1.4.3", "v1.5.0", "v2.0.0"} {
		if !semverGT(next, edge) {
			t.Errorf("an edge build reporting %q is not told about %q, so it never updates",
				edge, next)
		}
	}
	// Two edge builds order by commit only, which is not meaningful, but the
	// newer must never read as older than the release either.
	if semverGT("1.4.3-edge.gaaaaaaa", "v1.4.3") {
		t.Error("an edge prerelease outranks its own release")
	}
}
