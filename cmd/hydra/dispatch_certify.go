// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"fmt"

	"github.com/ankit373/hydra/internal/aver"
	"github.com/ankit373/hydra/internal/decide"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/trust"
)

// certifyWalk is certified acceptance over up to Budget candidates. It is the
// dispatch's Escalate hook: an answer not certified asks the next model, at its own bar.
type certifyWalk struct {
	alpha   float64
	walk    *aver.Walk
	checks  []answerCheck
	esc     *decide.OnFail
	tab     aver.Table
	kind    string
	seen    []*dispatch.Result
	lines   []string // one per candidate, for the report
	outcome aver.Outcome
	wanted  bool // the last answer asked for another
}

func newCertifyWalk(alpha float64, candidates int, checks []answerCheck, esc *decide.OnFail, domain string) (*certifyWalk, error) {
	w, err := aver.NewWalk(alpha, candidates, aver.RuleSpend)
	if err != nil {
		return nil, err
	}
	c := &certifyWalk{alpha: alpha, walk: w, checks: checks, esc: esc, kind: orUnknown(domain),
		outcome: aver.Outcome{Alpha: alpha, Reason: aver.ReasonNoCheck, Budget: candidates}}
	if len(checks) > 0 {
		tab, err := certifyTable()
		if err != nil {
			fmt.Fprintf(stdout, "  %s\n", dimStyle.Render("certify: "+err.Error()))
		}
		c.tab = tab
	}
	return c, nil
}

// Judge runs the answer's checks, adds its agreement with every earlier
// candidate as model:<head>, and asks for the next model unless it is certified.
func (c *certifyWalk) Judge(ctx context.Context, r *dispatch.Result) (bool, string) {
	c.esc.Judge(ctx, r)
	verdicts := map[string]bool{}
	served := c.esc.Verdicts()[len(c.esc.Verdicts())-len(c.checks):]
	for k, ch := range c.checks {
		if served[k].Checked {
			verdicts[ch.source] = served[k].Passed
		}
	}
	for _, p := range c.seen {
		name, agree := "model:"+p.Head.ID, trust.TextEquivalence(p.Output, r.Output)
		verdicts[name] = agree
		if r.SpanID != "" {
			fileVerdict(runlog.Event{SpanID: r.SpanID, Head: r.Head.ID}, evalset.Verdict{
				CandidateHash: candidateHash(r.Output), Domain: c.kind, Check: name, Passed: agree,
			})
		}
	}
	c.seen = append(c.seen, r)
	bar, _ := c.walk.Next()
	o := certifyOutcome(c.alpha, bar, verdicts, c.tab, c.kind, orUnknown(r.Head.ID))
	o.Candidates, o.Budget = c.walk.Bought(), c.walk.Budget()
	c.outcome = o
	c.lines = append(c.lines, fmt.Sprintf("candidate %d, %s: %s", o.Candidates, r.Head.ID, o.Line()))
	c.wanted = !o.Certified() && c.walk.Bought() < c.walk.Budget()
	if !c.wanted {
		return false, ""
	}
	return true, "not certified at bar " + fmt.Sprintf("%.3g", bar)
}

// finish names the stop that fired, prints the outcome and writes it on the task span.
func (c *certifyWalk) finish(r *dispatch.Result, runID, taskID string) aver.Outcome {
	o := c.outcome
	if len(c.seen) > 0 && !o.Certified() && o.Budget > 1 {
		o.Reason = aver.ReasonCandidates
		if c.wanted {
			o.Reason = aver.ReasonNoHead
			for _, g := range r.Gates {
				if g.Code == dispatch.GateCostCeiling {
					o.Reason = aver.ReasonCostCeiling
				}
			}
		}
	}
	if len(c.seen) > 1 {
		for _, l := range c.lines {
			fmt.Fprintf(stdout, "  %s\n", dimStyle.Render(l))
		}
	}
	style := warnStyle
	if o.Certified() {
		style = cortexStyle
	}
	fmt.Fprintf(stdout, "\n  %s\n", style.Render(o.Line()))
	if taskID != "" {
		if err := runlog.AppendCertified(runID, runlog.SpanIDFor(taskID), o); err != nil {
			fmt.Fprintf(stdout, "  %s\n", dimStyle.Render("task score: "+err.Error()))
		}
	}
	return o
}

// certifyTable is the calibration table from the verdict log, model votes
// included, with checks that fail together counted once.
func certifyTable() (aver.Table, error) {
	joint, err := evalset.Joint(evalset.VerdictsPath())
	if err != nil {
		return aver.Table{}, err
	}
	runs, err := trust.LoadRuns(trust.DefaultLogPath())
	if err != nil {
		return aver.Table{}, err
	}
	joint, _ = evalset.AddModelVotes(joint, runs)
	answers := averAnswers(joint)
	tab, err := aver.Fit(answers, aver.DefaultDelta)
	if err != nil {
		return aver.Table{}, err
	}
	return aver.Gate(answers, tab).Apply(tab), nil
}

// certifyOutcome multiplies the verdicts' e-values, strongest evidence first,
// and stops on accept or futility at this candidate's bar.
func certifyOutcome(alpha, bar float64, verdicts map[string]bool, tab aver.Table, kind, head string) aver.Outcome {
	o := aver.Outcome{Alpha: alpha, Bar: bar, Product: 1}
	idx := tab.Index()
	inKind := map[string]bool{}
	for _, c := range tab.Cells {
		if c.Kind == kind {
			inKind[c.Check] = true
		}
	}
	var cells []aver.Cell
	have := 0
	for check := range verdicts {
		c, ok := idx[[3]string{kind, check, head}]
		if !ok {
			continue
		}
		have = max(have, c.Wrong)
		if !c.Skip {
			cells = append(cells, c)
		}
	}
	cells = aver.Order(cells, func(string) float64 { return 0 }) // every check already ran
	run := aver.NewRun(bar)
	if run.Futile(cells) {
		o.Reason, o.Have, o.Need = aver.ReasonTooFewWrong, have, aver.NeedWrong(max(1, len(inKind)), aver.DefaultDelta, bar)
		return o
	}
	for i, c := range cells {
		if run.Futile(cells[i:]) {
			break
		}
		run.Add(aver.StepFor(c, verdicts[c.Check]))
		if run.Accepted() {
			o.Mode = aver.ModeIndependence
			break
		}
	}
	o.Product, o.Steps = run.Product, run.Seen
	if !o.Certified() {
		o.Reason = aver.ReasonFutile
	}
	return o
}
