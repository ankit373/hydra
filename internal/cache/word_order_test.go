// SPDX-License-Identifier: MIT

package cache

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// The cache is the one component that can answer with something no head just
// produced, so its failure mode is a confident answer to a question nobody
// asked. Comparing content tokens as a set made a question and its reverse the
// same question, and cosine does not save it: these six pairs measure 0.9736 to
// 0.9913 with nomic-embed-text, above the 0.95 threshold, so the dense half
// rates a reversed question as *more* alike than an average restatement.
// Measured through Store.Lookup before the fix: asking "merge main into
// develop" returned the answer produced for "merge develop into main" (#1010).
func TestSameQuestion_AReversedQuestionIsADifferentQuestion(t *testing.T) {
	for _, tc := range [][2]string{
		{"merge develop into main", "merge main into develop"},
		{"copy the config to the backup", "copy the backup to the config"},
		{"rename old.go to new.go", "rename new.go to old.go"},
		{"replace foo with bar", "replace bar with foo"},
		{"move the cache before the policy", "move the policy before the cache"},
		{"revert the fix that broke the test", "revert the test that broke the fix"},
	} {
		if sameQuestion(content(Normalize(tc[0])), content(Normalize(tc[1]))) {
			t.Errorf("served one for the other:\n  %q\n  %q", tc[0], tc[1])
		}
	}
}

// Repeats carry meaning too, and a set silently collapsed them.
func TestSameQuestion_ARepeatedWordIsNotDropped(t *testing.T) {
	if sameQuestion(content(Normalize("test the test")), content(Normalize("test"))) {
		t.Error(`"test the test" was treated as "test"`)
	}
}

// The true-hit side, and the reason order is free rather than paid for: every
// perturbation the false-hit harness treats as a restatement leaves the content
// sequence alone, because case, punctuation and whitespace are normalised
// before tokenizing and filler words are stopwords.
func TestSameQuestion_RestatementsAreStillTheSameQuestion(t *testing.T) {
	base := "rotate the signing key in internal/auth/token.go"
	for _, tc := range []struct{ name, q string }{
		{"case", strings.ToUpper(base)},
		{"trailing punctuation", base + "?"},
		{"whitespace", "  " + strings.ReplaceAll(base, " ", "   ") + "\n"},
		{"filler words", "please can you " + base + " for me"},
	} {
		if !sameQuestion(content(Normalize(tc.q)), content(Normalize(base))) {
			t.Errorf("%s: a restatement was refused, which turns the cache into a hash map:\n  %q", tc.name, tc.q)
		}
	}
}

// The corpus check the cosine gate was justified against, run against the gate
// that now carries the decision. Every pair of distinct commit subjects is a
// pair of distinct questions, so any pair served is a false hit. Needs no
// model, unlike the cosine half, so it runs in CI rather than behind a tag.
func TestSameQuestion_ServesNoPairOfDistinctPrompts(t *testing.T) {
	raw, err := os.ReadFile("../retrieve/testdata/corpus.txt")
	if err != nil {
		t.Fatal(err)
	}
	var terms [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			terms = append(terms, content(Normalize(s)))
		}
	}
	if len(terms) < 100 {
		t.Fatalf("corpus has %d usable lines, too few for this to mean anything", len(terms))
	}
	pairs, served := 0, 0
	for i := range terms {
		for j := i + 1; j < len(terms); j++ {
			pairs++
			if sameQuestion(terms[i], terms[j]) {
				served++
			}
		}
	}
	if served != 0 {
		t.Errorf("%d of %d distinct pairs would be served as the same question", served, pairs)
	}
	t.Logf("%d distinct pairs, %d served", pairs, served)
}

// The same thing through the real entry point, with the dense half given its
// best possible case: one vector for both prompts, cosine exactly 1.0. Before
// the fix this returned the stored answer; the measured pair sat at 0.9770,
// which is only lower than this, so passing here covers the real case.
func TestLookup_AReversedQuestionIsRefusedEvenAtCosineOne(t *testing.T) {
	s, err := OpenDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	vec := []float32{1, 0, 0}
	stored := Normalize("merge develop into main")
	if err := s.PutVec(Entry{Prompt: stored, Response: "answer for develop into main"}, vec); err != nil {
		t.Fatal(err)
	}

	out := s.Lookup("merge main into develop", vec, DefaultThreshold)
	if out.Found {
		t.Errorf("served %q for the reversed question; this is the wrong-answer failure the package exists to avoid", out.Hit.Response)
	}
	if !out.Refused {
		t.Error("not counted as a refusal, so the gate that earned its place is invisible in the stats")
	}

	// The same store must still serve a genuine restatement, or the fix has
	// turned the cache into a hash map.
	if out := s.Lookup("please can you merge develop into main for me", vec, DefaultThreshold); !out.Found {
		t.Error("a filler-word restatement was refused")
	}
}

// The refusal counter is what says whether the token gate earns its place, so
// it has to keep counting the case it was introduced for: cosine would have
// served this and the tokens said no. Making the tokens pick the candidate
// could have quietly turned that into a plain miss (#1015).
func TestLookup_StillCountsARefusalTheDenseHalfWouldHaveServed(t *testing.T) {
	s, err := OpenDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	vec := []float32{1, 0, 0}
	if err := s.PutVec(Entry{Prompt: Normalize("rotate the signing key"), Response: "x"}, vec); err != nil {
		t.Fatal(err)
	}

	// Same vector, so cosine is 1.0 and would serve; different content word, so
	// the token gate must not.
	out := s.Lookup("rotate the signing certificate", vec, DefaultThreshold)
	if out.Found {
		t.Fatalf("served a different question at cosine 1.0: %q", out.Hit.Response)
	}
	if !out.Refused {
		t.Error("counted as a plain miss, so the gate that earned its place is invisible in the stats")
	}
}

// The other way a near match can now fail: the tokens agree and the dense half,
// where there is one, does not. That is a refusal too, not a miss, or a machine
// with an embedder would under-report what its cache declined.
func TestLookup_ADenseDisagreementIsARefusalNotAMiss(t *testing.T) {
	s, err := OpenDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutVec(Entry{Prompt: Normalize("rotate the signing key"), Response: "x"}, []float32{1, 0, 0}); err != nil {
		t.Fatal(err)
	}

	out := s.Lookup("please rotate the signing key", []float32{0, 1, 0}, DefaultThreshold)
	if out.Found {
		t.Error("served a candidate the dense half rejected")
	}
	if !out.Refused {
		t.Error("a dense disagreement was recorded as a miss")
	}
}

// Nothing at all is still a miss, not a refusal: the cache declined nothing
// because there was nothing to decline.
func TestLookup_NoCandidateAtAllIsAMiss(t *testing.T) {
	s, err := OpenDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutVec(Entry{Prompt: Normalize("rotate the signing key"), Response: "x"}, []float32{1, 0, 0}); err != nil {
		t.Fatal(err)
	}

	out := s.Lookup("what is the capital of France", []float32{0, 1, 0}, DefaultThreshold)
	if out.Found || out.Refused {
		t.Errorf("Found=%v Refused=%v, want a plain miss", out.Found, out.Refused)
	}
}

// A prompt carrying no content words at all must match nothing, or every such
// prompt would be the same question as every other one and the first entry in
// the store would answer all of them. sameQuestion is the single place that
// decides this; asserting it here proves the decision survives the path a
// caller actually takes.
func TestLookup_APromptOfPureFunctionWordsMatchesNothing(t *testing.T) {
	s, err := OpenDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutVec(Entry{Prompt: Normalize("rotate the signing key"), Response: "x"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"how do i", "what is it", "can you"} {
		if out := s.Lookup(q, nil, DefaultThreshold); out.Found {
			t.Errorf("%q was served %q", q, out.Hit.Response)
		}
	}
}

// What the floor is worth, measured rather than asserted: the same corpus and
// the same perturbations the cachebench harness uses, driven through the real
// Store.Lookup with no vectors at all. Needs no model, which is the point, so
// unlike the cosine half of that harness this runs in CI.
//
// Before #1015 every one of these was a miss: with no embedder there was no
// candidate, and the cache was an exact hash map.
//
// The contract asserted is exact rather than a rate: a restatement is served
// if and only if it leaves the content sequence alone. That matters because
// uppercasing does not always leave it alone. retrieve.Tokenize emits a
// camelCase identifier both whole and split, so "LiteLLM" is
// [litellm lite llm] and "LITELLM" is [litellm]; 31 of these 400 subjects
// carry such an identifier. Refusing those is right, the words really did
// change, and a flat "100% of restatements" would be measuring the wrong thing.
func TestLookup_TheFloorServesRestatementsWithNoEmbedder(t *testing.T) {
	raw, err := os.ReadFile("../retrieve/testdata/corpus.txt")
	if err != nil {
		t.Fatal(err)
	}
	var docs []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			docs = append(docs, Normalize(s))
		}
	}

	s, err := OpenDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.maxN = len(docs) + 1 // the corpus is the population here, not a working set
	for i, d := range docs {
		if err := s.Put(Entry{Prompt: d, Response: fmt.Sprintf("answer %d", i)}); err != nil {
			t.Fatal(err)
		}
	}

	for _, p := range []struct {
		name string
		fn   func(string) string
	}{
		{"case", strings.ToUpper},
		{"trailing punctuation", func(s string) string { return s + "?" }},
		{"whitespace", func(s string) string { return "  " + strings.ReplaceAll(s, " ", "   ") + "\n" }},
		{"filler words", func(s string) string { return "please can you " + s + " for me" }},
	} {
		served, unchanged := 0, 0
		for i, d := range docs {
			q := p.fn(d)
			keptWords := sameQuestion(content(Normalize(q)), content(d))
			if keptWords {
				unchanged++
			}
			out := s.Lookup(q, nil, DefaultThreshold)
			hit := out.Found && out.Hit.Response == fmt.Sprintf("answer %d", i)
			if hit {
				served++
			}
			if hit != keptWords {
				t.Errorf("%s: served=%v but the content sequence was kept=%v for %q", p.name, hit, keptWords, d)
			}
		}
		t.Logf("%-22s %d/%d served, %d kept every content word in order", p.name, served, len(docs), unchanged)
	}
}
