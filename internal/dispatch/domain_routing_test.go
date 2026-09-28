// SPDX-License-Identifier: MIT

package dispatch

import (
	"testing"

	"github.com/ankit373/hydra/internal/budget"
	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/trust"
)

// domainDispatcher is routingDispatcher with a calibration store, so selection
// can be measured against real recorded outcomes rather than a stub score.
func domainDispatcher(t *testing.T, heads []provider.Head) *Dispatcher {
	t.Helper()
	cal, err := trust.New("")
	if err != nil {
		t.Fatalf("trust.New: %v", err)
	}
	return &Dispatcher{
		cfg: &config.Config{}, heads: heads,
		policy: policy.New(policy.DefaultRules(false)),
		budget: budget.NewRegistry(nil),
		cal:    cal,
	}
}

// record files n judged outcomes for one head in one domain.
func record(t *testing.T, cal *trust.Calibrator, head, domain string, correct, total int) {
	t.Helper()
	for i := range total {
		outcome := trust.OutcomeIncorrect
		if i < correct {
			outcome = trust.OutcomeCorrect
		}
		if err := cal.Update(head, domain, true, outcome); err != nil {
			t.Fatal(err)
		}
	}
}

// twoHeads is a stronger head on paper and a weaker one, both at the same tier
// so nothing but the measurement separates them.
func twoHeads() []provider.Head {
	return []provider.Head{
		registryHead("paper-strong", 72, false),
		registryHead("paper-weak", 70, false),
	}
}

// The point of #885: within a tier, the head measured best at this kind of
// work is tried first, even when the catalogue rates it lower.
func TestSelectHeads_RoutesToTheHeadMeasuredBestAtTheDomain(t *testing.T) {
	d := domainDispatcher(t, twoHeads())
	record(t, d.cal, "paper-strong", "go", 5, 40) // bad at go
	record(t, d.cal, "paper-weak", "go", 36, 40)  // good at go

	if got := d.selectHeads("", false, "go")[0].ID; got != "paper-weak" {
		t.Errorf("got %s first for go, want paper-weak, which is measured better there", got)
	}
	// And with no domain named, the declared order stands: nothing has been
	// asked about, so nothing may be assumed.
	if got := d.selectHeads("", false, "")[0].ID; got != "paper-strong" {
		t.Errorf("got %s first with no domain, want the declared order", got)
	}
}

// A head strong overall must not be routed work it is measurably bad at, which
// is exactly what pooling across domains hid.
func TestSelectHeads_DomainEvidenceBeatsThePooledRecord(t *testing.T) {
	d := domainDispatcher(t, twoHeads())
	// paper-strong is excellent everywhere except the domain being routed for.
	record(t, d.cal, "paper-strong", "ts", 95, 100)
	record(t, d.cal, "paper-strong", "sql", 3, 60)
	record(t, d.cal, "paper-weak", "sql", 50, 60)

	if got := d.selectHeads("", false, "sql")[0].ID; got != "paper-weak" {
		t.Errorf("got %s first for sql, want paper-weak: the pooled record hid a bad domain", got)
	}
	if got := d.selectHeads("", false, "ts")[0].ID; got != "paper-strong" {
		t.Errorf("got %s first for ts, want paper-strong, which is measured best there", got)
	}
}

// A domain nobody has recorded anything in must route exactly as it did
// before this change, or an unmeasured domain would be a routing change with
// no evidence behind it at all.
func TestSelectHeads_UnmeasuredDomainRoutesLikeNoDomain(t *testing.T) {
	heads := twoHeads()
	d := domainDispatcher(t, heads)
	record(t, d.cal, "paper-strong", "go", 36, 40)

	plain := routingDispatcher()
	plain.heads = heads
	want := ids(plain.selectHeads("", false, ""))
	got := ids(d.selectHeads("", false, "rust"))

	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rust routes %v, an unmeasured domain must route as %v", got, want)
		}
	}
}

// The cheapest-first degrade path answers "what can I afford" when nothing was
// cheap enough. Re-sorting it on quality would quietly turn a cost fallback
// into an escalation, which is the defect #165 was about.
func TestSelectHeads_DegradePathStaysCheapestFirst(t *testing.T) {
	heads := []provider.Head{
		registryHead("strongest", 100, false), // UITier 1
		registryHead("mid", 70, false),        // UITier 7
	}
	d := domainDispatcher(t, heads)
	// Measured brilliant in this domain, and the most expensive thing here.
	record(t, d.cal, "strongest", "go", 40, 40)
	record(t, d.cal, "mid", "go", 2, 40)

	// Tier 10: nothing is that cheap, so this is the degrade path.
	got := d.selectHeads("10", false, "go")
	if len(got) == 0 {
		t.Fatal("degrade path selected nothing")
	}
	if got[0].ID != "mid" {
		t.Errorf("got %s first, want the cheapest head: a cost fallback must not be re-sorted on quality", got[0].ID)
	}
}

// With no calibrator at all, which is what an unreadable store leaves, routing
// must be exactly what it was rather than an empty measurement for everything.
func TestSelectHeads_NoCalibratorLeavesTheOrderAlone(t *testing.T) {
	d := domainDispatcher(t, twoHeads())
	record(t, d.cal, "paper-weak", "go", 36, 40)
	measured := ids(d.selectHeads("", false, "go"))

	d.cal = nil
	unmeasured := ids(d.selectHeads("", false, "go"))

	if unmeasured[0] != "paper-strong" {
		t.Errorf("got %v with no calibrator, want the declared order first", unmeasured)
	}
	if measured[0] == unmeasured[0] {
		t.Fatal("test is inert: the measurement did not change the order to begin with")
	}
}

// routingDomain is what turns a dispatch into a domain: the caller's word, or
// the file it acts on, and never a guess.
func TestRoutingDomain(t *testing.T) {
	cases := []struct {
		name string
		opts Options
		want string
	}{
		{"explicit wins", Options{Domain: "sql", Resource: "main.go"}, "sql"},
		{"derived from the file", Options{Resource: "internal/auth/token.go"}, "go"},
		{"file with no extension", Options{Resource: "Makefile"}, trust.DefaultDomain},
		{"nothing to go on", Options{}, ""},
		{"blank is nothing, not default", Options{Domain: "   "}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := routingDomain(tc.opts); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// --dry-run has to say what it ranked on, or a reordering is indistinguishable
// from an arbitrary one.
func TestExplain_ReportsTheEvidenceBehindTheOrder(t *testing.T) {
	heads := twoHeads()
	d := domainDispatcher(t, heads)
	record(t, d.cal, "paper-weak", "go", 36, 40)
	record(t, d.cal, "paper-weak", "ts", 1, 10)

	scores := d.explain(heads, "go")
	sc, ok := scores["paper-weak"]
	if !ok {
		t.Fatal("no score for a head that has been measured")
	}
	if sc.N != 50 || sc.InDomain != 40 {
		t.Errorf("got n=%d in-domain=%d, want 50 and 40", sc.N, sc.InDomain)
	}
	if sc.Declared != 70 || sc.Effective <= sc.Declared {
		t.Errorf("got declared=%d effective=%d, want the measurement to have raised it", sc.Declared, sc.Effective)
	}
	if d.explain(heads, "") != nil {
		t.Error("explained an order that was never narrowed by a domain")
	}
}
