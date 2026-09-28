// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"io"
	"sort"

	"github.com/ankit373/hydra/internal/signals"
)

// printRuleDecision shows which rule fired and the signals it read.
//
// Nothing at all when no rule fired, which is what makes a machine with no
// rules file print byte-identically to one built before signals existed. That
// is a constraint with a test behind it, not a preference. `hyctl security`
// is where a loaded-but-unmatched rule set is visible.
//
// Only the true signals: the full set is every PII detector on every dry run,
// which buries the line that matters.
func printRuleDecision(w io.Writer, d signals.Decision, verbose bool) {
	if d.Rule == "" {
		return
	}
	fmt.Fprintf(w, "  %s %q → %s\n", dimStyle.Render("rules:"), d.Rule, describeAction(d.Action))
	if !verbose {
		return
	}

	var on []string
	for name, v := range d.Signals {
		switch t := v.(type) {
		case bool:
			if t {
				on = append(on, name)
			}
		case float64:
			on = append(on, fmt.Sprintf("%s=%g", name, t))
		}
	}
	sort.Strings(on)
	if len(on) > 0 {
		fmt.Fprintf(w, "  %s\n", dimStyle.Render(fmt.Sprintf("        signals: %v", on)))
	}
}

func describeAction(a signals.Action) string {
	switch a.Type {
	case signals.ActionRoute:
		out := "route"
		if a.LocalOnly {
			out += " local-only"
		}
		if a.Tier != "" {
			out += " tier=" + a.Tier
		}
		if a.Enum != "" {
			out += " enum=" + a.Enum
		}
		return out
	case signals.ActionRequireConfidence:
		return fmt.Sprintf("require confidence ≥ %.1f%%", a.Value*100)
	case signals.ActionBlock:
		return "block: " + a.Reason
	}
	return "fall through"
}
