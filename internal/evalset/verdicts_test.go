// SPDX-License-Identifier: MIT

package evalset

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func verdictsPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "verdicts.jsonl")
}

func mustVerdict(t *testing.T, path string, v Verdict) {
	t.Helper()
	if err := AddVerdict(path, v); err != nil {
		t.Fatal(err)
	}
}

func mustTruth(t *testing.T, path string, tr Truth) {
	t.Helper()
	if err := AddTruth(path, tr); err != nil {
		t.Fatal(err)
	}
}

func checkNames(a Answer) []string {
	var out []string
	for _, c := range a.Checks {
		out = append(out, c.Check)
	}
	return out
}

func TestJoint_GroupsChecksOnOneAnswerInOrder(t *testing.T) {
	p := verdictsPath(t)
	mustVerdict(t, p, Verdict{SpanID: "s1", Head: "local:qwen", Domain: "docs", Check: "grounding", Passed: true})
	mustVerdict(t, p, Verdict{SpanID: "s2", Check: "human", Passed: false})
	mustVerdict(t, p, Verdict{SpanID: "s1", Check: "label", Passed: false})
	mustVerdict(t, p, Verdict{SpanID: "s1", Check: "human", Passed: true})

	got, err := Joint(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Key != "s1" || got[1].Key != "s2" {
		t.Fatalf("answers = %+v, want s1 then s2", got)
	}
	a := got[0]
	if fmt.Sprint(checkNames(a)) != "[grounding label human]" {
		t.Fatalf("checks = %v, want append order", checkNames(a))
	}
	if a.Head != "local:qwen" || a.Domain != "docs" {
		t.Fatalf("head/domain = %q/%q", a.Head, a.Domain)
	}
	if a.Truth != nil {
		t.Fatalf("truth = %+v, want none", a.Truth)
	}
}

func TestJoint_TruthJoinsLaterBySpan(t *testing.T) {
	p := verdictsPath(t)
	mustVerdict(t, p, Verdict{SpanID: "s1", CandidateHash: Hash("x"), Check: "go-test", Passed: true})
	mustVerdict(t, p, Verdict{SpanID: "s1", CandidateHash: Hash("x"), Check: "lint", Passed: true})
	mustTruth(t, p, Truth{SpanID: "s1", Outcome: OutcomeIncorrect, Source: "trust outcome"})

	got, err := Joint(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("answers = %d, want 1", len(got))
	}
	if got[0].Truth == nil || got[0].Truth.Outcome != OutcomeIncorrect {
		t.Fatalf("truth = %+v, want incorrect", got[0].Truth)
	}
	if len(got[0].Checks) != 2 {
		t.Fatalf("checks = %d, want 2", len(got[0].Checks))
	}
}

func TestJoint_FallsBackToCandidateHashWithoutSpan(t *testing.T) {
	p := verdictsPath(t)
	h := Hash("SELECT 1")
	mustVerdict(t, p, Verdict{CandidateHash: h, Check: "editor:validator", Passed: true})
	mustTruth(t, p, Truth{CandidateHash: h, Outcome: OutcomeIncorrect, Source: "review"})
	mustVerdict(t, p, Verdict{SpanID: "s9", CandidateHash: h, Check: "schema", Passed: true})

	got, err := Joint(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Key != h || got[1].Key != "s9" {
		t.Fatalf("answers = %+v, want hash-keyed then span-keyed", got)
	}
	if got[0].Truth == nil || len(got[0].Checks) != 1 {
		t.Fatalf("hash-keyed answer = %+v, want one check and a truth", got[0])
	}
	bySpan, err := VerdictBySpan(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := bySpan[h]; ok || len(bySpan) != 1 {
		t.Fatalf("VerdictBySpan = %v, want only s9", bySpan)
	}
}

func TestVerdictBySpan_AnyFailFails(t *testing.T) {
	p := verdictsPath(t)
	mustVerdict(t, p, Verdict{SpanID: "pass", Check: "grounding", Passed: true})
	mustVerdict(t, p, Verdict{SpanID: "pass", Check: "human", Passed: true})
	mustVerdict(t, p, Verdict{SpanID: "fail", Check: "grounding", Passed: true})
	mustVerdict(t, p, Verdict{SpanID: "fail", Check: "human", Passed: false})
	mustVerdict(t, p, Verdict{SpanID: "overridden", Check: "go-test", Passed: true})
	mustTruth(t, p, Truth{SpanID: "overridden", Outcome: OutcomeIncorrect})
	mustVerdict(t, p, Verdict{SpanID: "rescued", Check: "label", Passed: false})
	mustTruth(t, p, Truth{SpanID: "rescued", Outcome: OutcomeCorrect})

	got, err := VerdictBySpan(p)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"pass": true, "fail": false, "overridden": false, "rescued": true}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("VerdictBySpan = %v, want %v", got, want)
	}
}

func TestAddVerdict_ConcurrentAppendsUnderRace(t *testing.T) {
	p := verdictsPath(t)
	const writers, each = 8, 25
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				span := fmt.Sprintf("s%d", i)
				if err := AddVerdict(p, Verdict{SpanID: span, Check: fmt.Sprintf("w%d", w), Passed: true}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()

	got, err := Joint(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != each {
		t.Fatalf("answers = %d, want %d", len(got), each)
	}
	for _, a := range got {
		if len(a.Checks) != writers {
			t.Fatalf("answer %s has %d checks, want %d", a.Key, len(a.Checks), writers)
		}
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no mode bits to check.
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestAddVerdict_RefusesLinesThatCannotJoin(t *testing.T) {
	p := verdictsPath(t)
	if err := AddVerdict(p, Verdict{Check: "human", Passed: true}); !errors.Is(err, ErrNoAnswerKey) {
		t.Fatalf("no key: err = %v", err)
	}
	if err := AddVerdict(p, Verdict{SpanID: "s1"}); !errors.Is(err, ErrNoCheck) {
		t.Fatalf("no check: err = %v", err)
	}
	if err := AddTruth(p, Truth{SpanID: "s1", Outcome: "maybe"}); !errors.Is(err, ErrBadOutcome) {
		t.Fatalf("bad outcome: err = %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("refused lines still created the log: %v", err)
	}
}
