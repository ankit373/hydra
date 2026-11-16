// SPDX-License-Identifier: MIT

// Package bench is the benchmark harness: pinned datasets, committed splits, and
// the runner that scores arms on them. It never writes a user's evalset.
package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/ankit373/hydra/internal/executor"
)

// Split is fixed by bench/splits/<suite>.tsv, never computed at run time.
type Split uint8

const (
	SplitCalibration Split = iota + 1
	SplitTest
	SplitExcluded // in the upstream file, scored by nobody (conflicting duplicate)
)

var splitNames = map[Split]string{SplitCalibration: "calibration", SplitTest: "test", SplitExcluded: "excluded"}

func (s Split) String() string { return splitNames[s] }

// ParseSplit reads the TSV spelling back.
func ParseSplit(name string) (Split, error) {
	for s, n := range splitNames {
		if n == name {
			return s, nil
		}
	}
	return 0, fmt.Errorf("bench: unknown split %q", name)
}

// Item is one benchmark question. Input and Gold stay raw so an extraction suite
// needs no change here.
type Item struct {
	ID    string          // "<upstream-file>/<row>", e.g. "test/0412"
	Class string          // E1 task class, e.g. "classification.banking77"
	Split Split           //
	Rank  uint64          // see Rank; tiers take a prefix
	Input json.RawMessage // {"text": "..."}
	Gold  json.RawMessage // {"label": "card_arrival"}
}

// Row is one upstream record as a suite parses it, before the split is joined.
type Row struct {
	ID, Text, Label string
}

// Grade is a suite's judgement of one answer. Format means the answer is not in
// the label set at all, counted apart so verbosity cannot hide as wrongness.
type Grade struct {
	Passed  bool
	Format  bool
	Timeout bool // a fail that never finished; counted apart, since load causes it
	Detail  string
	// Detection is set by a suite that grades a check: recall and false flags count from it.
	Detection *Detection
	Fields    map[string]FieldTally // an extraction suite's field counts by FieldKind; nil elsewhere
}

// Detection is one item of a check suite: whether the gold has something to catch
// (Positive) and whether the answer flagged the item.
type Detection struct {
	Positive bool `json:"positive"`
	Flagged  bool `json:"flagged"`
}

// FieldTally counts one answer's fields, right or wrong against the gold, and how many
// of each the checks flagged, that is, did not mark verified.
type FieldTally struct {
	Right        int `json:"right"`
	Wrong        int `json:"wrong"`
	RightFlagged int `json:"right_flagged"`
	WrongFlagged int `json:"wrong_flagged"`
}

func (t *FieldTally) add(o FieldTally) {
	t.Right, t.Wrong = t.Right+o.Right, t.Wrong+o.Wrong
	t.RightFlagged, t.WrongFlagged = t.RightFlagged+o.RightFlagged, t.WrongFlagged+o.WrongFlagged
}

// The kinds FieldKind sorts a value into, so the share caught shows apart for amounts,
// for identifiers and codes, which two models can misread the same way, and for text.
const (
	KindNumber = "number"
	KindCode   = "code"
	KindText   = "text"
)

var (
	numberValue = regexp.MustCompile(`^[-+]?[$€£]?\d[\d,]*(\.\d+)?%?$`)
	codeValue   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9./_-]*$`)
	capsValue   = regexp.MustCompile(`^[A-Z][A-Z0-9./_-]+$`)
)

// FieldKind is number for an amount, code for one token holding a digit or written in
// capitals (HLF-1O, INV-2041, USD), and text for the rest.
func FieldKind(v string) string {
	v = strings.TrimSpace(v)
	switch {
	case numberValue.MatchString(v):
		return KindNumber
	case codeValue.MatchString(v) && strings.ContainsAny(v, "0123456789"), capsValue.MatchString(v):
		return KindCode
	}
	return KindText
}

// Suite is one dataset with one verifier. Prompt is identical for every arm.
type Suite interface {
	Name() string  // "banking77"
	Class() string // "classification.banking77"
	// Rows verifies and parses the pinned files; Labels is valid once it returns.
	Rows(ds Dataset, dataDir string) ([]Row, error)
	Labels() []string
	Prompt(it Item) (executor.Request, error)
	Grade(ctx context.Context, it Item, answer string) (Grade, error)
}

// Checker is a suite with checks of its own that answer an item with no model. Each
// name in Checks can be an arm of kind "check", which costs nothing.
type Checker interface {
	Checks() []string
	Check(ctx context.Context, it Item, check string) (string, error)
}

// Text and Label read an Item's raw fields back for a label suite.
func (it Item) Text() (string, error) {
	var in struct{ Text string }
	if err := json.Unmarshal(it.Input, &in); err != nil {
		return "", fmt.Errorf("bench: item %s input: %w", it.ID, err)
	}
	return in.Text, nil
}

func (it Item) Label() (string, error) {
	var g struct{ Label string }
	if err := json.Unmarshal(it.Gold, &g); err != nil {
		return "", fmt.Errorf("bench: item %s gold: %w", it.ID, err)
	}
	return g.Label, nil
}
