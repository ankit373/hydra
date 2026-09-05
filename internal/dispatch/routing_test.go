// SPDX-License-Identifier: MIT

package dispatch

import (
	"strings"
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
// internal/tui/init.go actually writes them, NOT numerically.
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
		t.Fatalf("tier 10 (cheapest) selected the STRONGEST head %q, cost routing inverted", got[0].ID)
	}
	if got[0].ID != "weak" {
		t.Errorf("tier 10 primary = %q, want \"weak\"", got[0].ID)
	}
	for _, h := range got {
		if rank.UITier(h) < 10 {
			t.Errorf("tier 10 selected %q at capability tier %d, stronger than requested", h.ID, rank.UITier(h))
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
			t.Errorf("unknown tier %q selected %v, must not silently widen", hint, ids(got))
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
		{"8", "8"},      // numeric passes through
		{"expert", "1"}, // named → strongest member's capability tier
		{"local", "10"},
	}
	for _, tt := range tests {
		got, err := d.resolveTierHint(tt.in)
		if err != nil {
			t.Errorf("resolveTierHint(%q) unexpected error: %v", tt.in, err)
		}
		if got != tt.want {
			t.Errorf("resolveTierHint(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// A name absent from cfg.Tiers entirely is a config problem, not a
// routability one, it must produce a distinct error naming the bad tier and
// what IS configured, rather than resolving to a value selectHeads then fails
// on with the generic "no routable heads" message (#451).
func TestResolveTierHint_UnknownNameIsADistinctError(t *testing.T) {
	d := routingDispatcher()
	for _, hint := range []string{"bogus", "expret", "Expert"} {
		got, err := d.resolveTierHint(hint)
		if err == nil {
			t.Fatalf("resolveTierHint(%q) = %q, nil, want an error naming the unknown tier", hint, got)
		}
		if !strings.Contains(err.Error(), hint) {
			t.Errorf("resolveTierHint(%q) error = %q, want it to name the bad tier", hint, err)
		}
		for _, configured := range []string{"expert", "local"} {
			if !strings.Contains(err.Error(), configured) {
				t.Errorf("resolveTierHint(%q) error = %q, want it to list configured tier %q", hint, err, configured)
			}
		}
	}
}

// A numeric tier outside 1-10 can never be routable (rank.UITier never
// produces such a value), it must be rejected with the requested value and
// the valid range, not silently treated as "no tier" or clamped invisibly (#454).
func TestResolveTierHint_NumericOutOfRangeIsRejected(t *testing.T) {
	d := routingDispatcher()
	for _, hint := range []string{"0", "-1", "11", "15", "20"} {
		got, err := d.resolveTierHint(hint)
		if err == nil {
			t.Fatalf("resolveTierHint(%q) = %q, nil, want an out-of-range error", hint, got)
		}
		if !strings.Contains(err.Error(), hint) {
			t.Errorf("resolveTierHint(%q) error = %q, want it to name the requested value", hint, err)
		}
	}
}

// In-range numeric hints (the boundaries included) must still pass through
// untouched, only genuinely out-of-range values are rejected.
func TestResolveTierHint_NumericBoundariesAccepted(t *testing.T) {
	d := routingDispatcher()
	for _, hint := range []string{"1", "10"} {
		got, err := d.resolveTierHint(hint)
		if err != nil {
			t.Errorf("resolveTierHint(%q) unexpected error: %v", hint, err)
		}
		if got != hint {
			t.Errorf("resolveTierHint(%q) = %q, want %q", hint, got, hint)
		}
	}
}

// ValidateTierHint is what --swarm/--confidence must call too, or an invalid
// --tier only errors for plain dispatch (#501). It must accept exactly what
// resolveTierHint can turn into something routable, and reject everything
// else with a clear reason.
func TestValidateTierHint(t *testing.T) {
	cfg := routingDispatcher().cfg // has tiers "expert" and "local"

	valid := []string{"", "1", "8", "10", "expert", "local"}
	for _, hint := range valid {
		if err := ValidateTierHint(cfg, hint); err != nil {
			t.Errorf("ValidateTierHint(%q) = %v, want nil", hint, err)
		}
	}

	invalid := []string{"0", "11", "99", "-1", "bogus", "nonsense"}
	for _, hint := range invalid {
		if err := ValidateTierHint(cfg, hint); err == nil {
			t.Errorf("ValidateTierHint(%q) = nil, want an error", hint)
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
		t.Errorf("enum GRUNT routed to the strongest head, the router is inverted")
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

// When nothing is cheap enough, degrade to the CHEAPEST available head, never
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
		t.Errorf("fell back to the STRONGEST head %q, must degrade to the cheapest", got[0].ID)
	}
	if got[0].ID != "expert" {
		t.Errorf("fallback primary = %q, want cheapest (\"expert\")", got[0].ID)
	}
}

// The existing tier-10 test uses a local head at score 40, which the score
// ladder already put at tier 10, so it passed while the real machine failed.
// Ollama scores exactly 60, which landed at tier 9, and GRUNT degraded past it
// to a paid cloud head (#248). Same shape, real number.
func TestSelectHeads_Tier10PrefersALocalHeadOverAPaidOne(t *testing.T) {
	d := routingDispatcher()
	d.heads = []provider.Head{
		registryHead("paid-cheap", 68, false), // a real Gemini Flash Low score
		registryHead("ollama", 60, true),      // exactly Ollama's score
	}

	got := d.selectHeads("10", false)
	if len(got) == 0 {
		t.Fatal("tier 10 selected no heads, GRUNT would degrade to a paid head")
	}
	if got[0].ID != "ollama" {
		t.Errorf("tier 10 primary = %q, want \"ollama\", a free local head must win the cheapest tier", got[0].ID)
	}
	// Asserting the ID alone is not enough, and I had this wrong first: with
	// only two heads the *degraded* fallback also returns ollama, because it
	// reverses to weakest-first and ollama is weakest. So the test passed with
	// the fix removed. What actually distinguishes a real tier-10 match from a
	// degraded pick is the head's own tier.
	if tier := rank.UITier(got[0]); tier < 10 {
		t.Errorf("tier 10 was served by a head at tier %d, selected via the degradation path, "+
			"not because a local head belongs at the cheapest tier", tier)
	}
}

// The error a user actually hits when the only local head on the machine is one
// no executor can drive. Pointing at `hyctl probe` was worse than useless: probe
// listed the head, so looking there taught them nothing (#248).
func TestDispatch_NoRoutableHeadsNamesTheBlockedHeadAndWhy(t *testing.T) {
	d := routingDispatcher()
	// Exactly how internal/provider/cli registers the PATH-discovered binary.
	d.heads = []provider.Head{
		{ID: "ollama", Name: "Ollama", Provider: "local", Source: "cli", LocalOnly: true},
	}

	msg := d.blockedHeads(true)
	if msg == "" {
		t.Fatal("blockedHeads returned nothing for a head no executor can drive")
	}
	for _, want := range []string{"Ollama", "server"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not mention %q:\n%s", want, msg)
		}
	}
}

// A cloud head must not be listed as the blocker for a local-only run, the
// user could not have used it either way, so naming it sends them the wrong way.
func TestDispatch_BlockedHeadsRespectsLocalOnly(t *testing.T) {
	d := routingDispatcher()
	d.heads = []provider.Head{
		{ID: "mystery", Name: "Mystery Cloud", Provider: "nobody", Source: "cli"},
	}
	if msg := d.blockedHeads(true); msg != "" {
		t.Errorf("local-only run listed a cloud head as the blocker:\n%s", msg)
	}
	if msg := d.blockedHeads(false); msg == "" {
		t.Error("a non-local run should still report the undrivable cloud head")
	}
}
