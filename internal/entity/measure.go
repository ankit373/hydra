// SPDX-License-Identifier: MIT

package entity

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
)

//go:embed probeset.jsonl
var probeset []byte

// Probe is one labelled text. Positives are benchmark texts carrying a name or
// a street address and nothing a pattern can find; negatives are this repo's
// own commit subjects and technical prose naming people as the authors of
// results, which is the case a pattern can never get right.
type Probe struct {
	Text string `json:"text"`
	PII  bool   `json:"pii"`
}

// Probes returns the embedded probe set.
func Probes() ([]Probe, error) {
	var out []Probe
	sc := bufio.NewScanner(bytes.NewReader(probeset))
	sc.Buffer(make([]byte, 0, 1<<16), 1<<20)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var p Probe
		if err := json.Unmarshal(sc.Bytes(), &p); err != nil {
			return nil, fmt.Errorf("entity: probe set: %w", err)
		}
		out = append(out, p)
	}
	return out, sc.Err()
}

// Report is what one head measured against the probe set.
type Report struct {
	Positives, Recalled int
	Negatives, FalsePos int
	Unreadable          int // answers that were neither yes nor no
	Failed              int // the head could not be reached
}

func (r Report) Recall() float64 {
	if r.Positives == 0 {
		return 0
	}
	return float64(r.Recalled) / float64(r.Positives)
}

func (r Report) FalsePositiveRate() float64 {
	if r.Negatives == 0 {
		return 0
	}
	return float64(r.FalsePos) / float64(r.Negatives)
}

// MinRecall and MaxFalsePositive are where a head becomes worth asking, placed
// between what three heads measured on this probe set rather than chosen:
// Qwen2.5-Coder:7b 0.88 recall at 0.00, qwen3:0.6b 0.55 at 0.12, and
// Qwen2.5-0.5B 0.88 at 0.85, which says yes to almost every negative. The
// passing head still misses 12%, and that is the ceiling of the approach
// rather than a rounding error (#1039).
const (
	MinRecall        = 0.80
	MaxFalsePositive = 0.10
)

// Eligible reports whether this head may be asked in earnest.
//
// Both halves are required, because each alone is trivially passed: a head that
// answers NO to everything scores a flawless false-positive rate, and one that
// answers YES to everything scores perfect recall.
func (r Report) Eligible() bool {
	return r.Positives > 0 && r.Negatives > 0 &&
		r.Recall() >= MinRecall && r.FalsePositiveRate() <= MaxFalsePositive
}

// Measure runs every probe past one head.
//
// An unreadable answer counts against nothing and is reported on its own, since
// it is neither a detection nor a miss; a head that produces many of them fails
// the recall bar on its own without that needing a second rule.
func Measure(ctx context.Context, ask Ask) (Report, error) {
	probes, err := Probes()
	if err != nil {
		return Report{}, err
	}
	var r Report
	for _, p := range probes {
		if err := ctx.Err(); err != nil {
			return r, err
		}
		if p.PII {
			r.Positives++
		} else {
			r.Negatives++
		}
		v, err := Check(ctx, p.Text, ask)
		switch {
		case errors.Is(err, ErrUnreadable):
			r.Unreadable++
			continue
		case err != nil:
			r.Failed++
			continue
		}
		if v.Found && p.PII {
			r.Recalled++
		}
		if v.Found && !p.PII {
			r.FalsePos++
		}
	}
	return r, nil
}
