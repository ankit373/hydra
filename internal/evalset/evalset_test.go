// SPDX-License-Identifier: MIT

package evalset

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func ex(domain, candidate string, passed bool) Example {
	return Example{Domain: domain, Source: "verifier:go", Candidate: candidate, Passed: passed}
}

func TestAddAndLoad(t *testing.T) {
	p := filepath.Join(t.TempDir(), "examples.jsonl")
	added, err := Add(p, ex("go", "func main() {}", true))
	if err != nil || !added {
		t.Fatalf("added=%v err=%v", added, err)
	}
	got, err := Load(p)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %d examples, err=%v", len(got), err)
	}
	if got[0].V != SchemaVersion || got[0].TS == "" {
		t.Errorf("stamping missing: %+v", got[0])
	}
	if got[0].CandidateHash == "" || got[0].TaskHash == "" {
		t.Errorf("hashes not derived: %+v", got[0])
	}
}

// Re-running the same verification is normal. A duplicate would weight that
// case twice in anything computed from the corpus.
func TestAddIsIdempotentOnIdenticalTaskAndCandidate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "examples.jsonl")
	e := ex("go", "func main() {}", true)
	for i := 0; i < 5; i++ {
		added, err := Add(p, e)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && !added {
			t.Fatal("first add did not write")
		}
		if i > 0 && added {
			t.Fatalf("add %d wrote a duplicate", i)
		}
	}
	got, _ := Load(p)
	if len(got) != 1 {
		t.Errorf("corpus has %d examples, want 1", len(got))
	}
}

// A different candidate for the same task is a genuinely new example, that is
// the interesting case, two heads answering the same question differently.
func TestDifferentCandidateForSameTaskIsKept(t *testing.T) {
	p := filepath.Join(t.TempDir(), "examples.jsonl")
	a := ex("go", "func main() {}", true)
	b := ex("go", "func main() { return }", false)
	a.TaskHash, b.TaskHash = "same-task", "same-task"
	if _, err := Add(p, a); err != nil {
		t.Fatal(err)
	}
	added, err := Add(p, b)
	if err != nil || !added {
		t.Fatalf("added=%v err=%v", added, err)
	}
	got, _ := Load(p)
	if len(got) != 2 {
		t.Errorf("got %d, want 2", len(got))
	}
}

// A verdict with no candidate is a statistic, and belongs in calibration.
func TestEmptyCandidateRejected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "examples.jsonl")
	for _, c := range []string{"", "   ", "\n\t"} {
		if _, err := Add(p, ex("go", c, true)); !errors.Is(err, ErrNoCandidate) {
			t.Errorf("candidate %q: err=%v, want ErrNoCandidate", c, err)
		}
	}
	if got, _ := Load(p); len(got) != 0 {
		t.Errorf("rejected examples reached the corpus: %d", len(got))
	}
}

// Ground truth containing PII is still ground truth: dropping it would bias the
// corpus toward whatever happens to contain no PII. It must be marked instead.
func TestPIIIsMarkedNotDropped(t *testing.T) {
	p := filepath.Join(t.TempDir(), "examples.jsonl")
	if _, err := Add(p, ex("go", "user email is alice.smith@example.com in the fixture", true)); err != nil {
		t.Fatal(err)
	}
	got, _ := Load(p)
	if len(got) != 1 {
		t.Fatalf("got %d, want 1, a PII example must be kept", len(got))
	}
	if !got[0].PII {
		t.Error("PII not marked; an export path has nothing to refuse on")
	}
}

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err != nil || got != nil {
		t.Errorf("got %v, %v", got, err)
	}
}

func TestLoadSkipsTornTail(t *testing.T) {
	p := filepath.Join(t.TempDir(), "examples.jsonl")
	if _, err := Add(p, ex("go", "ok", true)); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"v":1,"domain":"go`)
	f.Close()
	got, err := Load(p)
	if err != nil || len(got) != 1 {
		t.Errorf("got %d examples, err=%v; want 1", len(got), err)
	}
}

func TestStats(t *testing.T) {
	in := []Example{
		{Domain: "go", Passed: true}, {Domain: "go", Passed: true}, {Domain: "go", Passed: false},
		{Domain: "ts", Passed: false},
		{Domain: "", Passed: true},
	}
	got := Stats(in)
	if len(got) != 3 {
		t.Fatalf("got %d domains, want 3", len(got))
	}
	if got[0].Domain != "go" || got[0].Total != 3 || got[0].Passed != 2 || got[0].Failed != 1 {
		t.Errorf("go: %+v", got[0])
	}
	if got[0].PassRate < 0.66 || got[0].PassRate > 0.67 {
		t.Errorf("pass rate %v, want ~0.667", got[0].PassRate)
	}
	var unnamed bool
	for _, s := range got {
		if s.Domain == "(none)" {
			unnamed = true
		}
	}
	if !unnamed {
		t.Error("an example with no domain must still be counted")
	}
}

// The eval set must live outside anything a retention pass walks.
// The routing decision must survive the write, or the corpus cannot be grouped
// by the thing routing.yaml is keyed on, which is the only reason to keep it.
func TestEnumAndTierRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "examples.jsonl")
	e := ex("go", "func main() {}", true)
	e.Enum, e.Tier, e.Head = "SIMPLE", 8, "ollama/qwen3"
	if _, err := Add(p, e); err != nil {
		t.Fatalf("add: %v", err)
	}
	got, err := Load(p)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %d, err=%v", len(got), err)
	}
	if got[0].Enum != "SIMPLE" || got[0].Tier != 8 || got[0].Head != "ollama/qwen3" {
		t.Errorf("routing decision lost: enum=%q tier=%d head=%q",
			got[0].Enum, got[0].Tier, got[0].Head)
	}
}

func withHead(enum, head string, n int) []Example {
	out := make([]Example, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, Example{Enum: enum, Head: head, Passed: i%2 == 0})
	}
	return out
}

func statFor(t *testing.T, rd []EnumStat, enum string) EnumStat {
	t.Helper()
	for _, s := range rd {
		if s.Enum == enum {
			return s
		}
	}
	t.Fatalf("no stat for enum %q in %+v", enum, rd)
	return EnumStat{}
}

// Volume on one head is not evidence about a different head, and choosing
// between heads is the whole point. Four times the floor on a single head must
// still read as not ready.
func TestReadinessNeedsSeveralHeadsNotJustVolume(t *testing.T) {
	lopsided := withHead("SIMPLE", "head-a", MinObservationsPerHead*4)
	if s := statFor(t, Readiness(lopsided), "SIMPLE"); s.Ready || s.Comparable != 1 {
		t.Errorf("one head at 4x the floor read as ready: %+v", s)
	}

	spread := append(withHead("SIMPLE", "head-a", MinObservationsPerHead),
		withHead("SIMPLE", "head-b", MinObservationsPerHead)...)
	s := statFor(t, Readiness(spread), "SIMPLE")
	if !s.Ready || s.Comparable != MinComparableHeads {
		t.Errorf("two heads at the floor did not read as ready: %+v", s)
	}
	if s.Total != MinObservationsPerHead*2 {
		t.Errorf("total = %d, want %d", s.Total, MinObservationsPerHead*2)
	}
}

// One short of the floor is short of the floor: rounding it up would recommend
// fitting on the sample size measured to lose to the strongest head.
func TestReadinessIsStrictAtTheFloor(t *testing.T) {
	just := append(withHead("SIMPLE", "head-a", MinObservationsPerHead),
		withHead("SIMPLE", "head-b", MinObservationsPerHead-1)...)
	if s := statFor(t, Readiness(just), "SIMPLE"); s.Ready {
		t.Errorf("one example short read as ready: %+v", s)
	}
}

// A verdict with no head is still ground truth, but it names nothing routable,
// so it can never make an enum fittable however many there are.
func TestReadinessIgnoresUnattributedHead(t *testing.T) {
	s := statFor(t, Readiness(withHead("SIMPLE", "", MinObservationsPerHead*3)), "SIMPLE")
	if s.Ready || s.Comparable != 0 {
		t.Errorf("unattributed heads counted toward comparability: %+v", s)
	}
	if s.Total != MinObservationsPerHead*3 {
		t.Errorf("unattributed examples were dropped: %+v", s)
	}
}

// Examples predating attribution land in "(none)". They are the gap this
// measures, so they must be visible and never ready.
func TestReadinessNeverReadyWithoutEnum(t *testing.T) {
	plenty := append(withHead("", "head-a", MinObservationsPerHead*2),
		withHead("", "head-b", MinObservationsPerHead*2)...)
	s := statFor(t, Readiness(plenty), "(none)")
	if s.Ready {
		t.Errorf("examples with no enum read as ready: %+v", s)
	}
	if s.Total != MinObservationsPerHead*4 {
		t.Errorf("total = %d, want %d", s.Total, MinObservationsPerHead*4)
	}
}

func TestDefaultPathIsNotUnderLogs(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	p := DefaultPath()
	if filepath.Base(filepath.Dir(p)) != "evalset" {
		t.Errorf("path %q is not under evalset/", p)
	}
	if got := filepath.Dir(p); filepath.Base(got) == "logs" || filepath.Base(filepath.Dir(got)) == "logs" {
		t.Errorf("eval set is under logs/ (%q), a retention pass would delete it", p)
	}
}
