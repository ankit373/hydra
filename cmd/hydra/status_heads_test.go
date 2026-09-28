// SPDX-License-Identifier: MIT

package main

import (
	"strconv"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
)

// The machine that produced #714: two heads discovery finds but nothing can
// drive, and a config tier naming a third that discovery never emits at all.
func liveHeads() []provider.Head {
	return []provider.Head{
		{ID: "claude", Name: "Claude Code", Provider: "anthropic", Source: "cli", CapScore: 95},
		{ID: "pro-high", Name: "Gemini 3.1 Pro (High)", Provider: "antigravity", Source: "registry",
			Executable: "/usr/bin/agy", CapScore: 80, Meta: map[string]string{"tier": "5"}},
		{ID: "ollama/qwen3:0.6b", Name: "qwen3:0.6b (Ollama)", Provider: "local", Source: "port",
			CapScore: 63, LocalOnly: true},
		{ID: "ollama", Name: "Ollama", Provider: "local", Source: "cli", CapScore: 60, LocalOnly: true},
		{ID: "ollama/nomic-embed-text:latest", Name: "nomic-embed-text:latest (Ollama)",
			Provider: "local", Source: "port", CapScore: 55, LocalOnly: true,
			Meta: map[string]string{"embedding_only": "true"}},
	}
}

// refuses stands in for health.Reason over the fixture above.
func refuses(h provider.Head) string {
	switch h.ID {
	case "ollama":
		return "binary only, start its local server"
	case "ollama/nomic-embed-text:latest":
		return "embeddings only, never routed"
	}
	return ""
}

// A rendered row is two spaces, a %-6s tier, a %-16s name list, then the heads,
// so the head column starts at a fixed offset. Splitting on a run of spaces
// instead would read the --tier names as head names.
const headColumn = 2 + 6 + 16

// listedHeads returns the head names headTiers presented as available. Exact
// names, not a substring scan: "Ollama" is a substring of "qwen3:0.6b (Ollama)".
func listedHeads(out string) map[string]bool {
	names := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if len(line) <= headColumn || !strings.HasPrefix(line, "  ") {
			continue
		}
		if _, err := strconv.Atoi(strings.TrimSpace(line[2:8])); err != nil {
			continue // not a tier row
		}
		for _, n := range strings.Split(line[headColumn:], ", ") {
			if n = strings.TrimSpace(n); n != "" {
				names[n] = true
			}
		}
	}
	return names
}

// The defect itself: status listed three heads that could not serve because it
// echoed cfg.Tiers instead of asking. Nothing reason refuses may appear as an
// available head, on any surface, ever again.
func TestHeadTiers_NeverListsAHeadThatCannotRun(t *testing.T) {
	listed := listedHeads(stripANSICodes(headTiers(liveHeads(), refuses)))
	if len(listed) == 0 {
		t.Fatal("parsed no rows at all; this test has stopped testing anything")
	}
	for _, h := range liveHeads() {
		if refuses(h) != "" && listed[h.Name] {
			t.Errorf("%q is listed as available but cannot run: %s", h.Name, refuses(h))
		}
		if refuses(h) == "" && !listed[h.Name] {
			t.Errorf("routable head %q is missing from the table", h.Name)
		}
	}
}

// A head that cannot run is still accounted for. Dropping it silently would
// make the list shorter and no more honest.
func TestHeadTiers_CountsWhatItWouldNotList(t *testing.T) {
	out := stripANSICodes(headTiers(liveHeads(), refuses))
	if !strings.Contains(out, "2 of 5 discovered heads cannot run") {
		t.Errorf("no count of the unroutable heads:\n%s", out)
	}
	if !strings.Contains(out, "hyctl probe") {
		t.Error("does not point at the command that gives the reason")
	}
}

// Grouping is rank.UITier, the same number dispatch selects on, so the table
// says where a head would actually route.
func TestHeadTiers_GroupsByTheTierDispatchWouldUse(t *testing.T) {
	out := stripANSICodes(headTiers(liveHeads(), refuses))
	for _, want := range []string{
		"1     core            Claude Code",
		"5     complex         Gemini 3.1 Pro (High)",
		"10    grunt, local    qwen3:0.6b (Ollama)", // LocalOnly ⇒ tier 10 regardless of score
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing row %q in:\n%s", want, out)
		}
	}
}

func TestHeadTiers_SaysSoWhenNothingCanRun(t *testing.T) {
	out := stripANSICodes(headTiers(liveHeads(), func(provider.Head) string { return "nope" }))
	if !strings.Contains(out, "no routable heads") {
		t.Errorf("an empty table must say why it is empty:\n%s", out)
	}
}

// The --tier names sit on the row of the tier they resolve to, so the word a
// user types and the head that answers it are read off one line. A separate
// panel used to list them against cfg.Tiers' frozen head IDs, which is not
// where any of them route (#782).
func TestHeadTiers_NamesEachTierWithTheWordsThatReachIt(t *testing.T) {
	out := stripANSICodes(headTiers(liveHeads(), refuses))
	for _, want := range []string{
		"core",  // tier 1
		"grunt", // tier 10
		"local", // tier 10's legacy alias
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the table does not name --tier %s:\n%s", want, out)
		}
	}
	// And a name never lands on a tier it does not resolve to.
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, " core ") {
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(line), "1 ") {
			t.Errorf("--tier core is shown against the wrong tier: %q", line)
		}
	}
}

// A name whose tier no live head sits at still routes, by degrading to the
// cheapest head available, so it must be reported rather than omitted. The
// fixture has nothing at tiers 2-4 or 6-9.
func TestIdleTierNames_ReportsNamesWithNothingToServeThem(t *testing.T) {
	out := stripANSICodes(headTiers(liveHeads(), refuses))
	if !strings.Contains(out, "resolves but nothing can serve it") {
		t.Errorf("names with no live head at their tier are not reported:\n%s", out)
	}
	if !strings.Contains(out, "expert (2)") {
		t.Errorf("want expert reported as idle at tier 2:\n%s", out)
	}
	// A tier that does have a head must not be called idle.
	idle := out[strings.Index(out, "resolves but nothing can serve it"):]
	for _, served := range []string{"core (1)", "grunt/local (10)"} {
		if strings.Contains(idle, served) {
			t.Errorf("%s has a live head but is reported idle:\n%s", served, idle)
		}
	}
}

// When every name has a head at its tier there is nothing to warn about, and
// an empty warning line reads as a warning.
func TestIdleTierNames_EmptyWhenEveryTierIsServed(t *testing.T) {
	names := map[int][]string{1: {"core"}, 10: {"grunt", "local"}}
	byTier := map[int][]string{1: {"Claude Code"}, 10: {"qwen3:0.6b (Ollama)"}}
	if got := idleTierNames(names, byTier); got != "" {
		t.Errorf("idleTierNames = %q, want empty when every tier has a head", got)
	}
}
