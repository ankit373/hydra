// SPDX-License-Identifier: MIT

package main

import (
	"fmt"

	"github.com/ankit373/hydra/internal/aver"
	"github.com/ankit373/hydra/internal/decide"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/trust"
)

// certifyServed decides certified acceptance for the served answer from its
// checks' verdicts, prints the one-line outcome and writes it on the answer's span.
func certifyServed(alpha float64, checks []answerCheck, esc *decide.OnFail, r *dispatch.Result, domain, runID string) aver.Outcome {
	verdicts := map[string]bool{}
	if len(checks) > 0 && len(esc.Verdicts()) >= len(checks) {
		served := esc.Verdicts()[len(esc.Verdicts())-len(checks):]
		for k, c := range checks {
			if served[k].Checked {
				verdicts[c.source] = served[k].Passed
			}
		}
	}
	o := aver.Outcome{Alpha: alpha, Reason: aver.ReasonNoCheck}
	if len(verdicts) > 0 {
		tab, err := certifyTable()
		if err != nil {
			fmt.Fprintf(stdout, "  %s\n", dimStyle.Render("certify: "+err.Error()))
		}
		o = certifyOutcome(alpha, verdicts, tab, orUnknown(domain), orUnknown(r.Head.ID))
	}
	style := warnStyle
	if o.Certified() {
		style = cortexStyle
	}
	fmt.Fprintf(stdout, "\n  %s\n", style.Render(o.Line()))
	if r.SpanID != "" {
		if err := runlog.AppendCertified(runID, r.SpanID, o); err != nil {
			fmt.Fprintf(stdout, "  %s\n", dimStyle.Render("span score: "+err.Error()))
		}
	}
	return o
}

// certifyTable is the verdict log's calibration, model votes included, counted
// since each check's last change, with checks that fail together counted once.
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
	tab, err := aver.FitSince(answers, aver.DefaultDelta, aver.Changes(answers))
	if err != nil {
		return aver.Table{}, err
	}
	return aver.Gate(answers, tab).Apply(tab), nil
}

// certifyOutcome multiplies the verdicts' e-values, strongest evidence first,
// and stops on accept or futility. One candidate, so the bar is 1/alpha.
func certifyOutcome(alpha float64, verdicts map[string]bool, tab aver.Table, kind, head string) aver.Outcome {
	bar := aver.Bar(alpha, 1)
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
