// SPDX-License-Identifier: MIT

package replay

import (
	"errors"
	"fmt"

	"github.com/ankit373/hydra/internal/aver"
)

// pooledHead stands in for every head when the product is fitted pooled.
const pooledHead = "*"

// averProduct buys heads cheapest first and accepts the first answer whose e-value
// product reaches its candidate's bar, 1/(alpha w_j) for the j-th answer bought.
// A head that cannot reach the bar even if every check passes is not bought.
type averProduct struct {
	alpha   float64
	delta   float64
	rule    string
	perHead bool
	bars    []float64                 // one per candidate, fixed from the head count on calibration
	cells   map[[2]string][]aver.Cell // (kind, head) -> usable cells, cheapest evidence first
}

func newAverProduct(perHead bool) func(Options) (Policy, error) {
	return func(o Options) (Policy, error) {
		rule := o.Rule
		if rule == "" {
			rule = aver.RuleSpend
		}
		return &averProduct{alpha: o.Alpha, delta: o.Delta, rule: rule, perHead: perHead}, nil
	}
}

// Bars is each candidate's bar, in the order answers are bought.
func (p *averProduct) Bars() []float64 { return p.bars }

func (p *averProduct) head(h string) string {
	if p.perHead {
		return h
	}
	return pooledHead
}

func (p *averProduct) Calibrate(cal []Row) error {
	answers := make([]aver.Answer, 0, len(cal))
	costSum, costN := map[string]float64{}, map[string]int{}
	for _, r := range cal {
		answers = append(answers, aver.Answer{Kind: r.Kind, Head: p.head(r.Head), Correct: r.Correct, Verdicts: r.Verdicts})
		for c := range r.Verdicts {
			costSum[c] += r.CheckCostUSD[c]
			costN[c]++
		}
	}
	w, err := aver.Weights(len(CheapestFirst(cal)), p.rule)
	if err != nil {
		return err
	}
	p.bars = make([]float64, len(w))
	for j, x := range w {
		p.bars[j] = aver.Bar(p.alpha, x)
	}
	tab, err := aver.Fit(answers, p.delta)
	if err != nil {
		return err
	}
	if len(tab.Cells) == 0 {
		return errors.New("aver: no calibration answer has a verdict")
	}
	byHead := map[[2]string][]aver.Cell{}
	for _, c := range tab.Cells {
		if !c.Skip {
			k := [2]string{c.Kind, c.Head}
			byHead[k] = append(byHead[k], c)
		}
	}
	meanCost := func(c string) float64 { return costSum[c] / float64(costN[c]) }
	p.cells = make(map[[2]string][]aver.Cell, len(byHead))
	for k, cs := range byHead {
		p.cells[k] = aver.Order(cs, meanCost)
	}
	return nil
}

// Decide runs only checks the row has a verdict for: a missing one did not run.
func (p *averProduct) Decide(task []Row, order []string) Outcome {
	rows := byHead(task)
	var o Outcome
	for _, h := range order {
		r, ok := rows[h]
		if !ok {
			continue
		}
		if len(o.Bought) == len(p.bars) {
			break // a candidate with no weight is never accepted
		}
		bar := p.bars[len(o.Bought)]
		all := p.cells[[2]string{r.Kind, p.head(h)}]
		run := aver.NewRun(bar)
		if run.Futile(all) {
			continue
		}
		var cells []aver.Cell
		for _, c := range all {
			if _, ran := r.Verdicts[c.Check]; ran {
				cells = append(cells, c)
			}
		}
		o.Bought = append(o.Bought, h)
		o.Spent += r.CostUSD
		for i, c := range cells {
			if run.Futile(cells[i:]) {
				break
			}
			passed := r.Verdicts[c.Check]
			o.Spent += r.CheckCostUSD[c.Check]
			run.Add(aver.Step{Check: c.Check, Passed: passed, E: aver.EValue(c, passed)})
			if run.Accepted() {
				o.Accepted = r
				o.Reason = fmt.Sprintf("product %.3g >= bar %.3g", run.Product, bar)
				return o
			}
		}
	}
	o.Reason = "no answer reached its bar"
	return o
}
