// SPDX-License-Identifier: MIT

package policy

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// The deadline's wiring, deterministically. An end-to-end timeout needs a head
// slow enough to miss it, which means `sleep`, which is Unix-only and racy;
// this asserts the policy's number reaches a real deadline, which is the part
// that was missing entirely (#424), and now for `hyctl edit` too (#769).
func TestDeadline_AppliesMaxWallSeconds(t *testing.T) {
	t.Run("a declared limit becomes a deadline", func(t *testing.T) {
		ctx, cancel := FilePolicy{MaxWallSeconds: 30}.Deadline(context.Background())
		defer cancel()
		dl, ok := ctx.Deadline()
		if !ok {
			t.Fatal("no deadline was set, so max_wall_seconds bounds nothing")
		}
		if d := time.Until(dl); d > 30*time.Second || d < 29*time.Second {
			t.Errorf("deadline is %v out, want about 30s", d)
		}
	})

	// An absent cap must not become a zero one: a context that has already
	// expired would refuse every edit on a policy that declared no limit.
	for _, v := range []int{0, -1} {
		t.Run(fmt.Sprintf("no limit at %d", v), func(t *testing.T) {
			ctx, cancel := FilePolicy{MaxWallSeconds: v}.Deadline(context.Background())
			defer cancel()
			if _, ok := ctx.Deadline(); ok {
				t.Error("a deadline was set with no limit declared")
			}
			if err := ctx.Err(); err != nil {
				t.Errorf("ctx is already done: %v", err)
			}
		})
	}
}

func TestDiffExceeded_MeasuresTheChangeAgainstTheCap(t *testing.T) {
	cap30 := FilePolicy{DiffSizeCapPct: 30}

	for _, c := range []struct {
		name                   string
		fp                     FilePolicy
		added, removed, origLn int
		want                   bool
	}{
		{"under the cap", cap30, 10, 5, 100, false},
		{"exactly at the cap is allowed", cap30, 30, 0, 100, false},
		{"over the cap", cap30, 40, 0, 100, true},
		{"a rewrite of a large file", cap30, 600, 600, 1000, true},

		// A new file has no percent of itself changed to measure, so the cap
		// cannot apply: every creation is a 100% change by construction.
		{"a new file", cap30, 50, 0, 0, false},

		// 0 is "unlimited" in policy.yaml, not "refuse everything".
		{"no cap declared", FilePolicy{DiffSizeCapPct: 0}, 9999, 9999, 10, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			why, over := c.fp.DiffExceeded(c.added, c.removed, c.origLn)
			if over != c.want {
				t.Fatalf("DiffExceeded(%d, %d, %d) = %v, want %v",
					c.added, c.removed, c.origLn, over, c.want)
			}
			if over && why == "" {
				t.Error("a refusal with no reason: the caller prints this verbatim")
			}
			if !over && why != "" {
				t.Errorf("a reason %q was given for an edit that is allowed", why)
			}
		})
	}
}

// An unreadable policy must leave the caps at their defaults rather than
// unset: a zero FilePolicy means "no limits" to every caller, which is the
// opposite of what an operator with an unparseable policy file wants.
func TestForFile_AnUnreadablePolicyStillCaps(t *testing.T) {
	fp := ForFile(t.TempDir(), Spec{File: "/x/y.go", FileLines: 100})
	if fp.DiffSizeCapPct <= 0 {
		t.Error("no diff-size cap after a failed policy load; an unreadable " +
			"policy reads as no policy")
	}
	if fp.MaxWallSeconds <= 0 {
		t.Error("no wall-clock cap after a failed policy load")
	}
}
