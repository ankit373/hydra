// SPDX-License-Identifier: MIT

// Package bench is the benchmark harness: pinned datasets, committed splits, and
// the runner that scores arms on them. It never writes a user's evalset.
package bench

import (
	"context"
	"encoding/json"
	"fmt"

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
	Passed bool
	Format bool
	Detail string
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
