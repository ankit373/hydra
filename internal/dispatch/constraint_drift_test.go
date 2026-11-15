// SPDX-License-Identifier: MIT

package dispatch

import (
	"math/rand/v2"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/trust"
)

// driftRequirement sits under a 0.92 head's bound on 20 fresh results and over
// a 0.70 head's bound on any number.
const driftRequirement = 0.80

// driftFixture is a cheap head and a dear one, both measured at 0.92 in go
// over 1000 judged answers each.
func driftFixture(t *testing.T) (*Dispatcher, *rand.Rand) {
	t.Helper()
	heads := []provider.Head{registryHead("dear", 92, false), registryHead("cheap", 90, false)}
	d := pricedDispatcher(t, heads, map[string]float64{"dear": 0.02, "cheap": 0.001})
	r := rand.New(rand.NewPCG(1546, 1))
	feed(t, d.cal, r, 0.92, 0.92, 1000)
	if first := driftFirst(d); first != "cheap" {
		t.Fatalf("before any change %s ran first, want cheap: the fixture needs both to clear", first)
	}
	return d, r
}

// feed judges n answers from each head in turn, at the given rates.
func feed(t *testing.T, cal *trust.Calibrator, r *rand.Rand, cheap, dear float64, n int) {
	t.Helper()
	for range n {
		judge(t, cal, r, "cheap", cheap)
		judge(t, cal, r, "dear", dear)
	}
}

func judge(t *testing.T, cal *trust.Calibrator, r *rand.Rand, head string, rate float64) {
	t.Helper()
	outcome := trust.OutcomeIncorrect
	if r.Float64() < rate {
		outcome = trust.OutcomeCorrect
	}
	if err := cal.Update(head, "go", true, outcome); err != nil {
		t.Fatal(err)
	}
}

func driftFirst(d *Dispatcher) string {
	got, _, _ := d.constrain(d.selectHeads("", false, "go"), "go", driftRequirement)
	return got[0].ID
}

// A cheap head that got worse loses its clearance on its record since the
// change, not after its whole history is outweighed.
func TestConstrain_DroppedHeadLosesClearance(t *testing.T) {
	d, r := driftFixture(t)
	for n := 1; n <= 100; n++ {
		feed(t, d.cal, r, 0.70, 0.92, 1)
		if driftFirst(d) == "dear" {
			t.Logf("dear first after %d post-drop answers", n)
			return
		}
	}
	c, total := d.cal.CommitmentsSince("cheap", "go")
	t.Fatalf("cheap still first 100 answers after it dropped to 0.70 (since-change record %d/%d)", c, total)
}

// The same head, fixed, is first again once it has 20 good results since.
func TestConstrain_RecoveredHeadClearsAgain(t *testing.T) {
	d, r := driftFixture(t)
	feed(t, d.cal, r, 0.70, 0.92, 300)
	if first := driftFirst(d); first != "dear" {
		t.Fatalf("%s first after 300 answers at 0.70, want dear", first)
	}
	for n := 1; n <= 300; n++ {
		feed(t, d.cal, r, 1, 0.92, 1)
		if driftFirst(d) == "cheap" {
			if c, total := d.cal.CommitmentsSince("cheap", "go"); c < 20 || c != total {
				t.Fatalf("cheap first on a since-change record of %d/%d, want 20 or more all good", c, total)
			}
			t.Logf("cheap first again after %d recovered answers", n)
			return
		}
	}
	t.Fatal("cheap never first again after 300 good answers")
}
