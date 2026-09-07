// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
)

// reasonFunc reports why a head cannot be dispatched to, or "" when it can.
// health.Reason in production; a stub in tests. Taking it as a parameter is
// what keeps this renderable without touching the machine.
type reasonFunc func(provider.Head) string

// headTiers renders the heads that can run right now, grouped by the tier they
// would actually route at. Anything reason refuses is counted, not listed:
// status advertised three heads that could not serve because it echoed a config
// snapshot instead of asking (#714).
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

	var b strings.Builder
	b.WriteString(dimStyle.Render("  "+strings.Repeat("─", 48)) + "\n")
	b.WriteString(fmt.Sprintf("  %-6s%s\n", "Tier", "Heads that can run now"))
	b.WriteString(dimStyle.Render("  "+strings.Repeat("─", 48)) + "\n")

	if len(byTier) == 0 {
		b.WriteString("  " + warnStyle.Render("no routable heads") + "\n")
	}
	tiers := make([]int, 0, len(byTier))
	for t := range byTier {
		tiers = append(tiers, t)
	}
	sort.Ints(tiers)
	for _, t := range tiers {
		b.WriteString(fmt.Sprintf("  %-6s%s\n", strconv.Itoa(t), strings.Join(byTier[t], ", ")))
	}

	if len(blocked) > 0 {
		b.WriteString("\n  " + dimStyle.Render(fmt.Sprintf(
			"%d of %d discovered heads cannot run (%s); `hyctl probe` says why.",
			len(blocked), len(heads), strings.Join(blocked, ", "))) + "\n")
	}
	return b.String()
}

// tierAliases renders cfg.Tiers as what it actually is: the names `--tier <name>`
// accepts. It is written once by `hyctl init` and never refreshed, so entries
// are resolved against discovery and dead ones are marked rather than listed as
// available (#714).
func tierAliases(tiers []config.Tier, heads []provider.Head, reason reasonFunc) string {
	if len(tiers) == 0 {
		return ""
	}
	live := map[string]provider.Head{}
	for _, h := range heads {
		live[h.ID] = h
	}

	var b strings.Builder
	b.WriteString(dimStyle.Render("  "+strings.Repeat("─", 48)) + "\n")
	b.WriteString(fmt.Sprintf("  %-14s  %s\n", "--tier <name>", "Heads it can reach"))
	b.WriteString(dimStyle.Render("  "+strings.Repeat("─", 48)) + "\n")

	for _, t := range tiers {
		var ok, dead []string
		for _, id := range t.Heads {
			h, found := live[id]
			switch {
			case !found:
				// Discovery never emitted it. Could be uninstalled, could be
				// `enabled: false` in the registry; status cannot tell, so it
				// says what it knows rather than guessing which.
				dead = append(dead, id+": not discovered")
			case reason(h) != "":
				dead = append(dead, id+": "+reason(h))
			default:
				ok = append(ok, h.Name)
			}
		}
		lineup := strings.Join(ok, ", ")
		if lineup == "" {
			lineup = warnStyle.Render("none can run")
		}
		b.WriteString(fmt.Sprintf("  %-14s  %s\n", t.Name, lineup))
		for _, d := range dead {
			b.WriteString(fmt.Sprintf("  %-14s  %s\n", "", dimStyle.Render("✗ "+d)))
		}
	}
	return b.String()
}
