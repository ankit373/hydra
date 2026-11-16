// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/ankit373/hydra/internal/decide"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/serve"
)

// servedChecks are the checks a request named in X-Hydra-Check.
type servedChecks struct {
	checks []answerCheck
	gen    genChecks
}

// parseServedChecks builds them before anything routes; a bad pattern is the client's mistake.
func parseServedChecks(specs []string, prompt string) (servedChecks, error) {
	var out servedChecks
	for _, s := range specs {
		if s == "gen" {
			out.gen.all = true
			continue
		}
		c, err := parseCheck(s, prompt)
		if err != nil {
			return servedChecks{}, fmt.Errorf("%w: %s %q: %w", serve.ErrBadRequest, serve.CheckHeader, s, err)
		}
		out.checks = append(out.checks, c)
	}
	return out, nil
}

func (s servedChecks) named() bool { return len(s.checks) > 0 || s.gen.asked() }

// verifiers are the checks that can judge an answer before it is served, so they join the cascade.
func (s servedChecks) verifiers() []decide.Verifier {
	out := make([]decide.Verifier, 0, len(s.checks))
	for _, c := range s.checks {
		out = append(out, decide.Verifier{Source: c.source, Oracle: c.oracle})
	}
	return out
}

// verdict judges the served answer with every named check. gen runs only now,
// after the answer, and like every check here it reports and never reruns.
func (s servedChecks) verdict(ctx context.Context, g genRun, res *dispatch.Result, prompt string) (result, detail string) {
	var v verified
	esc := &decide.OnFail{Checks: s.verifiers()}
	esc.Judge(ctx, res)
	for _, j := range esc.Verdicts() {
		v.add(j.Check, j.Checked, j.Passed, j.Detail)
	}
	switch {
	case !s.gen.asked():
	case g.kind == "":
		v.add("gen", false, false, "no kind of work: name one with "+serve.TaskClassHeader)
	default:
		g.want = s.gen
		v.addGen(g.check(ctx, res, prompt))
	}
	switch v.status() {
	case groundUnsupported:
		return serve.CheckFailed, v.why()
	case groundNotChecked:
		return serve.CheckNotChecked, v.why()
	}
	return serve.CheckPassed, strings.Join(v.passed, ", ")
}
