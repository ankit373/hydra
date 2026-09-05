// SPDX-License-Identifier: MIT

// Package diff produces unified diffs in-process.
//
// Hydra used to shell out to diff(1) for this. That binary does not exist on a
// stock Windows install, and every call site discarded the error, so the diff
// came back as an empty string with a nil error, which is indistinguishable
// from "this file has no changes". Three of the four call sites then *counted*
// lines in that output to report added/removed, so a modified file was reported
// as 0/0 and a reviewer approved changes they were never shown (#260).
//
// Doing it here removes the host-binary dependency on every platform, not just
// Windows, and makes "no changes" and "could not diff" different outcomes.
package diff

import (
	"fmt"
	"strings"
)

// ContextLines is how many unchanged lines surround each hunk, matching
// `diff -u`'s default so output stays familiar.
const ContextLines = 3

// maxCells caps the LCS table. Beyond it the quadratic table would cost more
// memory than a review diff is worth, so the file is reported as wholly
// replaced, honest, and still correct as a diff, just not minimal.
const maxCells = 4 << 20

type opKind int

const (
	opEqual opKind = iota
	opDelete
	opInsert
)

type op struct {
	kind opKind
	line string
}

// Unified returns a unified diff of old → new. An empty result means the two
// are identical, callers must treat that as "no changes", which is now a
// distinct outcome from an error.
func Unified(oldName, newName string, old, new []byte) string {
	ops := diffOps(splitLines(string(old)), splitLines(string(new)))
	hunks := group(ops)
	if len(hunks) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n", oldName)
	fmt.Fprintf(&b, "+++ %s\n", newName)
	for _, h := range hunks {
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", h.oldStart, h.oldLines, h.newStart, h.newLines)
		for _, o := range h.ops {
			switch o.kind {
			case opEqual:
				b.WriteString(" " + o.line + "\n")
			case opDelete:
				b.WriteString("-" + o.line + "\n")
			case opInsert:
				b.WriteString("+" + o.line + "\n")
			}
		}
	}
	return b.String()
}

// Stats counts changed lines. It is derived from the same ops as Unified, so
// the two can never disagree, the previous implementations re-parsed diff(1)'s
// text in three separate places and could.
func Stats(old, new []byte) (added, removed int) {
	for _, o := range diffOps(splitLines(string(old)), splitLines(string(new))) {
		switch o.kind {
		case opInsert:
			added++
		case opDelete:
			removed++
		}
	}
	return added, removed
}

// splitLines splits on \n and drops the trailing empty element a final newline
// produces, so a file ending in a newline is not treated as having a blank last
// line. \r\n is normalised so a CRLF file does not diff as entirely changed
// against an LF one.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// diffOps returns the edit script. Common prefix and suffix are trimmed first:
// a small edit in a large file is the normal case, and trimming keeps the
// quadratic table off all the lines that did not change.
func diffOps(a, b []string) []op {
	var ops []op

	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}

	for _, l := range a[:pre] {
		ops = append(ops, op{opEqual, l})
	}
	ops = append(ops, middle(a[pre:len(a)-suf], b[pre:len(b)-suf])...)
	for _, l := range a[len(a)-suf:] {
		ops = append(ops, op{opEqual, l})
	}
	return ops
}

// middle diffs the parts that differ, via an LCS table.
func middle(a, b []string) []op {
	switch {
	case len(a) == 0 && len(b) == 0:
		return nil
	case len(a) == 0:
		return allOf(opInsert, b)
	case len(b) == 0:
		return allOf(opDelete, a)
	case len(a)*len(b) > maxCells:
		// Too large to diff minimally; report a wholesale replacement rather
		// than allocating a table proportional to the product of the sizes.
		return append(allOf(opDelete, a), allOf(opInsert, b)...)
	}

	// lcs[i][j] = length of the longest common subsequence of a[i:] and b[j:].
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	var ops []op
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			ops = append(ops, op{opEqual, a[i]})
			i, j = i+1, j+1
		case lcs[i+1][j] >= lcs[i][j+1]:
			// Deletions before insertions at the same position, so a modified
			// line renders as -old then +new, as diff(1) does.
			ops = append(ops, op{opDelete, a[i]})
			i++
		default:
			ops = append(ops, op{opInsert, b[j]})
			j++
		}
	}
	ops = append(ops, allOf(opDelete, a[i:])...)
	return append(ops, allOf(opInsert, b[j:])...)
}

func allOf(k opKind, lines []string) []op {
	out := make([]op, 0, len(lines))
	for _, l := range lines {
		out = append(out, op{k, l})
	}
	return out
}

type hunk struct {
	oldStart, oldLines int
	newStart, newLines int
	ops                []op
}

// group turns a flat edit script into unified-diff hunks: each run of changes
// plus ContextLines of surrounding equal lines, with adjacent runs merged when
// their context overlaps.
func group(ops []op) []hunk {
	changed := make([]int, 0, len(ops))
	for i, o := range ops {
		if o.kind != opEqual {
			changed = append(changed, i)
		}
	}
	if len(changed) == 0 {
		return nil
	}

	// Merge change indices into ranges that share context.
	type span struct{ lo, hi int }
	var spans []span
	cur := span{changed[0], changed[0]}
	for _, idx := range changed[1:] {
		if idx-cur.hi <= 2*ContextLines+1 {
			cur.hi = idx
			continue
		}
		spans = append(spans, cur)
		cur = span{idx, idx}
	}
	spans = append(spans, cur)

	// Line numbers are 1-based and counted separately per side.
	oldNo, newNo := 1, 1
	pos := 0
	var out []hunk
	for _, sp := range spans {
		lo := max(0, sp.lo-ContextLines)
		hi := min(len(ops)-1, sp.hi+ContextLines)

		for ; pos < lo; pos++ {
			switch ops[pos].kind {
			case opEqual:
				oldNo, newNo = oldNo+1, newNo+1
			case opDelete:
				oldNo++
			case opInsert:
				newNo++
			}
		}

		h := hunk{oldStart: oldNo, newStart: newNo}
		for ; pos <= hi; pos++ {
			h.ops = append(h.ops, ops[pos])
			switch ops[pos].kind {
			case opEqual:
				h.oldLines, h.newLines = h.oldLines+1, h.newLines+1
				oldNo, newNo = oldNo+1, newNo+1
			case opDelete:
				h.oldLines++
				oldNo++
			case opInsert:
				h.newLines++
				newNo++
			}
		}
		// An empty side is rendered as start 0 by diff(1); match that so the
		// header of a pure-insert or pure-delete hunk is not misleading.
		if h.oldLines == 0 {
			h.oldStart = 0
		}
		if h.newLines == 0 {
			h.newStart = 0
		}
		out = append(out, h)
	}
	return out
}
