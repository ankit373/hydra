// SPDX-License-Identifier: MIT

package api

import (
	"errors"
	"strings"

	"github.com/ankit373/hydra/internal/runlog"
)

// Edit is one file change a run made.
type Edit struct {
	File   string `json:"file"`
	TS     string `json:"ts"`
	Detail string `json:"detail,omitempty"`

	// Ref resolves to the stored before/after snapshot. Empty when the content
	// was refused for size or could not be written — the change still happened,
	// so the row still appears, but no diff can be shown for it.
	Ref string `json:"ref,omitempty"`

	Added   int `json:"added"`
	Removed int `json:"removed"`
}

// Diff is one edit rendered as unified hunks.
type Diff struct {
	File  string `json:"file"`
	Found bool   `json:"found"`
	// Reason says why a diff is unavailable, so the view can be specific rather
	// than blank: pruned snapshot, oversized file, or a read failure.
	Reason string `json:"reason,omitempty"`

	Lines []DiffLine `json:"lines"`

	Added   int `json:"added"`
	Removed int `json:"removed"`
}

// DiffLine is one rendered row. Op is "+", "-", or " ".
type DiffLine struct {
	Op      string `json:"op"`
	Text    string `json:"text"`
	OldLine int    `json:"oldLine"` // 0 when the line is an addition
	NewLine int    `json:"newLine"` // 0 when the line is a removal

	// Spans are the byte ranges on this line that actually changed, set only
	// for a 1:1 replacement. Empty means "no intra-line detail" — either the
	// line was added or removed outright, or it belongs to a multi-line block
	// where pairing removed lines to added ones would invent a relationship
	// the diff never established.
	Spans []Span `json:"spans,omitempty"`
}

// Span is a byte range within DiffLine.Text. Bytes, not runes: Text crosses
// the bridge as a JSON string and the view slices it the same way.
type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// GetEdits lists the edits a run applied, newest last.
func (a *API) GetEdits(runID string) ([]Edit, error) {
	out := []Edit{}
	if runID == "" {
		return out, nil
	}
	events, err := runlog.Load(runID)
	if err != nil {
		return out, nil // a run with no log has no edits; Session reports the error
	}
	for _, e := range events {
		if e.Kind != runlog.KindEdit {
			continue
		}
		added, removed := parseCounts(e.Detail)
		out = append(out, Edit{
			File: e.File, TS: e.TS, Detail: e.Detail, Ref: e.Ref,
			Added: added, Removed: removed,
		})
	}
	return out, nil
}

// GetDiff renders one edit as unified hunks.
func (a *API) GetDiff(runID, ref, file string) (*Diff, error) {
	d := &Diff{File: file, Lines: []DiffLine{}}
	if ref == "" {
		d.Reason = "no snapshot was stored for this edit"
		return d, nil
	}

	before, after, err := runlog.LoadEdit(runID, ref)
	if err != nil {
		switch {
		case errors.Is(err, runlog.ErrNoSnapshot):
			d.Reason = "snapshot is no longer on disk"
		default:
			d.Reason = err.Error()
		}
		return d, nil
	}

	d.Found = true
	d.Lines = diffLines(splitLines(string(before)), splitLines(string(after)))
	markPairs(d.Lines)
	for _, l := range d.Lines {
		switch l.Op {
		case "+":
			d.Added++
		case "-":
			d.Removed++
		}
	}
	return d, nil
}

// splitLines splits content into lines without inventing a trailing empty one
// for the final newline — a file ending in "\n" has as many lines as newlines,
// and a phantom last line renders as a spurious change.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

// maxLCSCells bounds the dynamic program below. The LCS table is O(n·m), and
// MaxSnapshotBytes permits ~65k lines, so an unbounded table would be 4.2
// billion cells — tens of gigabytes and effectively a hang. Trimming the common
// prefix and suffix first collapses almost every real edit well under this;
// what survives is a file rewritten wholesale, which is rendered as one
// replacement rather than a line-by-line alignment nobody would read anyway.
const maxLCSCells = 4 << 20 // ~2000x2000 lines

// diffLines produces a line-level diff.
//
// Hand-rolled rather than pulled in: it is short, and the alternative is a
// dependency in a module whose whole point is to stay separable from hyctl.
//
// Common prefix and suffix are trimmed before any alignment work. Edits change
// a few lines in the middle of a file, so this is what keeps the quadratic core
// small in practice rather than the size cap, which bounds bytes and not
// similarity.
func diffLines(a, b []string) []DiffLine {
	out := []DiffLine{}

	// Shared head.
	head := 0
	for head < len(a) && head < len(b) && a[head] == b[head] {
		out = append(out, DiffLine{Op: " ", Text: a[head], OldLine: head + 1, NewLine: head + 1})
		head++
	}

	// Shared tail, stopping before the head so a line is never emitted twice.
	tail := 0
	for tail < len(a)-head && tail < len(b)-head && a[len(a)-1-tail] == b[len(b)-1-tail] {
		tail++
	}

	midA, midB := a[head:len(a)-tail], b[head:len(b)-tail]
	out = append(out, alignMiddle(midA, midB, head)...)

	for i := range tail {
		oldIdx, newIdx := len(a)-tail+i, len(b)-tail+i
		out = append(out, DiffLine{Op: " ", Text: a[oldIdx], OldLine: oldIdx + 1, NewLine: newIdx + 1})
	}
	return out
}

// alignMiddle diffs the differing region. offset is how many lines were trimmed
// from the head, so reported line numbers stay those of the real file.
func alignMiddle(a, b []string, offset int) []DiffLine {
	out := []DiffLine{}
	n, m := len(a), len(b)
	if n == 0 && m == 0 {
		return out
	}

	// Too large to align line by line: report it as a wholesale replacement,
	// which is both honest and what the content actually is.
	if n*m > maxLCSCells {
		for i, line := range a {
			out = append(out, DiffLine{Op: "-", Text: line, OldLine: offset + i + 1})
		}
		for j, line := range b {
			out = append(out, DiffLine{Op: "+", Text: line, NewLine: offset + j + 1})
		}
		return out
	}

	// lcs[i][j] = length of the longest common subsequence of a[i:] and b[j:].
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}

	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, DiffLine{Op: " ", Text: a[i], OldLine: offset + i + 1, NewLine: offset + j + 1})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, DiffLine{Op: "-", Text: a[i], OldLine: offset + i + 1})
			i++
		default:
			out = append(out, DiffLine{Op: "+", Text: b[j], NewLine: offset + j + 1})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, DiffLine{Op: "-", Text: a[i], OldLine: offset + i + 1})
	}
	for ; j < m; j++ {
		out = append(out, DiffLine{Op: "+", Text: b[j], NewLine: offset + j + 1})
	}
	return out
}

// parseCounts reads the "+N/-M" prefix editor writes into the event detail.
// A detail it cannot parse yields zeros rather than a guess.
func parseCounts(detail string) (added, removed int) {
	head, _, _ := strings.Cut(detail, " ")
	plus, minus, ok := strings.Cut(head, "/")
	if !ok {
		return 0, 0
	}
	return atoiAfter(plus, '+'), atoiAfter(minus, '-')
}

func atoiAfter(s string, prefix byte) int {
	if len(s) < 2 || s[0] != prefix {
		return 0
	}
	n := 0
	for _, c := range s[1:] {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
