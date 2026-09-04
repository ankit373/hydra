// SPDX-License-Identifier: MIT

package sketch

import (
	"encoding/json"
	"errors"
	"math"
	"math/rand/v2"
	"sort"
	"testing"
)

func lognormal(n int, seed uint64) []float64 {
	r := rand.New(rand.NewPCG(seed, 7))
	out := make([]float64, n)
	for i := range out {
		out[i] = math.Max(1, math.Exp(7.6+0.9*r.NormFloat64()))
	}
	return out
}

func exact(vals []float64, q float64) float64 {
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	return s[int(q*float64(len(s)-1))]
}

// The contract is a *relative* error bound, which is what makes this usable on
// skewed latency data where rank-error sketches go arbitrarily wrong in the tail.
func TestRelativeErrorHolds(t *testing.T) {
	vals := lognormal(200000, 1)
	for _, alpha := range []float64{0.02, 0.01, 0.005} {
		s := New(alpha)
		for _, v := range vals {
			s.Add(v)
		}
		for _, q := range []float64{0.5, 0.9, 0.95, 0.99, 0.999} {
			want, got := exact(vals, q), s.Quantile(q)
			rel := math.Abs(got-want) / want
			if rel > alpha {
				t.Errorf("alpha=%v p%.1f: got %.0f want %.0f rel err %.4f exceeds bound", alpha, q*100, got, want, rel)
			}
		}
		if s.Count() != int64(len(vals)) {
			t.Errorf("alpha=%v: count %d, want %d", alpha, s.Count(), len(vals))
		}
	}
}

// Mergeability is the property that lets two machines combine statistics
// without either shipping raw data. A merged sketch must be as good as one
// sketch of the union, not merely close.
func TestMergeMatchesSingleSketchOfTheUnion(t *testing.T) {
	a, b := lognormal(60000, 2), lognormal(60000, 3)
	all := append(append([]float64(nil), a...), b...)

	sa, sb, whole := New(0.01), New(0.01), New(0.01)
	for _, v := range a {
		sa.Add(v)
		whole.Add(v)
	}
	for _, v := range b {
		sb.Add(v)
		whole.Add(v)
	}
	if err := sa.Merge(sb); err != nil {
		t.Fatal(err)
	}
	if sa.Count() != whole.Count() {
		t.Fatalf("merged count %d, want %d", sa.Count(), whole.Count())
	}
	for _, q := range []float64{0.5, 0.9, 0.99} {
		merged, single, want := sa.Quantile(q), whole.Quantile(q), exact(all, q)
		if merged != single {
			t.Errorf("p%.0f: merged %.0f != single %.0f", q*100, merged, single)
		}
		if rel := math.Abs(merged-want) / want; rel > 0.01 {
			t.Errorf("p%.0f: merged rel err %.4f exceeds alpha", q*100, rel)
		}
	}
}

func TestMergeRejectsMismatchedAlpha(t *testing.T) {
	a, b := New(0.01), New(0.02)
	a.Add(1)
	b.Add(1)
	if err := a.Merge(b); !errors.Is(err, ErrAlphaMismatch) {
		t.Errorf("err = %v, want ErrAlphaMismatch", err)
	}
}

func TestMergeNilIsNoOp(t *testing.T) {
	s := New(0.01)
	s.Add(5)
	if err := s.Merge(nil); err != nil || s.Count() != 1 {
		t.Errorf("err=%v count=%d", err, s.Count())
	}
}

// A sketch is written to disk and read back by a different process, so the
// round trip must preserve quantiles exactly, not approximately.
func TestJSONRoundTripPreservesQuantiles(t *testing.T) {
	s := New(0.01)
	for _, v := range lognormal(50000, 4) {
		s.Add(v)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var back Sketch
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Count() != s.Count() {
		t.Fatalf("count %d, want %d", back.Count(), s.Count())
	}
	for _, q := range []float64{0, 0.25, 0.5, 0.9, 0.99, 1} {
		if a, b := s.Quantile(q), back.Quantile(q); a != b {
			t.Errorf("p%.0f: %v != %v after round trip", q*100, a, b)
		}
	}
}

// Memory must stay bounded whatever it is fed. Collapsing folds the lowest
// buckets together, so the guarantee is directional: the tail keeps its
// relative-error bound, the small end degrades. Asserting a tight bound on
// *every* quantile here would be asserting something the algorithm does not
// promise — at alpha 0.01 a 256-bucket cap represents a 167x range while this
// population spans ~1300x, so the bottom must give.
func TestCollapsePreservesTheTail(t *testing.T) {
	s := New(0.01)
	s.MaxBuckets = 256
	vals := lognormal(50000, 5)
	for _, v := range vals {
		s.Add(v)
	}
	if len(s.Buckets) > s.MaxBuckets {
		t.Fatalf("buckets %d exceeds cap %d", len(s.Buckets), s.MaxBuckets)
	}
	for _, q := range []float64{0.5, 0.9, 0.99, 0.999} {
		want, got := exact(vals, q), s.Quantile(q)
		if rel := math.Abs(got-want) / want; rel > s.Alpha {
			t.Errorf("p%.1f after collapse: got %.0f want %.0f rel err %.4f exceeds alpha", q*100, got, want, rel)
		}
	}
	// The low end is allowed to be wrong, but only upward: collapsing merges a
	// bucket into its neighbour above, so a collapsed value is never understated.
	if got, want := s.Quantile(0.001), exact(vals, 0.001); got < want*(1-s.Alpha) {
		t.Errorf("p0.1 understated: got %.0f, true %.0f — collapse must only inflate", got, want)
	}
}

// At the default settings the cap is unreachable for any realistic latency or
// cost population, so the tight bound holds everywhere.
func TestDefaultCapNeverCollapsesRealisticData(t *testing.T) {
	s := New(DefaultAlpha)
	for _, v := range lognormal(200000, 6) {
		s.Add(v)
	}
	if len(s.Buckets) >= s.MaxBuckets {
		t.Fatalf("realistic data hit the bucket cap (%d) — the default is too tight", len(s.Buckets))
	}
	t.Logf("%d buckets for 200k samples, cap %d", len(s.Buckets), s.MaxBuckets)
}

func TestZerosAndNegativesAreNotBucketed(t *testing.T) {
	s := New(0.01)
	for i := 0; i < 10; i++ {
		s.Add(0)
	}
	for i := 0; i < 90; i++ {
		s.Add(100)
	}
	if s.Zeros != 10 || s.Count() != 100 {
		t.Fatalf("zeros=%d n=%d", s.Zeros, s.Count())
	}
	if got := s.Quantile(0.05); got != 0 {
		t.Errorf("p5 = %v, want 0 (zeros sort first)", got)
	}
	if got := s.Quantile(0.9); math.Abs(got-100)/100 > 0.01 {
		t.Errorf("p90 = %v, want ~100", got)
	}
	s.Add(-1)
	if s.Negatives != 1 {
		t.Errorf("negatives = %d, want 1", s.Negatives)
	}
	if v := s.Quantile(0); !math.IsNaN(v) {
		t.Errorf("p0 with a negative present = %v, want NaN rather than an invented value", v)
	}
}

// A NaN or Inf must never enter a bucket; one would corrupt every later answer.
func TestNaNAndInfAreDropped(t *testing.T) {
	s := New(0.01)
	s.Add(math.NaN())
	s.Add(math.Inf(1))
	s.Add(math.Inf(-1))
	if s.Count() != 0 || len(s.Buckets) != 0 {
		t.Fatalf("count=%d buckets=%d, want 0/0", s.Count(), len(s.Buckets))
	}
	s.Add(42)
	if got := s.Quantile(0.5); math.Abs(got-42)/42 > 0.01 {
		t.Errorf("p50 = %v, want ~42", got)
	}
}

func TestEmptySketchAndBadAlpha(t *testing.T) {
	s := New(0.01)
	if s.Count() != 0 || s.Quantile(0.5) != 0 {
		t.Error("empty sketch must report 0")
	}
	for _, bad := range []float64{0, 1, -0.5, 2, math.NaN()} {
		if got := New(bad).Alpha; got != DefaultAlpha {
			t.Errorf("New(%v).Alpha = %v, want DefaultAlpha", bad, got)
		}
	}
}

func TestQuantileClampsOutOfRange(t *testing.T) {
	s := New(0.01)
	for i := 1; i <= 100; i++ {
		s.Add(float64(i))
	}
	if s.Quantile(-1) != s.Quantile(0) || s.Quantile(2) != s.Quantile(1) {
		t.Error("out-of-range q must clamp")
	}
}

func TestCloneIsDeep(t *testing.T) {
	s := New(0.01)
	s.Add(10)
	c := s.Clone()
	c.Add(1000)
	if s.Count() == c.Count() {
		t.Error("clone shares state with its source")
	}
}
