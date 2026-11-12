// SPDX-License-Identifier: MIT

package main

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// GOVERNANCE.md: nothing releases without the maintainer's approval. The
// release train is a scheduled job holding a write token, so it must stay a
// job that only labels and comments.
func TestReleaseTrain_NeverApprovesOrMerges(t *testing.T) {
	body := workflowFile(t, "release-train.yml")
	for _, bad := range []string{"gh pr merge", "gh pr review", "--approve", "@mergifyio", "/merge"} {
		if strings.Contains(body, bad) {
			t.Errorf("release-train.yml contains %q; it may only label and comment", bad)
		}
	}
	if !regexp.MustCompile(`(?m)^\s+- cron: "[^"]+ \d"$`).MatchString(body) {
		t.Error("release-train.yml has no weekly cron (one day-of-week field)")
	}
}

// The limits are in the policy; a drifted number changes when releases are due.
func TestReleaseTrain_OverdueLimitsMatchThePolicy(t *testing.T) {
	body := workflowFile(t, "release-train.yml")
	for _, want := range []string{`OVERDUE_DAYS: "14"`, `OVERDUE_CHANGES: "20"`} {
		if !strings.Contains(body, want) {
			t.Errorf("release-train.yml does not set %s", want)
		}
	}
}

// A soak rule nobody enforces is a memory, not a gate.
func TestReleaseGates_AreRequiredForTheReleasePR(t *testing.T) {
	for _, r := range mergifyConfig(t)["queue_rules"].([]any) {
		rule := r.(map[string]any)
		if rule["name"] != "release-please" {
			continue
		}
		if !slices.Contains(conditionStrings(rule["queue_conditions"]), "check-success=Release gates") {
			t.Error("the release-please queue does not require Release gates")
		}
		return
	}
	t.Fatal("no release-please queue in .mergify.yml; this test no longer checks what it thinks")
}

// The gate and the train must size a release the same way, so both call the
// one script rather than each counting commits.
func TestReleaseSize_IsTheOneDefinition(t *testing.T) {
	for _, f := range []string{"release-train.yml", "ci.yml"} {
		if !strings.Contains(workflowFile(t, f), "./scripts/release-size.sh") {
			t.Errorf("%s does not size the release with scripts/release-size.sh", f)
		}
	}
}
