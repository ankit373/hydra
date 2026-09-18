// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
)

func probeHead(id, quant string, score int) provider.Head {
	return provider.Head{
		ID: id, Name: id, Provider: "local", Source: "port",
		CapScore: score, LocalOnly: true,
		Meta: map[string]string{"model_quant": quant},
	}
}

// headers renders the header row of a column set, split into fields.
func headers(cols []probeColumn) []string {
	return strings.Fields(probeRow(cols, func(c probeColumn) string { return c.head }))
}

// The Declared column is the answer to "why does this head rank here". It must
// not appear on a machine that has measured nothing, where it would be a
// column of blanks restating the Score beside it.
func TestProbeColumns_DeclaredAppearsOnlyBesideAnAdjustment(t *testing.T) {
	heads := []provider.Head{probeHead("ollama/a:7b", "", 66)}

	unmeasured := map[string]rank.Score{"ollama/a:7b": {Declared: 66, Effective: 66}}
	if got := headers(probeColumns(heads, unmeasured)); contains(got, "Declared") {
		t.Errorf("headers %v carry a Declared column with nothing adjusted", got)
	}

	measured := map[string]rank.Score{"ollama/a:7b": {Declared: 66, Effective: 74, N: 31}}
	if got := headers(probeColumns(heads, measured)); !contains(got, "Declared") {
		t.Errorf("headers %v hide the declared score a head was moved off", got)
	}
}

// Score is the number the head was ranked on. Rendering the declared one there
// would describe an order nobody got.
func TestProbeColumns_ScoreIsWhatTheHeadWasRankedOn(t *testing.T) {
	h := probeHead("ollama/a:7b", "", 66)
	scores := map[string]rank.Score{"ollama/a:7b": {Declared: 66, Effective: 74, N: 31}}
	cols := probeColumns([]provider.Head{h}, scores)

	row := probeRow(cols, func(c probeColumn) string { return c.cell(h) })
	fields := strings.Fields(row)
	if !contains(fields, "74") {
		t.Errorf("row %q does not carry the effective score 74", row)
	}
	if !strings.Contains(row, "66 (n=31)") {
		t.Errorf("row %q does not say what it was adjusted from, or on how much evidence", row)
	}
}

// An unadjusted head sharing a table with an adjusted one leaves the cell
// empty rather than repeating its own score, which would read as a change.
func TestProbeColumns_UnadjustedHeadLeavesTheDeclaredCellEmpty(t *testing.T) {
	adjusted := probeHead("ollama/a:7b", "", 66)
	plain := probeHead("ollama/b:7b", "", 70)
	scores := map[string]rank.Score{
		"ollama/a:7b": {Declared: 66, Effective: 74, N: 31},
		"ollama/b:7b": {Declared: 70, Effective: 70},
	}
	cols := probeColumns([]provider.Head{adjusted, plain}, scores)

	var declared probeColumn
	for _, c := range cols {
		if c.head == "Declared" {
			declared = c
		}
	}
	if declared.cell == nil {
		t.Fatal("no Declared column despite an adjustment")
	}
	if got := declared.cell(plain); got != "" {
		t.Errorf("unadjusted head renders %q, want an empty cell", got)
	}
}

// The header and every row are one column set, so they line up by construction.
// A drifting width is how the quant column pushed later columns right (#762).
func TestProbeRow_HeaderAndRowsShareTheSameWidths(t *testing.T) {
	heads := []provider.Head{
		probeHead("ollama/short:1b", "Q4_K_M", 40),
		probeHead("ollama/a-considerably-longer-model-name:70b", "Q8_0", 80),
		// Past the column's cap, which the two above are not: a llama.cpp head
		// is named after the file it serves and an Ollama model pulled from
		// HuggingFace carries its whole repo path, so the case this test exists
		// for was one the fixture could not reach (#913).
		probeHead("llamacpp/"+strings.Repeat("very-long-model-name-", 4), "", 55),
	}
	scores := map[string]rank.Score{
		heads[0].ID: {Declared: 40, Effective: 52, N: 25},
		heads[1].ID: {Declared: 80, Effective: 80},
		heads[2].ID: {Declared: 55, Effective: 55},
	}
	cols := probeColumns(heads, scores)

	// Counted in runes, not bytes: a cut cell ends in a one-rune ellipsis that
	// is three bytes, so a byte offset reads a correctly aligned row as two
	// columns adrift.
	want := columnAt(probeRow(cols, func(c probeColumn) string { return c.head }), "Src")
	for _, h := range heads {
		row := probeRow(cols, func(c probeColumn) string { return c.cell(h) })
		if got := columnAt(row, h.Source); got != want {
			t.Errorf("%s: Src column starts at %d, header puts it at %d", h.ID, got, want)
		}
	}
}

// columnAt is where sub begins, measured the way a terminal lays a line out.
func columnAt(row, sub string) int {
	i := strings.Index(row, sub)
	if i < 0 {
		return -1
	}
	return utf8.RuneCountInString(row[:i])
}

// The last column is unpadded, or every line ships a run of trailing spaces.
func TestProbeRow_LastColumnIsNotPadded(t *testing.T) {
	h := probeHead("ollama/a:7b", "", 66)
	cols := probeColumns([]provider.Head{h}, map[string]rank.Score{h.ID: {Declared: 66, Effective: 66}})

	row := probeRow(cols, func(c probeColumn) string { return c.cell(h) })
	if row != strings.TrimRight(row, " ") {
		t.Errorf("row %q ends in padding", row)
	}
}

// --dry-run has to say what the order was built on. A head nothing measured
// gets no annotation rather than a row of zeroes, which would read as an
// adjustment that did not happen.
func TestRoutingEvidence(t *testing.T) {
	h := probeHead("ollama/a:7b", "", 70)
	cases := []struct {
		name  string
		res   *dispatch.Result
		wants []string
		empty bool
	}{
		{
			name: "measured in this domain",
			res: &dispatch.Result{Domain: "go", Scores: map[string]rank.Score{
				h.ID: {Declared: 70, Effective: 88, N: 50, InDomain: 40},
			}},
			wants: []string{"88", "50", "40", "go"},
		},
		{
			name: "borrowed from other domains",
			res: &dispatch.Result{Domain: "rust", Scores: map[string]rank.Score{
				h.ID: {Declared: 70, Effective: 80, N: 50, InDomain: 0},
			}},
			wants: []string{"80", "50", "0", "rust"},
		},
		{
			name:  "nothing measured",
			res:   &dispatch.Result{Domain: "go", Scores: map[string]rank.Score{h.ID: {Declared: 70, Effective: 70}}},
			empty: true,
		},
		{name: "no domain narrowed the ranking", res: &dispatch.Result{}, empty: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := routingEvidence(tc.res, h)
			if tc.empty {
				if got != "" {
					t.Fatalf("got %q, want nothing", got)
				}
				return
			}
			for _, w := range tc.wants {
				if !strings.Contains(got, w) {
					t.Errorf("got %q, want it to carry %q", got, w)
				}
			}
		})
	}
}

// Widths are minimums, not the whole answer: "registry" is eight characters in
// a column declared five. Cutting it to "regi…" to keep the columns aligned
// would trade one wrong output for another, so a column fits its own values.
func TestProbeColumns_AColumnFitsItsOwnValues(t *testing.T) {
	h := provider.Head{ID: "agy/opus", Name: "Claude Opus", Provider: "antigravity", Source: "registry"}
	cols := probeColumns([]provider.Head{h}, map[string]rank.Score{})

	for _, c := range cols {
		if c.width == 0 {
			continue // the last column runs to the end of the line
		}
		if got := utf8.RuneCountInString(c.cell(h)); got > c.width {
			t.Errorf("column %q is %d wide but has to show %d characters (%q)",
				c.head, c.width, got, c.cell(h))
		}
		if got := utf8.RuneCountInString(c.head); got > c.width {
			t.Errorf("column %q is %d wide but its own header is %d characters", c.head, c.width, got)
		}
	}

	if row := probeRow(cols, func(c probeColumn) string { return c.cell(h) }); !strings.Contains(row, "registry") {
		t.Errorf("row cut a value that fits its column:\n%s", row)
	}
}
