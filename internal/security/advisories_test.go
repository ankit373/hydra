// SPDX-License-Identifier: MIT

package security

import (
	"strings"
	"testing"
)

func TestAdvisoryCheck_NoServersIsNotEvaluated(t *testing.T) {
	if got := advisoryCheck(nil).Status; got != "not evaluated" {
		t.Errorf("Status = %q, want not evaluated", got)
	}
}

// The default. Nothing was asked, and that must not read as nothing was found.
func TestAdvisoryCheck_NotRequestedIsNotClean(t *testing.T) {
	c := advisoryCheck([]LocalServer{{Kind: "ollama", Version: "0.33.2"}})
	if c.Status != "not checked" {
		t.Errorf("Status = %q, want not checked", c.Status)
	}
	if strings.Contains(c.Status, "no known") {
		t.Errorf("Status = %q reads as a clean result for a lookup that never ran", c.Status)
	}
	if !strings.Contains(c.Detail, "--advisories") {
		t.Errorf("Detail %q does not say how to run it", c.Detail)
	}
}

// The actionable headline: a fix exists and this server does not have it.
func TestAdvisoryCheck_AFixableAdvisoryNamesTheVersionToUpgradeTo(t *testing.T) {
	c := advisoryCheck([]LocalServer{{
		Kind: "ollama", Version: "0.16.0", AdvisoryState: AdvisoriesChecked,
		Advisories: []Advisory{{ID: "GHSA-x8qc-fggm-mpqg", CVE: "CVE-2026-7482", FixedIn: "0.17.1"}},
	}})
	if c.Status != "1 fixable" {
		t.Fatalf("Status = %q, want 1 fixable", c.Status)
	}
	for _, want := range []string{"CVE-2026-7482", "0.17.1", "0.16.0"} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("Detail does not mention %q: %s", want, c.Detail)
		}
	}
}

// A current Ollama carries nine advisories with no fix upstream. Counting those
// together with fixable ones would read the same on a patched server as on an
// abandoned one, so they are separated and the unfixed ones say what to do.
func TestAdvisoryCheck_UnfixedAdvisoriesDoNotAskForAnUpgrade(t *testing.T) {
	c := advisoryCheck([]LocalServer{{
		Kind: "ollama", Version: "0.33.2", AdvisoryState: AdvisoriesChecked,
		Advisories: []Advisory{{ID: "GO-2025-3557"}, {ID: "GO-2025-3558"}},
	}})
	if c.Status != "2 unfixed upstream" {
		t.Fatalf("Status = %q, want 2 unfixed upstream", c.Status)
	}
	// The wording is "no upgrade answers these", so match the fixable path's own
	// instruction rather than a substring both branches share.
	if strings.Contains(c.Detail, "fixed in") {
		t.Errorf("Detail names a fix version for advisories that have none: %s", c.Detail)
	}
	if !strings.Contains(c.Detail, "no upgrade") {
		t.Errorf("Detail does not say upgrading cannot answer these: %s", c.Detail)
	}
	if !strings.Contains(c.Detail, "loopback") {
		t.Errorf("Detail does not point at the mitigation that does exist: %s", c.Detail)
	}
}

// Both kinds present: the fixable ones lead, because they are the ones an
// action closes, and the rest are still counted rather than dropped.
func TestAdvisoryCheck_FixableLeadsAndUnfixedIsStillCounted(t *testing.T) {
	c := advisoryCheck([]LocalServer{{
		Kind: "ollama", Version: "0.16.0", AdvisoryState: AdvisoriesChecked,
		Advisories: []Advisory{
			{CVE: "CVE-2026-7482", FixedIn: "0.17.1"},
			{ID: "GO-2025-3557"},
			{ID: "GO-2025-3558"},
		},
	}})
	if c.Status != "1 fixable" {
		t.Fatalf("Status = %q, want 1 fixable", c.Status)
	}
	if !strings.Contains(c.Detail, "2 have no fix") {
		t.Errorf("Detail loses the unfixed ones: %s", c.Detail)
	}
}

func TestAdvisoryCheck_CleanIsOnlyClaimedAfterAsking(t *testing.T) {
	c := advisoryCheck([]LocalServer{{Kind: "ollama", Version: "9.9.9", AdvisoryState: AdvisoriesChecked}})
	if c.Status != "no known advisories" {
		t.Errorf("Status = %q, want no known advisories", c.Status)
	}
}

// Each way a lookup can fail to happen has to be visible, or a partial answer
// reads as a whole one.
func TestAdvisoryCheck_EveryUnaskedReasonIsNamed(t *testing.T) {
	cases := map[AdvisoryState]string{
		AdvisoriesUnknownVersion: "publishes no version",
		AdvisoriesNotQueryable:   "no package OSV tracks",
		AdvisoriesFailed:         "did not complete",
	}
	for state, want := range cases {
		c := advisoryCheck([]LocalServer{{Kind: "lmstudio", AdvisoryState: state}})
		if !strings.Contains(c.Detail, want) {
			t.Errorf("state %q: detail %q does not contain %q", state, c.Detail, want)
		}
	}
}

// A failed lookup beside a clean one must not let the clean one speak for both.
func TestAdvisoryCheck_AFailedLookupIsReportedBesideACleanOne(t *testing.T) {
	c := advisoryCheck([]LocalServer{
		{Kind: "ollama", Version: "9.9.9", AdvisoryState: AdvisoriesChecked},
		{Kind: "litellm", AdvisoryState: AdvisoriesFailed},
	})
	if !strings.Contains(c.Detail, "did not complete") {
		t.Errorf("Detail %q hides the failed lookup behind the clean one", c.Detail)
	}
	if !strings.Contains(c.Detail, "not clean") {
		t.Errorf("Detail %q does not say a failed lookup is unchecked rather than clean", c.Detail)
	}
}

func TestCapped_KeepsTheCountWhenItTruncates(t *testing.T) {
	got := capped([]string{"a", "b", "c", "d", "e"}, 3)
	if len(got) != 4 || got[3] != "and 2 more" {
		t.Errorf("capped = %v, want the first 3 plus a count of the rest", got)
	}
	if same := capped([]string{"a"}, 3); len(same) != 1 {
		t.Errorf("capped truncated a short list: %v", same)
	}
}

func TestAdvisoryName_PrefersTheCVE(t *testing.T) {
	if got := advisoryName(Advisory{ID: "GHSA-x", CVE: "CVE-1"}); got != "CVE-1" {
		t.Errorf("advisoryName = %q, want the CVE", got)
	}
	if got := advisoryName(Advisory{ID: "GO-2025-3557"}); got != "GO-2025-3557" {
		t.Errorf("advisoryName = %q, want the OSV id when there is no CVE", got)
	}
}
