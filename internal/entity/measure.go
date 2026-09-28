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
	"math"
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
// between what heads measured on this probe set rather than chosen: a 7B scored
// around 0.8 recall at near-zero false positives, while a 0.5B scored similar
// recall and said yes to three quarters of the negatives.
//
// The bar is compared against the interval's lower bound, not the point
// estimate. On 40 positives the 95% half-width was 0.124, wider than the gap
// between two runs of the same head, so a point estimate near the bar decided
// eligibility by noise (#1041).
const (
	MinRecall        = 0.80
	MaxFalsePositive = 0.10
)

// RecallLow and FalsePositiveHigh are the 95% Wilson bounds, which is the
// interval to use on a proportion near 0 or 1 where the normal approximation
// puts its limits outside [0,1] and reports certainty it does not have.
func (r Report) RecallLow() float64 {
	lo, _ := wilson(r.Recalled, r.Positives)
	return lo
}

func (r Report) FalsePositiveHigh() float64 {
	_, hi := wilson(r.FalsePos, r.Negatives)
	return hi
}

// wilson returns the 95% score interval for k successes in n trials.
func wilson(k, n int) (lo, hi float64) {
	if n == 0 {
		return 0, 1
	}
	const z = 1.959964
	fn := float64(n)
	p := float64(k) / fn
	d := 1 + z*z/fn
	centre := (p + z*z/(2*fn)) / d
	spread := z * math.Sqrt(p*(1-p)/fn+z*z/(4*fn*fn)) / d
	return math.Max(0, centre-spread), math.Min(1, centre+spread)
}

// Eligible reports whether this head may be asked in earnest.
//
// Both halves are required, because each alone is trivially passed: a head that
// answers NO to everything scores a flawless false-positive rate, and one that
// answers YES to everything scores perfect recall.
//
// Judged on the interval rather than the point estimate, so a head is eligible
// only when the probe set is large enough to say so. That is strictly harder to
// pass and deliberately: the failure this prevents is a control that reports
// competence it has not measured.
func (r Report) Eligible() bool {
	return r.Positives > 0 && r.Negatives > 0 &&
		r.RecallLow() >= MinRecall && r.FalsePositiveHigh() <= MaxFalsePositive
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
