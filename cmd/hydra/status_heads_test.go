// SPDX-License-Identifier: MIT

package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/config"
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

// tierRow matches a rendered tier row. The %-6s tier column leaves at least two
// spaces, which is what separates a row from the "2 of 5 … cannot run" summary.
var tierRow = regexp.MustCompile(`(?m)^  (\d{1,2}) {2,}(.+)$`)

// listedHeads returns the head names headTiers presented as available. Exact
// names, not a substring scan: "Ollama" is a substring of "qwen3:0.6b (Ollama)".
func listedHeads(out string) map[string]bool {
	names := map[string]bool{}
	for _, m := range tierRow.FindAllStringSubmatch(out, -1) {
		for _, n := range strings.Split(m[2], ", ") {
			names[strings.TrimSpace(n)] = true
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
		"1     Claude Code",
		"5     Gemini 3.1 Pro (High)",
		"10    qwen3:0.6b (Ollama)", // LocalOnly ⇒ tier 10 regardless of score
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

// cfg.Tiers is written once by `hyctl init`. Entries that have gone stale must
// read as stale, not as available.
func TestTierAliases_MarksEntriesThatCannotRun(t *testing.T) {
	tiers := []config.Tier{
		{Name: "complex", Heads: []string{"flash-thinking", "pro-high"}},
		{Name: "simple", Heads: []string{"ollama/qwen3:0.6b", "ollama", "ollama/nomic-embed-text:latest"}},
	}
	out := stripANSICodes(tierAliases(tiers, liveHeads(), refuses))

	for _, want := range []string{
		"flash-thinking: not discovered",
		"ollama: binary only, start its local server",
		"ollama/nomic-embed-text:latest: embeddings only, never routed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stale entry not marked: want %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "complex") || !strings.Contains(out, "Gemini 3.1 Pro (High)") {
		t.Errorf("the live half of a partly-stale tier must still be shown:\n%s", out)
	}
}

func TestTierAliases_SaysNoneCanRunRatherThanShowingBlank(t *testing.T) {
	tiers := []config.Tier{{Name: "expert", Heads: []string{"gone", "ollama"}}}
	out := stripANSICodes(tierAliases(tiers, liveHeads(), refuses))
	if !strings.Contains(out, "none can run") {
		t.Errorf("a tier with nothing live must say so, not print an empty cell:\n%s", out)
	}
}

// An unconfigured machine has no aliases to show, and an empty bordered table
// with no rows is worse than no table.
func TestTierAliases_EmptyWhenNoTiersConfigured(t *testing.T) {
	if out := tierAliases(nil, liveHeads(), refuses); out != "" {
		t.Errorf("want no output for an empty tier list, got:\n%s", out)
	}
}
