// SPDX-License-Identifier: MIT

package bench

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/ankit373/hydra/internal/oracle"
)

// calibrationBuckets of 1000 go to calibration: the scope's 30/70 split.
const calibrationBuckets = 300

const splitsHeader = "id\tsplit\trank\tlabel"

// SplitRow is one line of bench/splits/<suite>.tsv.
type SplitRow struct {
	ID    string
	Split Split
	Rank  uint64
	Label string
}

func hash64(parts ...string) uint64 {
	s := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return binary.BigEndian.Uint64(s[:8])
}

// splitText keys an utterance for leakage, not grading: the label normaliser keeps
// punctuation that labels need, so "what's my balance?" and "whats my balance" differed.
func splitText(text string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(text) {
		switch {
		case strings.ContainsRune("'’ʼ`", r): // "what's" is "whats"
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default: // punctuation, symbols, any Unicode space: a word break
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// normalisable refuses what splitText cannot key without NFC (golang.org/x/text): a
// combining mark on a Latin, Greek or Cyrillic letter, a fullwidth form, a ligature.
// Devanagari, Thai or Arabic marks are spelling, not variants, and load.
func normalisable(suite string, rows []Row) error {
	var bad []string
	for _, r := range rows {
		if needsNormalising(r.Text) {
			bad = append(bad, r.ID)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	shown := bad[:min(len(bad), 10)]
	return fmt.Errorf("bench: %s has %d items needing Unicode normalisation (combining marks on Latin, Greek or Cyrillic, fullwidth forms or ligatures): %s",
		suite, len(bad), strings.Join(shown, ", "))
}

func needsNormalising(text string) bool {
	var base rune // the letter a run of combining marks sits on
	for _, c := range text {
		switch {
		case (c >= 0xFF00 && c <= 0xFFEF) || (c >= 0xFB00 && c <= 0xFB4F):
			return true
		case unicode.Is(unicode.Mn, c):
			if unicode.In(base, unicode.Latin, unicode.Greek, unicode.Cyrillic) {
				return true
			}
		default:
			base = c
		}
	}
	return false
}

// splitOf keys on the text, not the id, so a duplicate cannot leak from calibration into test.
func splitOf(suite, text string) Split {
	if hash64("split", suite, splitText(text))%1000 < calibrationBuckets {
		return SplitCalibration
	}
	return SplitTest
}

// Rank orders items for every tier, keyed on the id alone so it is recomputable from the TSV.
func Rank(suite, id string) uint64 { return hash64("rank", suite, id) }

// AssignSplits computes the committed split for every row. An utterance whose
// copies carry different gold labels is label noise no arm can score: excluded.
func AssignSplits(suite string, rows []Row) ([]SplitRow, error) {
	labels := map[string]map[string]bool{}
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.ID] {
			return nil, fmt.Errorf("bench: %s has id %s twice", suite, r.ID)
		}
		seen[r.ID] = true
		c := splitText(r.Text)
		if labels[c] == nil {
			labels[c] = map[string]bool{}
		}
		labels[c][oracle.NormaliseLabel(r.Label)] = true
	}
	out := make([]SplitRow, len(rows))
	for i, r := range rows {
		s := splitOf(suite, r.Text)
		if len(labels[splitText(r.Text)]) > 1 {
			s = SplitExcluded
		}
		out[i] = SplitRow{ID: r.ID, Split: s, Rank: Rank(suite, r.ID), Label: r.Label}
	}
	return out, nil
}

// WriteSplits writes the TSV in the rows' own order, which is the upstream file's.
func WriteSplits(w io.Writer, rows []SplitRow) error {
	bw := bufio.NewWriter(w)
	fmt.Fprintln(bw, splitsHeader)
	for _, r := range rows {
		if strings.ContainsAny(r.ID+r.Label, "\t\n") {
			return fmt.Errorf("bench: %s cannot be written as one TSV line", r.ID)
		}
		fmt.Fprintf(bw, "%s\t%s\t%d\t%s\n", r.ID, r.Split, r.Rank, r.Label)
	}
	return bw.Flush()
}

// ReadSplits parses a committed TSV, refusing anything it would have to guess at.
func ReadSplits(r io.Reader) ([]SplitRow, error) {
	sc := bufio.NewScanner(r)
	if !sc.Scan() || sc.Text() != splitsHeader {
		return nil, fmt.Errorf("bench: a splits file starts with %q", splitsHeader)
	}
	var out []SplitRow
	for line := 2; sc.Scan(); line++ {
		f := strings.Split(sc.Text(), "\t")
		if len(f) != 4 {
			return nil, fmt.Errorf("bench: splits line %d has %d fields, want 4", line, len(f))
		}
		s, err := ParseSplit(f[1])
		if err != nil {
			return nil, fmt.Errorf("bench: splits line %d: %w", line, err)
		}
		rank, err := strconv.ParseUint(f[2], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("bench: splits line %d: %w", line, err)
		}
		out = append(out, SplitRow{ID: f[0], Split: s, Rank: rank, Label: f[3]})
	}
	return out, sc.Err()
}

// Load parses a suite's pinned files and joins them to its committed splits. Every
// data id must be in the TSV and every TSV id in the data, with the same label.
func Load(s Suite, ds Dataset, dataDir, splitsPath string) ([]Item, error) {
	rows, err := s.Rows(ds, dataDir)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(splitsPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	splits, err := ReadSplits(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", splitsPath, err)
	}
	if len(splits) != len(rows) {
		return nil, fmt.Errorf("bench: %s has %d rows, %s's data has %d", splitsPath, len(splits), s.Name(), len(rows))
	}
	byID := make(map[string]SplitRow, len(splits))
	for _, r := range splits {
		byID[r.ID] = r
	}
	if err := normalisable(s.Name(), rows); err != nil {
		return nil, err
	}
	// The TSV is checked against the data, not trusted: a hand-edited split, rank or
	// label, or two copies of one utterance in different splits, is refused here.
	want, err := AssignSplits(s.Name(), rows)
	if err != nil {
		return nil, err
	}
	items := make([]Item, len(rows))
	for i, r := range rows {
		sr, ok := byID[r.ID]
		if !ok {
			return nil, fmt.Errorf("bench: %s id %s is missing from %s", s.Name(), r.ID, splitsPath)
		}
		if sr != want[i] {
			return nil, fmt.Errorf("bench: %s line for %s is %v/%d/%q, the data gives %v/%d/%q",
				splitsPath, r.ID, sr.Split, sr.Rank, sr.Label, want[i].Split, want[i].Rank, want[i].Label)
		}
		in, _ := json.Marshal(struct {
			Text string `json:"text"`
		}{r.Text})
		gold, _ := json.Marshal(struct {
			Label string `json:"label"`
		}{r.Label})
		items[i] = Item{ID: r.ID, Class: s.Class(), Split: sr.Split, Rank: sr.Rank, Input: in, Gold: gold}
	}
	return items, nil
}

// FirstByRank returns the first n items of one split in rank order: tiers nest,
// so smoke's items are the first of full's, and the seed never chooses them.
func FirstByRank(items []Item, split Split, n int) []Item {
	var out []Item
	for _, it := range items {
		if it.Split == split {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rank != out[j].Rank {
			return out[i].Rank < out[j].Rank
		}
		return out[i].ID < out[j].ID
	})
	if n < len(out) {
		out = out[:n]
	}
	return out
}
