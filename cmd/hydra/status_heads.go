// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
)

// reasonFunc reports why a head cannot be dispatched to, or "" when it can.
// health.Reason in production; a stub in tests. Taking it as a parameter is
// what keeps this renderable without touching the machine.
type reasonFunc func(provider.Head) string

// headTiers renders the heads that can run right now, grouped by the tier they
// would actually route at, and the --tier/--enum words that reach each one.
// Anything reason refuses is counted, not listed: status advertised three heads
// that could not serve because it echoed a config snapshot instead of asking
// (#714).
//
// The names come from routing.yaml, the table the router resolves them
// through. A separate panel used to list them against cfg.Tiers' frozen head
// IDs, which is not where any of them route (#782).
func headTiers(heads []provider.Head, reason reasonFunc) string {
	byTier := map[int][]string{}
	var blocked []string
	for _, h := range heads {
		if why := reason(h); why != "" {
			blocked = append(blocked, h.Name)
			continue
		}
		t := rank.UITier(h)
		byTier[t] = append(byTier[t], h.Name)
	}
	names := dispatch.TierNamesByTier()

	var b strings.Builder
	rule := dimStyle.Render("  "+strings.Repeat("─", 62)) + "\n"
	b.WriteString(rule)
	b.WriteString(fmt.Sprintf("  %-6s%-16s%s\n", "Tier", "--tier/--enum", "Heads that can run now"))
	b.WriteString(rule)

	if len(byTier) == 0 {
		b.WriteString("  " + warnStyle.Render("no routable heads") + "\n")
	}
	tiers := make([]int, 0, len(byTier))
	for t := range byTier {
		tiers = append(tiers, t)
	}
	sort.Ints(tiers)
	for _, t := range tiers {
		b.WriteString(fmt.Sprintf("  %-6s%-16s%s\n",
			strconv.Itoa(t), strings.Join(names[t], ", "), strings.Join(byTier[t], ", ")))
	}
	if idle := idleTierNames(names, byTier); idle != "" {
		b.WriteString("\n  " + dimStyle.Render("resolves but nothing can serve it: "+idle) + "\n")
	}

	if len(blocked) > 0 {
		b.WriteString("\n  " + dimStyle.Render(fmt.Sprintf(
			"%d of %d discovered heads cannot run (%s); `hyctl probe` says why.",
			len(blocked), len(heads), strings.Join(blocked, ", "))) + "\n")
	}
	return b.String()
}

// idleTierNames lists the names that resolve to a tier no live head sits at.
// They still route, by degrading to the cheapest head available, so leaving
// them out of the table entirely would hide a word the user can legitimately
// type; saying so is the honest middle (#782).
func idleTierNames(names map[int][]string, byTier map[int][]string) string {
	tiers := make([]int, 0, len(names))
	for t := range names {
		if len(byTier[t]) == 0 {
			tiers = append(tiers, t)
		}
	}
	if len(tiers) == 0 {
		return ""
	}
	sort.Ints(tiers)

	var out []string
	for _, t := range tiers {
		out = append(out, fmt.Sprintf("%s (%d)", strings.Join(names[t], "/"), t))
	}
	return strings.Join(out, ", ")
}
