// SPDX-License-Identifier: MIT

package replay

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Outcome is what a policy did on one task. Accepted is nil when it refused
// ("not verified"); Spent counts every answer bought and every check run.
type Outcome struct {
	Accepted *Row
	Bought   []string
	Spent    float64
	Reason   string
}

// Policy decides acceptance on one task's candidates, after seeing only the
// calibration half. order is the cheapest-first head order from that half.
type Policy interface {
	Calibrate(cal []Row) error
	Decide(task []Row, order []string) Outcome
}

// Options configure a policy. A baseline that has no use for alpha ignores it.
type Options struct {
	Alpha float64
	Delta float64 // chance a calibrated bound is wrong, for the AVER policies
	Check string
}

var policies = map[string]func(Options) (Policy, error){
	"always_strongest":     func(Options) (Policy, error) { return &alwaysStrongest{}, nil },
	"aver_gated":           newAverProduct(true, true),
	"aver_product":         newAverProduct(false, false),
	"aver_product_perhead": newAverProduct(true, false),
	"first_check_pass": func(o Options) (Policy, error) {
		if o.Check == "" {
			return nil, errors.New("first_check_pass needs its primary check named (bench replay --check)")
		}
		return &firstCheckPass{check: o.Check}, nil
	},
}

// Names lists the policies New knows, sorted.
func Names() []string {
	names := make([]string, 0, len(policies))
	for n := range policies {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// New makes a fresh, uncalibrated policy.
func New(name string, o Options) (Policy, error) {
	mk, ok := policies[name]
	if !ok {
		return nil, fmt.Errorf("no policy %q (known: %s)", name, strings.Join(Names(), ", "))
	}
	return mk(o)
}

func byHead(task []Row) map[string]*Row {
	m := make(map[string]*Row, len(task))
	for i := range task {
		m[task[i].Head] = &task[i]
	}
	return m
}

// alwaysStrongest buys the head most often correct on calibration and accepts it
// unchecked: the cost and error of not checking at all.
type alwaysStrongest struct{ head string }

func (p *alwaysStrongest) Calibrate(cal []Row) error {
	right, n := map[string]int{}, map[string]int{}
	for _, r := range cal {
		n[r.Head]++
		if r.Correct {
			right[r.Head]++
		}
	}
	best, bestAcc := "", -1.0
	for _, h := range CheapestFirst(cal) { // ties go to the cheaper head
		if acc := float64(right[h]) / float64(n[h]); acc > bestAcc {
			best, bestAcc = h, acc
		}
	}
	if best == "" {
		return errors.New("always_strongest: no calibration answers")
	}
	p.head = best
	return nil
}

func (p *alwaysStrongest) Decide(task []Row, _ []string) Outcome {
	r, ok := byHead(task)[p.head]
	if !ok {
		return Outcome{Reason: "no answer from " + p.head}
	}
	return Outcome{Accepted: r, Bought: []string{p.head}, Spent: r.CostUSD, Reason: "strongest on calibration"}
}

// firstCheckPass buys heads cheapest first and accepts the first answer whose
// primary check passes. A missing verdict is a check that did not pass.
type firstCheckPass struct{ check string }

func (p *firstCheckPass) Calibrate(cal []Row) error {
	for _, r := range cal {
		if _, ok := r.Verdicts[p.check]; ok {
			return nil
		}
	}
	return fmt.Errorf("first_check_pass: no calibration answer has a %q verdict", p.check)
}

func (p *firstCheckPass) Decide(task []Row, order []string) Outcome {
	rows := byHead(task)
	var o Outcome
	for _, h := range order {
		r, ok := rows[h]
		if !ok {
			continue
		}
		o.Bought = append(o.Bought, h)
		o.Spent += r.CostUSD + r.CheckCostUSD[p.check]
		if r.Verdicts[p.check] {
			o.Accepted, o.Reason = r, p.check+" passed"
			return o
		}
	}
	o.Reason = "no answer passed " + p.check
	return o
}
