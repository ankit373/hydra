// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/decide"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/ground"
	"github.com/ankit373/hydra/internal/linkcheck"
	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/internal/workspace"
)

// dispatchChecks are the checks that read the answer itself, so a dispatch,
// which writes no file, can still run them.
var dispatchChecks = []string{"ground", "links", "match:labels=<file>", "match:pattern=<re>", "cmd:<command with {answer} or {file}>"}

// answerCheck is one parsed --check, built before anything routes.
type answerCheck struct {
	oracle oracle.Oracle
	source string
	ground *ground.Oracle // set for ground only, whose spans and "not checked" print apart
}

// parseChecks builds every --check, refusing a bad one before anything routes.
// The gen checks come back apart: they are written only once the answer exists.
func parseChecks(specs []string, prompt string) ([]answerCheck, genChecks, error) {
	checks := make([]answerCheck, 0, len(specs))
	var gen genChecks
	for _, s := range specs {
		isGen, err := gen.parseGen(s)
		if !isGen {
			var c answerCheck
			c, err = parseCheck(s, prompt)
			checks = append(checks, c)
		}
		if err != nil {
			return nil, genChecks{}, fmt.Errorf("--check %q: %w", s, err)
		}
	}
	return checks, gen, nil
}

func parseCheck(spec, prompt string) (answerCheck, error) {
	kind, arg, _ := strings.Cut(spec, ":")
	switch {
	case spec == "ground":
		g := &ground.Oracle{Prompt: prompt}
		return answerCheck{oracle: g, source: g.Key(), ground: g}, nil
	case spec == "links":
		o := &linkcheck.Oracle{Gate: linkcheck.LedgerGate}
		return answerCheck{oracle: o, source: o.Key()}, nil
	case kind == "match":
		return parseMatch(arg)
	case kind == "cmd":
		// Without a placeholder the command never sees the answer, which is why --verify is refused.
		if !strings.Contains(arg, "{answer}") && !strings.Contains(arg, "{file}") {
			return answerCheck{}, errors.New("the command has no {answer} or {file}, so it cannot see the answer")
		}
		fields := strings.Fields(arg)
		o := &oracle.CommandOracle{Template: arg, Source: "verifier:" + fields[0], Env: workspace.ValidatorEnvNames(config.ScriptHome())}
		return answerCheck{oracle: o, source: o.Source}, nil
	}
	return answerCheck{}, errors.New("unknown check, want one of " + strings.Join(dispatchChecks, ", "))
}

// parseMatch reads labels=<file> or pattern=<re>; the id is the file's name or the pattern.
func parseMatch(arg string) (answerCheck, error) {
	key, val, _ := strings.Cut(arg, "=")
	var (
		o   *oracle.MatchOracle
		err error
	)
	switch key {
	case "labels":
		var labels []string
		if labels, err = readLabels(val); err != nil {
			return answerCheck{}, err
		}
		o, err = oracle.NewMatch(filepath.Base(val), labels, "")
	case "pattern":
		o, err = oracle.NewMatch(val, nil, val)
	default:
		return answerCheck{}, errors.New("match takes labels=<file> or pattern=<re>")
	}
	if err != nil {
		return answerCheck{}, err
	}
	return answerCheck{oracle: o, source: o.Source}, nil
}

// checkRule sets every check as opts' escalation hook: a real fail asks up to
// max stronger heads, a check that could not run asks none.
func checkRule(opts *dispatch.Options, checks []answerCheck, max int) *decide.OnFail {
	esc := &decide.OnFail{Max: max}
	for _, c := range checks {
		esc.Checks = append(esc.Checks, decide.Verifier{Source: c.source, Oracle: c.oracle})
	}
	if len(checks) == 0 {
		return esc
	}
	opts.Escalate = esc.Judge
	// The rerun sees the rejected answer, which must never be cached.
	if max > 0 {
		opts.Retry, opts.NoCache = dispatch.RetryPrompt, true
	}
	return esc
}

// runChecks reports and files every answer esc judged, the served one r last.
// It never fails the dispatch.
func runChecks(ctx context.Context, checks []answerCheck, esc *decide.OnFail, r *dispatch.Result, prompt, enum, domain, runID string) {
	if len(checks) == 0 {
		return
	}
	// A cached answer is served without reaching the hook, so it is judged here.
	if len(esc.Answers()) == 0 {
		esc.Judge(ctx, r)
	}
	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		fmt.Fprintf(stdout, "  %s\n", dimStyle.Render("calibration: "+err.Error()))
		return
	}
	// Judge appends one verdict per check per answer, in order.
	answers, verdicts := esc.Answers(), esc.Verdicts()
	for i, a := range answers {
		vs := verdicts[i*len(checks) : (i+1)*len(checks)]
		served := i == len(answers)-1
		if served {
			printVerdicts(cal, checks, vs, domain)
		} else {
			printRejected(a.Head.ID, vs)
		}
		fileAnswer(cal, checks, vs, a, prompt, enum, domain, runID)
	}
	if len(answers) > 1 {
		fmt.Fprintf(stdout, "  %s\n", dimStyle.Render(fmt.Sprintf("served %s after %d stronger head(s)", r.Head.ID, len(answers)-1)))
	}
	if why := esc.Unasked(); why != "" {
		for _, g := range r.Gates {
			if g.Code == dispatch.GateCostCeiling {
				why += "; " + g.Head + ": " + g.Detail
			}
		}
		fmt.Fprintf(stdout, "  %s\n\n", warnStyle.Render("no rerun: "+why))
	}
}

// printVerdicts prints the served answer's verdicts, each with its calibrated strength.
func printVerdicts(cal *trust.Calibrator, checks []answerCheck, vs []decide.Judged, domain string) {
	for k, c := range checks {
		j := vs[k]
		if !j.Checked {
			printNotChecked(c.source, errors.New(j.Detail))
			continue
		}
		v := oracle.Verdict{Passed: j.Passed, Detail: j.Detail}
		measured, why := oracle.Measured(cal, c.source, domain)
		llr := oracle.LLR(cal, c.source, domain, v)
		// The served answer is the last one judged, so Last holds its spans.
		if c.ground != nil {
			printGrounding(c.source, c.ground.Last, v, measured, why, llr)
		} else {
			printCheck(c.source, v, measured, why, llr)
		}
	}
}

// printRejected is one line per answer a failed check passed over.
func printRejected(head string, vs []decide.Judged) {
	for _, j := range vs {
		if j.Checked && !j.Passed {
			fmt.Fprintf(stdout, "\n  %s  %s  %s\n", warnStyle.Render("REJECTED"), head,
				dimStyle.Render(j.Check+": "+oneLine(j.Detail)))
			return
		}
	}
}

// fileAnswer scores one judged answer's span, files its verdicts and example,
// and trains the head that wrote it, so a failure counts against that head.
func fileAnswer(cal *trust.Calibrator, checks []answerCheck, vs []decide.Judged, a *dispatch.Result, prompt, enum, domain, runID string) {
	var sources, failed []string
	passed := true
	for k, c := range checks {
		j := vs[k]
		if !j.Checked {
			continue
		}
		name := "oracle"
		if c.ground != nil {
			name = "grounding"
		}
		var spanEv runlog.Event // a cached answer has no span of its own to score
		if a.SpanID != "" {
			ev, err := scoreVerdict(runID, a.SpanID, name, c.source, j.Passed, j.Detail)
			if err != nil {
				fmt.Fprintf(stdout, "  %s\n", dimStyle.Render("span score: "+err.Error()))
			}
			spanEv = ev
		}
		fileVerdict(spanEv, evalset.Verdict{
			CandidateHash: candidateHash(a.Output), Domain: domain, Check: c.source, Passed: j.Passed, Detail: j.Detail,
		})
		if a.SpanID == "" {
			continue // no head ran, so there is no example to file or head to train
		}
		sources, passed = append(sources, c.source), passed && j.Passed
		if !j.Passed {
			failed = append(failed, c.source+": "+j.Detail)
		}
	}
	if len(sources) == 0 {
		return // nothing was checked, so there is no verdict to keep
	}
	fileExample(evalset.Example{
		TaskHash: evalset.TaskHashFor(prompt), Domain: domain, Source: strings.Join(sources, ","),
		Head: a.Head.ID, Enum: enum, Tier: rank.UITier(a.Head),
		Candidate: a.Output, Passed: passed, Detail: strings.Join(failed, "; "),
	})
	// The answer is the head's implicit claim of correctness, as hyctl edit records it.
	_ = cal.Update(a.Head.ID, domain, true, trust.OutcomeFor(passed))
}

// fileExample adds a verdict to the eval corpus. Reported, never fatal: losing
// the example must not fail the verification that produced it.
func fileExample(e evalset.Example) {
	e.Config, _ = config.Breadcrumb()
	added, err := evalset.Add(evalset.DefaultPath(), e)
	switch {
	case err != nil:
		fmt.Fprintf(stdout, "  %s\n", dimStyle.Render("eval set: "+err.Error()))
	case added:
		fmt.Fprintf(stdout, "  %s\n", dimStyle.Render("recorded to the eval set"))
	}
}

// printCheck prints a pass or fail and its calibrated strength, or why none is stated.
func printCheck(src string, v oracle.Verdict, measured bool, why string, llr float64) {
	status := cortexStyle.Render("PASS")
	if !v.Passed {
		status = warnStyle.Render("FAIL")
	}
	fmt.Fprintf(stdout, "\n  %s  %s\n", status, dimStyle.Render(src))
	if v.Detail != "" {
		fmt.Fprintf(stdout, "  %s\n", dimStyle.Render(v.Detail))
	}
	if measured {
		fmt.Fprintf(stdout, "  calibrated evidence  %+.3f nats\n\n", llr)
		return
	}
	fmt.Fprintf(stdout, "  %s\n\n", dimStyle.Render("insufficient evidence to state a strength: "+why))
}
