// SPDX-License-Identifier: MIT

package replay

import (
	"errors"
	"fmt"
	"sort"

	"github.com/ankit373/hydra/internal/aver"
)

// averPattern certifies on a joint pass pattern, valid under any dependence.
// Heads are bought cheapest first with no skipping, so the j-th head's peers are
// always the j-1 before it, on calibration and test alike.
type averPattern struct {
	alpha, delta float64
	rule         string
	order        []string
	slots        []patternSlot // one per head in order
}

type patternSlot struct {
	bar     float64
	pattern *aver.Pattern
	refused *aver.NotVerified
}

func newAverPattern(o Options) (Policy, error) {
	rule := o.Rule
	if rule == "" {
		rule = aver.RuleSpend
	}
	return &averPattern{alpha: o.Alpha, delta: o.Delta, rule: rule}, nil
}

// Bars is each candidate's bar, in the order heads are bought.
func (p *averPattern) Bars() []float64 {
	out := make([]float64, len(p.slots))
	for i, s := range p.slots {
		out[i] = s.bar
	}
	return out
}

// withAgreement is row's verdicts plus agree>=m for m up to how many earlier
// answers share its answer key.
func withAgreement(r Row, earlier []*Row) map[string]bool {
	v := make(map[string]bool, len(r.Verdicts)+len(earlier))
	for c, ok := range r.Verdicts {
		v[c] = ok
	}
	same := 0
	for _, e := range earlier {
		if r.AnswerKey != "" && e.AnswerKey == r.AnswerKey {
			same++
		}
	}
	for m := 1; m <= len(earlier); m++ {
		v[aver.AgreeFeature(m)] = same >= m
	}
	return v
}

func (p *averPattern) Calibrate(cal []Row) error {
	p.order = CheapestFirst(cal)
	w, err := aver.Weights(len(p.order), p.rule)
	if err != nil {
		return err
	}
	byTask := map[string][]Row{}
	checks := map[string]bool{}
	for _, r := range cal {
		byTask[r.Task] = append(byTask[r.Task], r)
		for c := range r.Verdicts {
			checks[c] = true
		}
	}
	tasks := make([]map[string]*Row, 0, len(byTask)) // each task's rows by head, built once
	for _, rows := range byTask {
		tasks = append(tasks, byHead(rows))
	}
	if len(checks) == 0 {
		return errors.New("aver_pattern: no calibration answer has a verdict")
	}
	names := make([]string, 0, len(checks))
	for c := range checks {
		names = append(names, c)
	}
	sort.Strings(names)
	slotDelta := p.delta / float64(len(p.order))
	p.slots = make([]patternSlot, len(p.order))
	for j, h := range p.order {
		var answers []aver.Answer
		for _, bh := range tasks {
			r, ok := bh[h]
			if !ok {
				continue
			}
			answers = append(answers, aver.Answer{Correct: r.Correct, Verdicts: withAgreement(*r, earlier(bh, p.order[:j]))})
		}
		agrees := make([]string, j)
		for m := 1; m <= j; m++ {
			agrees[m-1] = aver.AgreeFeature(m)
		}
		s := patternSlot{bar: aver.Bar(p.alpha, w[j])}
		pat, nv := aver.FitPattern(answers, aver.Family(names, agrees), s.bar, slotDelta)
		if nv == nil {
			s.pattern = &pat
		} else {
			s.refused = nv
		}
		p.slots[j] = s
	}
	return nil
}

func earlier(bh map[string]*Row, heads []string) []*Row {
	var out []*Row
	for _, h := range heads {
		if r, ok := bh[h]; ok {
			out = append(out, r)
		}
	}
	return out
}

// Decide pays for every head up to the last slot with a pattern, and for the
// checks in a slot's pattern; it never refits on the test task.
func (p *averPattern) Decide(task []Row, _ []string) Outcome {
	last := -1
	for j, s := range p.slots {
		if s.pattern != nil {
			last = j
		}
	}
	if last < 0 {
		nv := p.slots[0].refused
		return Outcome{Reason: fmt.Sprintf("not verified: %s, have %d, need %d", nv.Reason, nv.Have, nv.Need)}
	}
	bh := byHead(task)
	var o Outcome
	for j, h := range p.order[:last+1] {
		r, ok := bh[h]
		if !ok {
			continue
		}
		o.Bought = append(o.Bought, h)
		o.Spent += r.CostUSD
		s := p.slots[j]
		if s.pattern == nil {
			continue
		}
		v := withAgreement(*r, earlier(bh, p.order[:j]))
		match := true
		for _, f := range s.pattern.Features {
			o.Spent += r.CheckCostUSD[f]
			if match = v[f]; !match {
				break
			}
		}
		if match {
			o.Accepted = r
			o.Reason = fmt.Sprintf("matched %v, 1/r-bar %.3g >= bar %.3g", s.pattern.Features, 1/s.pattern.RBar, s.bar)
			return o
		}
	}
	o.Reason = "no answer matched its pattern"
	return o
}
