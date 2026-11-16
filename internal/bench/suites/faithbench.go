// SPDX-License-Identifier: MIT

package suites

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ankit373/hydra/internal/bench"
	"github.com/ankit373/hydra/internal/checkgen"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/ground"
	"github.com/ankit373/hydra/internal/util"
)

// faithBatches are the release files; upstream numbers them 1 to 16 and has no 13.
var faithBatches = []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "14", "15", "16"}

func faithFiles() []string {
	out := make([]string, len(faithBatches))
	for i, b := range faithBatches {
		out[i] = "batch_" + b + ".json"
	}
	return out
}

// faithbench is FaithBench's summaries cut into sentences by ground.Sentences, each
// labelled from the annotators' spans. Ids are batch_<n>/<sample>/<sentence>.
type faithbench struct {
	items map[string]faithItem
}

type faithItem struct{ source, sentence string }

type faithAnnotation struct {
	Label []string `json:"label"`
	Start *int     `json:"summary_start"`
	End   *int     `json:"summary_end"`
}

func (*faithbench) Name() string     { return "faithbench" }
func (*faithbench) Class() string    { return "claims.faithbench" }
func (*faithbench) Labels() []string { return nil }
func (*faithbench) Checks() []string { return []string{"ground"} }

// Rows keeps a sentence an Unwanted span overlaps as not_supported and one no span
// touches as supported. A sentence only Questionable or Benign spans touch is left out.
func (s *faithbench) Rows(ds bench.Dataset, dataDir string) ([]bench.Row, error) {
	if err := ds.RequireFiles(s.Name(), faithFiles()...); err != nil {
		return nil, err
	}
	s.items = map[string]faithItem{}
	var rows []bench.Row
	for _, name := range faithFiles() {
		raw, err := ds.Open(dataDir, name)
		if err != nil {
			return nil, err
		}
		var file struct {
			Samples []struct {
				ID          *int `json:"sample_id"`
				Source      string
				Summary     string
				Annotations []faithAnnotation
			}
		}
		if err := json.Unmarshal(raw, &file); err != nil {
			return nil, fmt.Errorf("suites: faithbench %s: %w", name, err)
		}
		w := width(len(file.Samples))
		for _, sm := range file.Samples {
			if sm.ID == nil || strings.TrimSpace(sm.Source) == "" {
				return nil, fmt.Errorf("suites: faithbench %s has a sample with no id or no source", name)
			}
			srcHash := sha256.Sum256([]byte(sm.Source))
			spans, err := sentenceSpans(sm.Summary)
			if err != nil {
				return nil, fmt.Errorf("suites: faithbench %s sample %d: %w", name, *sm.ID, err)
			}
			for n, sp := range spans {
				label, ok, err := faithLabel(sp.start, sp.end, sm.Annotations)
				if err != nil {
					return nil, fmt.Errorf("suites: faithbench %s sample %d: %w", name, *sm.ID, err)
				}
				if !ok {
					continue
				}
				id := fmt.Sprintf("%s/%0*d/%d", strings.TrimSuffix(name, ".json"), w, *sm.ID, n+1)
				if _, dup := s.items[id]; dup {
					return nil, fmt.Errorf("suites: faithbench has id %s twice", id)
				}
				s.items[id] = faithItem{source: sm.Source, sentence: sp.text}
				rows = append(rows, bench.Row{ID: id, Text: faithKey(srcHash, sp.text), Label: label})
			}
		}
	}
	return rows, nil
}

// faithKey is the text the split is keyed on: the sentence and its source's hash, so one
// sentence about two sources is two items. Combining marks are dropped; a few summaries
// spell an accent with one, and the key has no NFC.
func faithKey(srcHash [sha256.Size]byte, sentence string) string {
	noMarks := strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		return r
	}, sentence)
	return noMarks + "\n" + hex.EncodeToString(srcHash[:8])
}

// sentenceSpan is a sentence and where it sits in the summary, in runes, as the spans count.
type sentenceSpan struct {
	text       string
	start, end int
}

// sentenceSpans finds each of ground.Sentences in order; each is a trimmed run of the text.
func sentenceSpans(summary string) ([]sentenceSpan, error) {
	var out []sentenceSpan
	at := 0
	for _, s := range ground.Sentences(summary) {
		i := strings.Index(summary[at:], s)
		if i < 0 {
			return nil, fmt.Errorf("sentence %q is not in the summary", s)
		}
		start := utf8.RuneCountInString(summary[:at+i])
		out = append(out, sentenceSpan{text: s, start: start, end: start + utf8.RuneCountInString(s)})
		at += i + len(s)
	}
	return out, nil
}

// faithLabel applies the rule in bench/DATASETS.md to the span [start, end). ok is
// false for a sentence left out.
func faithLabel(start, end int, anns []faithAnnotation) (label string, ok bool, err error) {
	touched, unwanted := false, false
	for _, a := range anns {
		if a.Start == nil || a.End == nil || *a.Start > *a.End {
			return "", false, fmt.Errorf("an annotation has no summary span")
		}
		if *a.Start >= end || *a.End <= start {
			continue
		}
		touched = true
		for _, l := range a.Label {
			if l == "Unwanted" || strings.HasPrefix(l, "Unwanted.") {
				unwanted = true
			}
		}
	}
	switch {
	case unwanted:
		return checkgen.StatusNotSupported, true, nil
	case touched:
		return "", false, nil
	}
	return checkgen.StatusSupported, true, nil
}

func (s *faithbench) item(it bench.Item) (faithItem, error) {
	fi, ok := s.items[it.ID]
	if !ok {
		return faithItem{}, fmt.Errorf("suites: faithbench item %s is not loaded", it.ID)
	}
	return fi, nil
}

// Prompt is the claims check's judge question for one sentence, as a chat head gets it.
func (s *faithbench) Prompt(it bench.Item) (executor.Request, error) {
	fi, err := s.item(it)
	if err != nil {
		return executor.Request{}, err
	}
	system, prompt, err := checkgen.ClaimsJudgeChat(fi.source, fi.sentence)
	if err != nil {
		return executor.Request{}, err
	}
	zero := 0.0
	return executor.Request{System: system, Prompt: prompt, Temperature: &zero, MaxTokens: maxAnswerTokens}, nil
}

// Check is the claims check's ground variant on the sentence, against the fenced source.
// It answers with the sentence's status.
func (s *faithbench) Check(ctx context.Context, it bench.Item, check string) (string, error) {
	if check != "ground" {
		return "", fmt.Errorf("suites: faithbench has no check %q", check)
	}
	fi, err := s.item(it)
	if err != nil {
		return "", err
	}
	v, err := checkgen.GroundSentence(ctx, util.WrapUntrusted("SOURCE", fi.source), fi.sentence)
	return v.Status, err
}

// Grade reads a ground status or a judge's yes or no. not_supported and no flag the
// sentence. A reply that is neither is a format fail and flags nothing, as a check that did not run.
func (s *faithbench) Grade(_ context.Context, it bench.Item, answer string) (bench.Grade, error) {
	if _, err := s.item(it); err != nil {
		return bench.Grade{}, err
	}
	gold, err := it.Label()
	if err != nil {
		return bench.Grade{}, err
	}
	d := &bench.Detection{Positive: gold == checkgen.StatusNotSupported}
	g := bench.Grade{Detection: d}
	switch answer {
	case checkgen.StatusNotSupported:
		d.Flagged = true
	case checkgen.StatusSupported:
	case checkgen.StatusNothingToCheck:
		g.Detail = "nothing to check in the sentence"
	default:
		yes, ok := checkgen.YesNo(answer)
		if !ok {
			g.Format, g.Detail = true, fmt.Sprintf("not a yes or no: %.80q", answer)
			return g, nil
		}
		d.Flagged = !yes
	}
	g.Passed = d.Flagged == d.Positive
	if !g.Passed && g.Detail == "" {
		g.Detail = fmt.Sprintf("flagged %v, gold %s", d.Flagged, gold)
	}
	return g, nil
}
