// SPDX-License-Identifier: MIT

package policy

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// The three caps in policy.yaml that are enforced, and the one place they are
// enforced from. `hyctl parallel` gained them in #424 and `hyctl edit`, the
// single-file command a person actually reaches for, consulted the policy file
// nowhere at all (#769). Two copies of a cap is two chances for a refusal to
// read differently depending on which command hit it.

// ForFile decides the policy governing one file edit.
//
// A policy file that cannot be read leaves the caps at their defaults rather
// than unset: an unreadable policy must not read as "no limits", which is what
// a zero FilePolicy would mean to every caller below.
func ForFile(hydraHome string, spec Spec) FilePolicy {
	eng, err := LoadFilePolicy(hydraHome)
	if err != nil {
		return defaultFilePolicy()
	}
	return eng.Decide(spec)
}

// Deadline bounds a dispatch by max_wall_seconds, or returns ctx unchanged
// when the policy sets no ceiling.
func (fp FilePolicy) Deadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if fp.MaxWallSeconds <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, time.Duration(fp.MaxWallSeconds)*time.Second)
}

// Bounded reports whether ctx ended because this policy's wall-clock ceiling
// fired, which is what separates the policy refusing from the head failing.
//
// Read off the context rather than the dispatch error: killing a CLI head's
// subprocess surfaces as "signal: killed", which carries no deadline for
// errors.Is to find, so an error-only check saw the timeout on HTTP heads and
// never on CLI ones.
func (fp FilePolicy) Bounded(ctx context.Context) bool {
	return fp.MaxWallSeconds > 0 && errors.Is(ctx.Err(), context.DeadlineExceeded)
}

// WallExceeded is what a caller reports when Bounded says the ceiling fired. A
// deadline is the policy refusing rather than the head failing, and the two
// want different answers from whoever reads the result.
func (fp FilePolicy) WallExceeded() string {
	return fmt.Sprintf("max_wall_seconds_exceeded: policy allows %ds", fp.MaxWallSeconds)
}

// DiffExceeded reports whether an edit breaches diff_size_cap_pct, and the
// refusal to report when it does.
//
// origLines <= 0 is a new file, which has no "percent of itself changed" to
// measure, so the cap applies to modifications only.
func (fp FilePolicy) DiffExceeded(added, removed, origLines int) (string, bool) {
	if fp.DiffSizeCapPct <= 0 || origLines <= 0 {
		return "", false
	}
	pct := float64(added+removed) / float64(origLines) * 100
	if pct <= float64(fp.DiffSizeCapPct) {
		return "", false
	}
	return fmt.Sprintf("diff_size_cap_exceeded: changed %.0f%% of file (cap %d%%)",
		pct, fp.DiffSizeCapPct), true
}
