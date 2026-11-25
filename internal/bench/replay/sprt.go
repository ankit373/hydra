// SPDX-License-Identifier: MIT

package replay

import (
	"context"
	"errors"

	"github.com/ankit373/hydra/internal/trust"
)

// sprtPolicy is hydra's current default, trust.Run, replayed: agreement over
// models, each vote weighed by its calibrated sensitivity and specificity, at
// confidence 1 - alpha. Calibration and the family discount come from the
// calibrate half, in memory; answers agree when their answer keys match.
type sprtPolicy struct {
	alpha  float64
	cal    *trust.Calibrator
	co     *trust.CoAgreement
	cost   map[string]float64 // mean calibration cost per head, the source's estimate
	family map[string]string
}

func newSPRT(o Options) (Policy, error) { return &sprtPolicy{alpha: o.Alpha}, nil }

// keyOf is the text a row answers with. A row with no answer key agrees with nothing.
func keyOf(r Row) string {
	if r.AnswerKey == "" {
		return "\x00" + r.Task + "\x00" + r.Head
	}
	return r.AnswerKey
}

func sameKey(a, b string) bool { return a == b }

// fitSPRT trains cal and records co-agreement from labelled calibrate rows. Every
// distinct answer to a task is a hypothesis; each head says "correct" for it when
// its key matches, which is the LLR scoreHypotheses gives every hypothesis.
func fitSPRT(cal []Row, c *trust.Calibrator, record func(domain string, ids, families, texts []string)) error {
	byTask := map[string][]Row{}
	var tasks []string
	for _, r := range cal {
		if _, ok := byTask[r.Task]; !ok {
			tasks = append(tasks, r.Task)
		}
		byTask[r.Task] = append(byTask[r.Task], r)
	}
	for _, t := range tasks {
		rows := byTask[t]
		seen := map[string]bool{}
		ids, fams, texts := make([]string, len(rows)), make([]string, len(rows)), make([]string, len(rows))
		for i, h := range rows {
			ids[i], fams[i], texts[i] = h.Head, h.Family, keyOf(h)
		}
		for _, hyp := range rows {
			k := keyOf(hyp)
			if seen[k] {
				continue
			}
			seen[k] = true
			actual := trust.OutcomeFor(hyp.Correct)
			for _, h := range rows {
				if err := c.Update(h.Head, hyp.Kind, keyOf(h) == k, actual); err != nil {
					return err
				}
			}
		}
		record(rows[0].Kind, ids, fams, texts)
	}
	return nil
}

func (p *sprtPolicy) Calibrate(cal []Row) error {
	c, err := trust.New("")
	if err != nil {
		return err
	}
	co := &trust.CoAgreement{}
	err = fitSPRT(cal, c, func(d string, ids, fams, texts []string) { co.Add(d, ids, fams, texts, sameKey) })
	if err != nil {
		return err
	}
	sum, n := map[string]float64{}, map[string]int{}
	p.family = map[string]string{}
	for _, r := range cal {
		sum[r.Head] += r.CostUSD
		n[r.Head]++
		p.family[r.Head] = r.Family
	}
	p.cost = make(map[string]float64, len(n))
	for h, k := range n {
		p.cost[h] = sum[h] / float64(k)
	}
	p.cal, p.co = c, co
	return nil
}

// sprtExec answers with a task's cached rows; a head with no row errors, and
// trust.Run skips it, as it skips a head that fails live.
type sprtExec map[string]*Row

func (e sprtExec) Execute(_ context.Context, src trust.Source, _ trust.Task) (trust.Answer, error) {
	r, ok := e[src.ID]
	if !ok {
		return trust.Answer{}, errors.New("no cached answer")
	}
	return trust.Answer{Text: keyOf(*r), CostUSD: r.CostUSD}, nil
}

// sources is every head in order that answered this task, as trust.Run sees them.
func (p *sprtPolicy) sources(rows map[string]*Row, order []string) []trust.Source {
	var out []trust.Source
	for _, h := range order {
		if _, ok := rows[h]; ok {
			out = append(out, trust.Source{ID: h, Family: p.family[h], EstCostUSD: p.cost[h]})
		}
	}
	return out
}

func (p *sprtPolicy) Decide(task []Row, order []string) Outcome {
	rows := byHead(task)
	srcs := p.sources(rows, order)
	if len(srcs) == 0 {
		return Outcome{Reason: "no calibrated head answered"}
	}
	res, err := trust.Run(context.Background(), trust.Task{Domain: task[0].Kind}, srcs, sprtExec(rows), p.cal,
		trust.Target{Confidence: 1 - p.alpha}, trust.WithEquivalence(sameKey), trust.WithCoAgreement(p.co))
	if err != nil {
		return Outcome{Reason: err.Error()}
	}
	o := Outcome{Spent: res.SpentUSD, Reason: res.Decision.String()}
	for _, e := range res.Ledger {
		o.Bought = append(o.Bought, e.Source)
	}
	if res.Decision == trust.DecisionAccept {
		for _, h := range o.Bought {
			if keyOf(*rows[h]) == res.Candidate {
				o.Accepted = rows[h]
				break
			}
		}
	}
	return o
}
