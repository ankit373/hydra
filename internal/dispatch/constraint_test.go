// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/trust"
)

// pricedDispatcher is domainDispatcher with a price for every head, which is
// the input the cost constraint needs and the probe-only dispatchers lack.
func pricedDispatcher(t *testing.T, heads []provider.Head, prices map[string]float64) *Dispatcher {
	t.Helper()
	d := domainDispatcher(t, heads)
	d.price = func(h provider.Head) (float64, bool) {
		usd, ok := prices[h.ID]
		return usd, ok
	}
	return d
}

// costly is a strong head that charges, and free a local one that does not.
func costly() []provider.Head {
	return []provider.Head{
		registryHead("paid", 92, false),
		registryHead("local", 55, true),
	}
}

var listPrices = map[string]float64{"paid": 0.0225, "local": 0}

// bothMeasured gives each head a flawless in-domain record, so both clear the
// requirement and the stronger one is still the stronger one. That is the case
// the constraint exists for: #885 already routes to the better-measured head,
// and the question left is whether being *enough* ends the auction.
func bothMeasured(t *testing.T, d *Dispatcher) {
	t.Helper()
	// paid:  (100 + 20*0.92) / 120 = 0.987 -> 99
	// local: (100 + 20*0.55) / 120 = 0.925 -> 93
	record(t, d.cal, "paid", "go", 100, 100)
	record(t, d.cal, "local", "go", 100, 100)
}

// The whole point: with no tier pinned, work goes to the cheapest head this
// machine has measured competent at the domain, not to the best-measured one.
func TestConstrain_UnpinnedRoutesToTheCheapestMeasuredCompetentHead(t *testing.T) {
	d := pricedDispatcher(t, costly(), listPrices)
	bothMeasured(t, d)

	measured := d.selectHeads("", false, "go")
	if measured[0].ID != "paid" {
		t.Fatalf("measured order is %v; this test needs the expensive head to be the better one", ids(measured))
	}

	got, scores, _ := d.constrain(measured, "go", 0.90)
	if got[0].ID != "local" {
		t.Fatalf("ran %s first, want local: measured %d in go over %d judged answers, and it is free",
			got[0].ID, scores["local"].Effective, scores["local"].InDomain)
	}
	if got[1].ID != "paid" {
		t.Errorf("chain = %v, want the paid head still available as a fallback", ids(got))
	}
}

// The floor that makes this safe on by default: with nothing judged in this
// domain, the order is the one #885 already produced, unchanged.
func TestConstrain_WithoutInDomainEvidenceNothingMoves(t *testing.T) {
	d := pricedDispatcher(t, costly(), listPrices)

	before := d.selectHeads("", false, "go")
	after, _, _ := d.constrain(before, "go", 0.90)
	if strings.Join(ids(after), ",") != strings.Join(ids(before), ",") {
		t.Errorf("order changed from %v to %v with nothing measured", ids(before), ids(after))
	}
}

// A pinned tier is a user instruction. One word must not route two ways
// (#782), so a dispatch that named a tier gets exactly the chain it named.
func TestDispatch_APinnedTierIsNotReordered(t *testing.T) {
	d := pricedDispatcher(t, costly(), listPrices)
	bothMeasured(t, d)

	// Same machine, same evidence, same domain: the only difference is that
	// one dispatch named a tier and the other did not.
	unpinned, _, _ := d.constrain(d.selectHeads("", false, "go"), "go", 0.90)
	pinned := d.selectHeads("2", false, "go")

	if unpinned[0].ID != "local" {
		t.Fatalf("unpinned ran %s first, want the cheap head", unpinned[0].ID)
	}
	if pinned[0].ID != "paid" {
		t.Errorf("--tier 2 ran %s first: a named tier was reordered onto price", pinned[0].ID)
	}
}

// Every input the constraint needs can be missing, and each missing one has to
// leave routing where it was rather than produce a different answer quietly.
func TestConstrain_DegradesToTodaysOrder(t *testing.T) {
	heads := costly()
	base := pricedDispatcher(t, heads, listPrices)
	bothMeasured(t, base)
	want := ids(base.selectHeads("", false, "go"))

	noPrice := pricedDispatcher(t, heads, listPrices)
	bothMeasured(t, noPrice)
	noPrice.price = nil

	noCal := pricedDispatcher(t, heads, listPrices)
	noCal.cal = nil

	cases := []struct {
		name string
		d    *Dispatcher
		dom  string
		req  float64
	}{
		{"no pricing", noPrice, "go", 0.90},
		{"no calibration", noCal, "go", 0.90},
		{"no domain", base, "", 0.90},
		{"no requirement", base, "go", 0},
	}
	for _, c := range cases {
		candidates := c.d.selectHeads("", false, c.dom)
		got, _, _ := c.d.constrain(candidates, c.dom, c.req)
		if strings.Join(ids(got), ",") != strings.Join(ids(candidates), ",") {
			t.Errorf("%s: reordered %v to %v", c.name, ids(candidates), ids(got))
		}
	}
	// And the baseline itself is the measured order, or the cases above are
	// all agreeing with nothing.
	if want[0] != "paid" {
		t.Fatalf("unmeasured order is %v; these cases compare against it", want)
	}
}

// The requirement is the defect model's, not a number invented here: a prompt
// that costs more to get wrong demands a better-measured head. It is also the
// same number `hyctl trust defect` prints, or the two would disagree about
// what the same task needs.
func TestRequirementFor_PersonalDataRaisesTheBar(t *testing.T) {
	baseline := requirementFor(nil)
	if baseline <= 0 || baseline >= 1 {
		t.Fatalf("baseline requirement %v is not a probability", baseline)
	}
	if want := trust.NewDefectModel().RequiredConfidence(trust.Task{}); baseline != want {
		t.Errorf("baseline %.3f, defect model says %.3f", baseline, want)
	}
	pii := requirementFor(&policy.Classification{PII: true})
	if pii <= baseline {
		t.Errorf("personal data demanded %.3f, no more than the baseline %.3f", pii, baseline)
	}
}

// A head nothing can price is not free. Reading pricing's zero as $0.00 would
// make an unpriceable head the cheapest thing on the machine.
func TestHeadPrice_UnpriceableIsNotFree(t *testing.T) {
	if p := headPrice(nil); p != nil {
		t.Error("headPrice invented a price with no pricing database")
	}
}

// Constrained is the routing decision itself, so an off-policy estimate cannot
// describe a policy the router does not implement.
func TestConstrained_AgreesWithWhatRoutingWouldDo(t *testing.T) {
	d := pricedDispatcher(t, costly(), listPrices)
	bothMeasured(t, d)

	head, ok := d.Constrained("go")
	if !ok {
		t.Fatal("Constrained found no head to route to")
	}
	ordered, _, _ := d.constrain(d.selectHeads("", false, "go"), "go", requirementFor(nil))
	if head.ID != ordered[0].ID {
		t.Errorf("Constrained says %s, routing says %s", head.ID, ordered[0].ID)
	}

	empty := pricedDispatcher(t, nil, nil)
	if _, ok := empty.Constrained("go"); ok {
		t.Error("Constrained named a head on a machine with none")
	}
}

// The dry run must explain the order it got. Scores comes out of the same call
// that produced the ordering, so the two cannot drift apart.
func TestConstrain_ReportsTheEvidenceItOrderedOn(t *testing.T) {
	d := pricedDispatcher(t, costly(), listPrices)
	bothMeasured(t, d)

	got, scores, _ := d.constrain(d.selectHeads("", false, "go"), "go", 0.90)
	sc := scores[got[0].ID]
	if !sc.Clears {
		t.Errorf("%s ran first but is not marked as clearing the requirement", got[0].ID)
	}
	if sc.InDomain < rank.MinCommitments {
		t.Errorf("%s ran first on %d in-domain observations", got[0].ID, sc.InDomain)
	}
	// The head that lost cleared too, and the reason it lost is its price.
	// Reporting it as failing the requirement would explain the wrong thing.
	if !scores["paid"].Clears {
		t.Error("the head that lost on price is reported as failing the requirement")
	}
	if scores["paid"].CostUSD <= sc.CostUSD {
		t.Errorf("paid costs %v, the chosen head %v", scores["paid"].CostUSD, sc.CostUSD)
	}
}

// End to end through Dispatch, which is where the pin is actually read: the
// same machine, the same evidence and the same domain route two different ways
// depending only on whether the caller named a tier.
func TestDispatch_ConstraintAppliesOnlyWhenNoTierIsPinned(t *testing.T) {
	s := testutil.NewSandbox(t)
	cal, err := trust.New("")
	if err != nil {
		t.Fatal(err)
	}
	d := liveDispatcher(echoHead(t, s, "paid", 92), echoHead(t, s, "local", 55))
	d.cal = cal
	d.price = func(h provider.Head) (float64, bool) {
		return map[string]float64{"paid": 0.0225, "local": 0}[h.ID], true
	}
	bothMeasured(t, d)

	unpinned, err := d.Dispatch(context.Background(), "x", Options{DryRun: true, Domain: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if unpinned.Head.ID != "local" {
		t.Errorf("unpinned dispatch chose %s, want the cheapest competent head", unpinned.Head.ID)
	}
	if unpinned.Requirement <= 0 {
		t.Error("an unpinned dispatch reported no requirement, so nothing can explain the choice")
	}

	pinned, err := d.Dispatch(context.Background(), "x", Options{DryRun: true, Domain: "go", TierHint: "2"})
	if err != nil {
		t.Fatal(err)
	}
	if pinned.Head.ID != "paid" {
		t.Errorf("--tier 2 chose %s: a named tier was reordered onto price", pinned.Head.ID)
	}
	if pinned.Requirement != 0 {
		t.Errorf("a pinned dispatch reported a requirement of %v, which it never applied", pinned.Requirement)
	}
}

// A requirement nothing applied must not be reported. Without calibration or
// pricing the order is the one it always was, and printing a bar beside it
// would describe a decision the router did not make.
func TestConstrain_ReportsNoRequirementWhenItDidNotApplyOne(t *testing.T) {
	heads := costly()
	cases := []struct {
		name string
		make func() *Dispatcher
	}{
		{"no calibration", func() *Dispatcher {
			d := pricedDispatcher(t, heads, listPrices)
			d.cal = nil
			return d
		}},
		{"no pricing", func() *Dispatcher {
			d := pricedDispatcher(t, heads, listPrices)
			bothMeasured(t, d)
			d.price = nil
			return d
		}},
	}
	for _, c := range cases {
		d := c.make()
		if _, _, req := d.constrain(d.selectHeads("", false, "go"), "go", 0.90); req != 0 {
			t.Errorf("%s: reported a requirement of %v that nothing applied", c.name, req)
		}
	}
}

// The router ranks on the domain (#885) and nothing recorded which one, so no
// off-policy question about domain-aware routing could be asked of the log at
// all. A row that names a routing key the routing did not use is the #832
// defect, so this reads the one derivation rather than the raw flag.
func TestLogDispatch_CostRowCarriesTheRoutingDomain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	d := newTestDispatcher()
	if err := d.logDispatch(logResult(), "p", Options{Resource: "internal/auth/token.go"}, 1, "span"); err != nil {
		t.Fatal(err)
	}
	rows := readLog(t, home, "cost.jsonl")
	if len(rows) != 1 {
		t.Fatalf("cost.jsonl: got %d rows, want 1", len(rows))
	}
	want := trust.DomainForFile("internal/auth/token.go")
	if got := rows[0]["domain"]; got != want {
		t.Errorf("domain = %v, want %q, the key the candidates were ranked on", got, want)
	}

	// And a dispatch that ranked on nothing says nothing, rather than logging
	// an empty string a reader would have to interpret.
	second := t.TempDir()
	t.Setenv("HOME", second)
	t.Setenv("USERPROFILE", second)
	if err := d.logDispatch(logResult(), "p", Options{}, 1, "span"); err != nil {
		t.Fatal(err)
	}
	rows = readLog(t, second, "cost.jsonl")
	if _, present := rows[0]["domain"]; present {
		t.Errorf("a dispatch with no domain logged one: %v", rows[0]["domain"])
	}
}
