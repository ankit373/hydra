// SPDX-License-Identifier: MIT

package ground

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/util"
)

func prompt(context string) string {
	return "Summarise this file.\n\n" + util.WrapUntrusted("CONTEXT", context)
}

const source = `// UITier keeps tier 10 as the free floor, local-only, and floors paid heads at 9.
func UITier(h provider.Head) int {
	if h.LocalOnly {
		return 10
	}
	return 9
}`

// Nothing fenced, nothing checked. The distinction the whole package rests on:
// "no context" and "checked and fine" are different facts.
func TestCheck_UnfencedPromptIsNotChecked(t *testing.T) {
	v := Check("UITier returns 10", "Summarise this file.\n\n"+source)
	if v.Checked {
		t.Error("an unfenced prompt produced a verdict")
	}
	if v.Grounded {
		t.Error("an unchecked verdict reported itself grounded")
	}
}

// The case this exists for: a number and a symbol the context never mentions.
func TestCheck_NamesTheInventedSpans(t *testing.T) {
	v := Check("UITier puts a LocalOnly head at tier 12, calling rank.EffectiveScoreIn to decide.", prompt(source))
	if !v.Checked || v.Grounded {
		t.Fatalf("checked=%v grounded=%v, want a checked failure", v.Checked, v.Grounded)
	}
	want := map[string]Kind{"12": KindNumber, "rank.EffectiveScoreIn": KindSymbol}
	got := map[string]Kind{}
	for _, c := range v.Unsupported {
		got[c.Text] = c.Kind
	}
	for text, kind := range want {
		if got[text] != kind {
			t.Errorf("%q reported as %q, want %q; got %+v", text, got[text], kind, v.Unsupported)
		}
	}
}

// An answer whose claims all appear in the context passes, or the check is
// only useful for refusing.
func TestCheck_GroundedAnswerPasses(t *testing.T) {
	v := Check("UITier puts a LocalOnly head at tier 10, the free floor, and paid heads at 9.", prompt(source))
	if !v.Grounded {
		t.Errorf("flagged a grounded answer: %+v", v.Unsupported)
	}
	if v.Claims == 0 {
		t.Error("no claims were counted, so nothing was actually verified")
	}
	if v.Context != len(source) {
		t.Errorf("checked against %d bytes, want the %d fenced", v.Context, len(source))
	}
}

// Prose is not a claim. A model is asked to explain, so it writes words the
// context never used, and requiring those to appear would fail every answer.
func TestCheck_ProseIsNotAClaim(t *testing.T) {
	answer := "This is a best-effort, top-level summary. PII handling is unaffected, " +
		"and there are 3 branches worth noting."
	v := Check(answer, prompt(source))
	if !v.Grounded {
		t.Errorf("ordinary prose was flagged: %+v", v.Unsupported)
	}
}

// Each of those three is a separate rule, and a test that only checks them
// together would pass with any one of them removed.
func TestClaimKind_SeparatesNamesFromWords(t *testing.T) {
	cases := []struct {
		tok       string
		kind      Kind
		checkable bool
	}{
		{"UITier", "", false}, // an acronym-led name has no lower→upper step
		{"LocalOnly", KindSymbol, true},
		{"rank.UITier", KindSymbol, true},
		{"internal/awsconf", KindSymbol, true},
		{"last_handoff.json", KindSymbol, true},
		{"--max-cost", KindSymbol, true},
		{"40960", KindNumber, true},
		{"10", KindNumber, true},
		{"3", "", false},           // a count in a sentence, not a measurement
		{"best-effort", "", false}, // a hyphen joins words in English
		{"highest-stakes", "", false},
		{"PII", "", false}, // an acronym, not camelCase
		{"summary", "", false},
		{"v1.3.1", KindSymbol, true},
	}
	for _, c := range cases {
		kind, ok := claimKind(c.tok)
		if ok != c.checkable || kind != c.kind {
			t.Errorf("claimKind(%q) = (%q, %v), want (%q, %v)", c.tok, kind, ok, c.kind, c.checkable)
		}
	}
}

// The reason this does not reuse retrieve.Tokenize: splitting on the slash
// would let internal/awsconf be supported by a context that says
// internal/executor, on the shared half. That is the failure #911 measured.
func TestCheck_ASharedPathPrefixIsNotSupport(t *testing.T) {
	v := Check("The fix is in internal/awsconf.", prompt("the bug was in internal/executor"))
	if v.Grounded {
		t.Error("internal/awsconf was supported by a context that only says internal/executor")
	}
}

// The reverse direction is support: a context naming rank.UITier does contain
// UITier, so an answer using the bare name is grounded.
func TestCheck_AQualifiedContextSupportsTheBareName(t *testing.T) {
	v := Check("UITier decides the tier.", prompt("see rank.UITier for the banding"))
	if !v.Grounded {
		t.Errorf("a bare name was not supported by its qualified form: %+v", v.Unsupported)
	}
}

// A #-prefixed number cites something outside the context by construction, so
// requiring it to appear inside asks the impossible. Measured: 50 of 89
// numeric false alarms on this repository's own doc comments.
func TestCheck_CitationsAreNotMeasurements(t *testing.T) {
	v := Check("The floor was introduced in #752 and refined by #815.", prompt(source))
	if !v.Grounded {
		t.Errorf("issue citations were treated as claims about the material: %+v", v.Unsupported)
	}
	// The same digits stated as a quantity are still a claim.
	if v := Check("The floor is 752 tokens.", prompt(source)); v.Grounded {
		t.Error("a bare number stated as a measurement was not checked")
	}
}

// Support is the whole prompt, not only the fenced part. A model repeating a
// flag the user named is grounded in what it was given.
func TestCheck_TheInstructionsAreAlsoSupport(t *testing.T) {
	p := "Does --max-cost apply here?\n\n" + util.WrapUntrusted("CONTEXT", "func f() {}")
	if v := Check("--max-cost does not appear in this function.", p); !v.Grounded {
		t.Errorf("a flag the question itself named was reported unsupported: %+v", v.Unsupported)
	}
}

// An answer that names and measures nothing has nothing to verify, and saying
// so is different from saying it checked out.
func TestCheck_CountsTheClaimsItChecked(t *testing.T) {
	v := Check("It does what the file says.", prompt(source))
	if !v.Checked {
		t.Fatal("a fenced prompt was not checked")
	}
	if v.Claims != 0 {
		t.Errorf("counted %d claims in an answer that states none", v.Claims)
	}
	if !v.Grounded {
		t.Error("an answer with no claims was reported ungrounded")
	}
}

// A repeated invention is named once, and the list is bounded: a wholly
// fabricated answer produces hundreds and a list that long is not a diagnostic.
func TestCheck_BoundsAndDeduplicatesTheReport(t *testing.T) {
	var b strings.Builder
	for i := range 40 {
		b.WriteString("pkg.Symbol")
		b.WriteByte(byte('A' + i%26))
		b.WriteString(" and pkg.SymbolA again. ")
	}
	v := Check(b.String(), prompt(source))
	if len(v.Unsupported) > MaxUnsupported {
		t.Errorf("reported %d spans, over the %d cap", len(v.Unsupported), MaxUnsupported)
	}
	seen := map[string]bool{}
	for _, c := range v.Unsupported {
		if seen[c.Text] {
			t.Errorf("%q reported twice", c.Text)
		}
		seen[c.Text] = true
	}
}

// The same answer must report the same list twice: the spans are collected
// through a map, and a reader comparing two runs would otherwise see a diff
// that is not a change.
func TestCheck_IsDeterministic(t *testing.T) {
	answer := "It calls pkg.Alpha, pkg.Beta, pkg.Gamma and pkg.Delta at tier 12 and 4096."
	first := Check(answer, prompt(source))
	for range 50 {
		got := Check(answer, prompt(source))
		if len(got.Unsupported) != len(first.Unsupported) {
			t.Fatalf("span count changed between calls: %d then %d", len(first.Unsupported), len(got.Unsupported))
		}
		for i := range got.Unsupported {
			if got.Unsupported[i] != first.Unsupported[i] {
				t.Fatalf("span %d changed: %+v then %+v", i, first.Unsupported[i], got.Unsupported[i])
			}
		}
	}
}

// Overlap is reported and deliberately not part of the verdict: a good summary
// in fresh words has low overlap, so gating on it would fail the answers worth
// having.
func TestCheck_OverlapIsReportedNotEnforced(t *testing.T) {
	v := Check("Entirely fresh wording describing behaviour in general terms.", prompt(source))
	if v.Overlap > 0.5 {
		t.Errorf("overlap %.2f, expected this rewording to score low", v.Overlap)
	}
	if !v.Grounded {
		t.Error("low overlap alone failed an answer that invents nothing")
	}
}
