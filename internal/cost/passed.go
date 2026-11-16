// SPDX-License-Identifier: MIT

package cost

import (
	"cmp"
	"slices"

	"github.com/ankit373/hydra/internal/stats"
)

// MinChecked is how many checked answers a figure needs before it is shown.
// Below it the interval is too wide to say anything, so nothing is shown.
const MinChecked = 30

// PassRecord is what checked answers cost and how many of them passed.
type PassRecord struct {
	Answers int
	Passed  int
	CostUSD float64
	Lo      float64 // 95% Wilson interval on the pass rate
	Hi      float64
}

// Rate is the share of checked answers that passed.
func (p PassRecord) Rate() float64 {
	if p.Answers == 0 {
		return 0
	}
	return float64(p.Passed) / float64(p.Answers)
}

// PerPassed is the spend behind each passed answer, failures included. False
// when nothing passed, since the cost of a pass is then undefined, not infinite.
func (p PassRecord) PerPassed() (float64, bool) {
	if p.Passed == 0 {
		return 0, false
	}
	return p.CostUSD / float64(p.Passed), true
}

// KindRecord is one kind of work: the routing that ran, and the single model
// with the best measured pass rate on it. Best is "" when no one model has
// MinChecked answers of this kind on its own.
type KindRecord struct {
	Kind       string
	Routed     PassRecord
	Best       string
	BestRecord PassRecord
}

// PassedReport compares cost per passed answer for the routing used with the
// best single model on the same kinds of work, both from this machine's log.
type PassedReport struct {
	Routed PassRecord
	Kinds  []KindRecord // most checked answers first
}

type answer struct {
	kind    string
	model   string // "" when more than one model worked on it
	costUSD float64
	passed  bool
}

// PerPassedAnswer joins cost rows to verdicts by span. passed maps a span to
// whether its answer passed. ok is false below MinChecked checked answers.
func PerPassedAnswer(rows []Row, passed map[string]bool) (rep PassedReport, checked int, ok bool) {
	bySpan := map[string]*answer{}
	var order []string
	for _, r := range rows {
		p, hit := passed[r.SpanID]
		if r.SpanID == "" || !hit {
			continue
		}
		a := bySpan[r.SpanID]
		if a == nil {
			a = &answer{model: CanonicalKey(r), passed: p}
			bySpan[r.SpanID] = a
			order = append(order, r.SpanID)
		} else if a.model != CanonicalKey(r) {
			a.model = ""
		}
		if a.kind == "" {
			a.kind = r.Domain
		}
		a.costUSD += r.EstCostUSD
	}
	if len(order) < MinChecked {
		return PassedReport{}, len(order), false
	}

	routed := map[string]*PassRecord{}
	single := map[string]map[string]*PassRecord{}
	for _, span := range order {
		a := bySpan[span]
		rep.Routed.add(a)
		if a.kind == "" {
			continue
		}
		recordOf(routed, a.kind).add(a)
		if a.model != "" {
			if single[a.kind] == nil {
				single[a.kind] = map[string]*PassRecord{}
			}
			recordOf(single[a.kind], a.model).add(a)
		}
	}
	rep.Routed.interval()
	for kind, r := range routed {
		if r.Answers < MinChecked {
			continue
		}
		r.interval()
		k := KindRecord{Kind: kind, Routed: *r}
		k.Best, k.BestRecord = bestSingle(single[kind])
		rep.Kinds = append(rep.Kinds, k)
	}
	slices.SortFunc(rep.Kinds, func(a, b KindRecord) int {
		return cmp.Or(cmp.Compare(b.Routed.Answers, a.Routed.Answers), cmp.Compare(a.Kind, b.Kind))
	})
	return rep, len(order), true
}

// bestSingle is the model with the highest lower bound on its pass rate among
// those with MinChecked answers; ties go to the cheaper pass, then the name.
func bestSingle(models map[string]*PassRecord) (string, PassRecord) {
	var name string
	var best PassRecord
	for m, r := range models {
		if r.Answers < MinChecked {
			continue
		}
		r.interval()
		if name == "" || better(m, *r, name, best) {
			name, best = m, *r
		}
	}
	return name, best
}

func better(m string, r PassRecord, n string, b PassRecord) bool {
	if r.Lo != b.Lo {
		return r.Lo > b.Lo
	}
	rc, rok := r.PerPassed()
	bc, bok := b.PerPassed()
	if rok && bok && rc != bc {
		return rc < bc
	}
	return m < n
}

func recordOf(m map[string]*PassRecord, key string) *PassRecord {
	if m[key] == nil {
		m[key] = &PassRecord{}
	}
	return m[key]
}

func (p *PassRecord) add(a *answer) {
	p.Answers++
	p.CostUSD += a.costUSD
	if a.passed {
		p.Passed++
	}
}

// interval sets Lo and Hi; Wilson only errs on k > n or n < 0, never here.
func (p *PassRecord) interval() {
	p.Lo, p.Hi, _ = stats.Wilson(p.Passed, p.Answers, stats.Z95)
}
