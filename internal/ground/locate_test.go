// SPDX-License-Identifier: MIT

package ground

import (
	"testing"

	"github.com/ankit373/hydra/internal/util"
)

var salesReport = "Summarise the report.\n" + util.WrapUntrusted("REPORT",
	"Quarterly sales\nUnits sold: 1,200\nGrowth was 1.9 percent.\nThe review is due on 3 March 2026.\nPriya Raman signed it.")

func TestLocate_NumberFoundOnItsLine(t *testing.T) {
	got := NewIndex(salesReport).Locate(Claim{Text: "1.9", Kind: KindNumber})
	if len(got) != 1 || got[0].Label != "REPORT" || got[0].StartLine != 3 || got[0].EndLine != 3 {
		t.Fatalf("spans = %+v, want REPORT line 3", got)
	}
	content := util.Unwrap(salesReport)[0].Content
	if s := got[0]; content[s.Start:s.End] != "1.9" {
		t.Errorf("offsets %d-%d hold %q, want the number", s.Start, s.End, content[s.Start:s.End])
	}
}

func TestLocate_DateWrittenAnotherWay(t *testing.T) {
	for _, text := range []string{"March 3, 2026", "2026-03-03", "March 3"} {
		got := NewIndex(salesReport).Locate(Claim{Text: text, Kind: KindDate})
		if len(got) != 1 || got[0].StartLine != 4 {
			t.Errorf("%q: spans = %+v, want REPORT line 4", text, got)
		}
	}
}

func TestLocate_NameQuoteAndRequestText(t *testing.T) {
	x := NewIndex(salesReport)
	if got := x.Locate(Claim{Text: "Priya Raman", Kind: KindName}); len(got) != 1 || got[0].StartLine != 5 {
		t.Errorf("name spans = %+v, want line 5", got)
	}
	if got := x.Locate(Claim{Text: "growth   WAS 1.9 percent", Kind: KindQuote}); len(got) != 1 || got[0].StartLine != 3 {
		t.Errorf("quote spans = %+v, want line 3", got)
	}
	if got := x.Locate(Claim{Text: "Summarise", Kind: KindSymbol}); len(got) != 1 || got[0].Label != RequestLabel || got[0].StartLine != 1 {
		t.Errorf("request spans = %+v, want the request's line 1", got)
	}
	if got := x.Locate(Claim{Text: "450", Kind: KindNumber}); got != nil {
		t.Errorf("a number nowhere in the source located at %+v", got)
	}
}

func TestMergeSpans_TouchingLinesJoin(t *testing.T) {
	got := MergeSpans([]Span{
		{Label: "R", StartLine: 4, EndLine: 4, Start: 40, End: 44},
		{Label: "R", StartLine: 2, EndLine: 3, Start: 10, End: 30},
		{Label: "R", StartLine: 9, EndLine: 9, Start: 90, End: 95},
		{Label: "R", StartLine: 2, EndLine: 2, Start: 5, End: 8, block: 1},
	})
	if len(got) != 3 || got[0].StartLine != 2 || got[0].EndLine != 4 || got[0].Start != 10 || got[0].End != 44 ||
		got[1].StartLine != 9 || got[2].block != 1 {
		t.Fatalf("merged = %+v", got)
	}
}

// Every claim Check accepts against a prose passage is located there.
func TestLocate_EveryAcceptedClaimIsLocated(t *testing.T) {
	located := 0
	for _, p := range loadProse(t) {
		prompt := "Summarise this article.\n" + util.WrapUntrusted("ARTICLE", p.Context)
		x := NewIndex(prompt)
		for i, a := range p.Answers {
			for _, c := range Check(a.Answer, prompt).Supported {
				if len(x.Locate(c)) == 0 {
					t.Errorf("%s[%d]: accepted %s %q has no span", p.ID, i, c.Kind, c.Text)
				}
				located++
			}
		}
	}
	if located < 100 {
		t.Fatalf("located %d claims, want the set to exercise at least 100", located)
	}
}
