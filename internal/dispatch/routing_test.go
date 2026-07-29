// SPDX-License-Identifier: MIT

package dispatch

import (
	"testing"

	"github.com/ankit373/hydra/internal/budget"
	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
)

// registryHead is executor.Supports()-eligible without any API key, so these
// tests exercise real selection rather than being filtered out wholesale.
func registryHead(id string, capScore int, local bool) provider.Head {
	return provider.Head{
		ID: id, Name: id, Provider: "agy", Source: "registry",
		CapScore: capScore, LocalOnly: local, AuthReady: true,
	}
}

// routingDispatcher mirrors a real install: heads pre-sorted by CapScore
// (as probe.Run does via rank.ByCapScore) and tiers named the way
// internal/tui/init.go actually writes them — NOT numerically.
func routingDispatcher() *Dispatcher {
	heads := []provider.Head{
		registryHead("strongest", 100, false), // UITier 1  (orchestrator class)
		registryHead("expert", 92, false),     // UITier 2  (enum EXPERT)
		registryHead("mid", 70, false),        // UITier 7  (enum STANDARD)
		registryHead("weak", 40, true),        // UITier 10 (enum GRUNT, local)
	}
	cfg := &config.Config{Tiers: []config.Tier{
		{Name: "expert", Heads: []string{"strongest"}},
		{Name: "local", Heads: []string{"weak"}},
	}}
	return &Dispatcher{
		cfg: cfg, heads: heads,
		policy: policy.New(policy.DefaultRules(false)),
		budget: budget.NewRegistry(nil),
	}
}

// The regression that matters: a cost router must never answer "cheapest"
// with "most expensive". Before #165 every numeric tier fell through to the
// full head list and returned the strongest head.
func TestSelectHeads_NumericTierDoesNotReturnStrongest(t *testing.T) {
	d := routingDispatcher()

	got := d.selectHeads("10", false)
	if len(got) == 0 {
		t.Fatal("tier 10 selected no heads")
	}
	if got[0].ID == "strongest" {
		t.Fatalf("tier 10 (cheapest) selected the STRONGEST head %q — cost routing inverted", got[0].ID)
	}
	if got[0].ID != "weak" {
		t.Errorf("tier 10 primary = %q, want \"weak\"", got[0].ID)
	}
	for _, h := range got {
		if rank.UITier(h) < 10 {
			t.Errorf("tier 10 selected %q at capability tier %d — stronger than requested", h.ID, rank.UITier(h))
		}
	}
}

func TestSelectHeads_NumericTierOrdering(t *testing.T) {
	d := routingDispatcher()

	// Strongest tier: everything is eligible, best first.
	if got := d.selectHeads("1", false); len(got) == 0 || got[0].ID != "strongest" {
		t.Errorf("tier 1 primary = %v, want \"strongest\"", ids(got))
	}
	// Mid tier: excludes the strongest, keeps mid + weaker as fallback.
	got := d.selectHeads("7", false)
	if len(got) == 0 || got[0].ID != "mid" {
		t.Fatalf("tier 7 primary = %v, want \"mid\"", ids(got))
	}
	for _, h := range got {
		if h.ID == "strongest" {
			t.Error("tier 7 must not include the tier-1 head")
		}
	}
}

// An unmatched hint previously widened to every head (and so to the most
// expensive one). It must now select nothing so the caller can report it.
func TestSelectHeads_UnknownTierSelectsNothing(t *testing.T) {
	d := routingDispatcher()
	for _, hint := range []string{"expret", "nonsense", "Expert"} {
		if got := d.selectHeads(hint, false); len(got) != 0 {
			t.Errorf("unknown tier %q selected %v — must not silently widen", hint, ids(got))
		}
	}
}

func TestSelectHeads_NamedTierStillWorks(t *testing.T) {
	d := routingDispatcher()
	if got := d.selectHeads("expert", false); len(got) != 1 || got[0].ID != "strongest" {
		t.Errorf("named tier \"expert\" = %v, want [strongest]", ids(got))
	}
	if got := d.selectHeads("local", false); len(got) != 1 || got[0].ID != "weak" {
		t.Errorf("named tier \"local\" = %v, want [weak]", ids(got))
	}
}

func TestSelectHeads_LocalOnlyFilters(t *testing.T) {
	d := routingDispatcher()
	for _, h := range d.selectHeads("1", true) {
		if !h.LocalOnly {
			t.Errorf("localOnly selection included remote head %q", h.ID)
		}
	}
}

// Named tiers must resolve to a capability number so claudeMode can downgrade
// them; previously Atoi failed and the whole pressure table was inert.
func TestResolveTierHint(t *testing.T) {
	d := routingDispatcher()
	tests := []struct{ in, want string }{
		{"", ""},
		{"8", "8"},         // numeric passes through
		{"expert", "1"},    // named → strongest member's capability tier
		{"local", "10"},    //
		{"bogus", "bogus"}, // unknown returned as-is so the caller can report it
	}
	for _, tt := range tests {
		if got := d.resolveTierHint(tt.in); got != tt.want {
			t.Errorf("resolveTierHint(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// The end-to-end contract CLAUDE.md documents: an enum is a routing
// instruction. GRUNT must not land on the most expensive head.
func TestEnumToTier_RoutesAwayFromStrongest(t *testing.T) {
	d := routingDispatcher()

	cheap := d.selectHeads(EnumToTier("GRUNT"), false)
	if len(cheap) == 0 {
		t.Fatal("GRUNT selected no heads")
	}
	if cheap[0].ID == "strongest" {
		t.Errorf("enum GRUNT routed to the strongest head — the router is inverted")
	}
	if cheap[0].ID != "weak" {
		t.Errorf("enum GRUNT primary = %q, want \"weak\"", cheap[0].ID)
	}

	// EXPERT resolves to tier 2, so the tier-1 orchestrator-class head is
	// deliberately excluded: an enum must not escalate past what it asked for.
	expensive := d.selectHeads(EnumToTier("EXPERT"), false)
	if len(expensive) == 0 || expensive[0].ID != "expert" {
		t.Errorf("enum EXPERT primary = %v, want \"expert\"", ids(expensive))
	}
	for _, h := range expensive {
		if h.ID == "strongest" {
			t.Error("enum EXPERT must not escalate to the tier-1 head")
		}
	}
}

func ids(heads []provider.Head) []string {
	out := make([]string, 0, len(heads))
	for _, h := range heads {
		out = append(out, h.ID)
	}
	return out
}

// When nothing is cheap enough, degrade to the CHEAPEST available head — never
// silently escalate to the most expensive one, which is the #165 failure mode.
func TestSelectHeads_NoCheapHeadFallsBackToCheapest(t *testing.T) {
	d := routingDispatcher()
	d.heads = []provider.Head{
		registryHead("strongest", 100, false), // UITier 1
		registryHead("expert", 92, false),     // UITier 2
	}

	got := d.selectHeads("10", false) // nothing at tier 10
	if len(got) == 0 {
		t.Fatal("fallback selected nothing")
	}
	if got[0].ID == "strongest" {
		t.Errorf("fell back to the STRONGEST head %q — must degrade to the cheapest", got[0].ID)
	}
	if got[0].ID != "expert" {
		t.Errorf("fallback primary = %q, want cheapest (\"expert\")", got[0].ID)
	}
}
