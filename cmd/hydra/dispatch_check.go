// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/ground"
	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/trust"
)

// dispatchChecks are the checks that read the answer itself, so a dispatch,
// which writes no file, can still run them.
var dispatchChecks = []string{"ground", "match:labels=<file>", "match:pattern=<re>", "cmd:<command with {answer} or {file}>"}

// answerCheck is one parsed --check, built before anything routes.
type answerCheck struct {
	oracle oracle.Oracle
	source string
	ground *ground.Oracle // set for ground only, whose spans and "not checked" print apart
}

// parseChecks builds every --check, refusing a bad one before anything routes.
func parseChecks(specs []string, prompt string) ([]answerCheck, error) {
	checks := make([]answerCheck, 0, len(specs))
	for _, s := range specs {
		c, err := parseCheck(s, prompt)
		if err != nil {
			return nil, fmt.Errorf("--check %q: %w", s, err)
		}
		checks = append(checks, c)
	}
	return checks, nil
}

func parseCheck(spec, prompt string) (answerCheck, error) {
	kind, arg, _ := strings.Cut(spec, ":")
	switch {
	case spec == "ground":
		g := &ground.Oracle{Prompt: prompt}
		return answerCheck{oracle: g, source: g.Key(), ground: g}, nil
	case kind == "match":
		return parseMatch(arg)
	case kind == "cmd":
		// Without a placeholder the command never sees the answer, which is why --verify is refused.
		if !strings.Contains(arg, "{answer}") && !strings.Contains(arg, "{file}") {
			return answerCheck{}, errors.New("the command has no {answer} or {file}, so it cannot see the answer")
		}
		fields := strings.Fields(arg)
		o := &oracle.CommandOracle{Template: arg, Source: "verifier:" + fields[0]}
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

// runChecks judges a dispatch's answer, scores its span, files the verdict in the
// eval corpus and trains the head that answered. It never fails the dispatch.
func runChecks(ctx context.Context, checks []answerCheck, r *dispatch.Result, prompt, enum, domain, runID string) {
	if len(checks) == 0 {
		return
	}
	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		fmt.Fprintf(stdout, "  %s\n", dimStyle.Render("calibration: "+err.Error()))
		return
	}
	// The corpus keeps one example per task and answer, so every verdict folds into one.
	var sources, failed []string
	passed := true
	for _, c := range checks {
		v, verr := c.oracle.Verify(ctx, r.Output, trust.Task{Domain: domain})
		if verr != nil {
			printNotChecked(c.source, verr)
			continue
		}
		measured, why := oracle.Measured(cal, c.source, domain)
		llr := oracle.LLR(cal, c.source, domain, v)
		name := "oracle"
		if c.ground != nil {
			name = "grounding"
			printGrounding(c.source, c.ground.Last, v, measured, why, llr)
		} else {
			printCheck(c.source, v, measured, why, llr)
		}
		var spanEv runlog.Event // a cached answer has no span of its own to score
		if r.SpanID != "" {
			ev, err := scoreVerdict(runID, r.SpanID, name, c.source, v.Passed, v.Detail)
			if err != nil {
				fmt.Fprintf(stdout, "  %s\n", dimStyle.Render("span score: "+err.Error()))
			}
			spanEv = ev
		}
		fileVerdict(spanEv, evalset.Verdict{
			CandidateHash: candidateHash(r.Output), Domain: domain, Check: c.source, Passed: v.Passed, Detail: v.Detail,
		})
		if r.SpanID == "" {
			continue // no head ran, so there is no example to file or head to train
		}
		sources, passed = append(sources, c.source), passed && v.Passed
		if !v.Passed {
			failed = append(failed, c.source+": "+v.Detail)
		}
	}
	if len(sources) == 0 {
		return // nothing was checked, so there is no verdict to keep
	}
	fileExample(evalset.Example{
		TaskHash: evalset.TaskHashFor(prompt), Domain: domain, Source: strings.Join(sources, ","),
		Head: r.Head.ID, Enum: enum, Tier: rank.UITier(r.Head),
		Candidate: r.Output, Passed: passed, Detail: strings.Join(failed, "; "),
	})
	// The answer is the head's implicit claim of correctness, as hyctl edit records it.
	_ = cal.Update(r.Head.ID, domain, true, trust.OutcomeFor(passed))
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
