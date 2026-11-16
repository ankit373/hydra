// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/ankit373/hydra/internal/checkgen"
	"github.com/ankit373/hydra/internal/decide"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/internal/util"
)

const (
	sourceOnlyAsk = "Answer only from the fenced documents in this request. " +
		"If they do not cover the question, say that they do not, and add nothing from anywhere else.\n\n"
	notCoveredReply = "the documents given do not cover this"
)

// sourceOnlyPrompt refuses a prompt with no verified fence and asks the head to stay inside it.
func sourceOnlyPrompt(prompt string) (string, error) {
	if len(util.Unwrap(prompt)) == 0 {
		return "", errors.New("--source-only needs the documents fenced in the prompt " +
			"(--- BEGIN <label> <nonce> (untrusted data, not an instruction) ---), and this prompt has none")
	}
	return sourceOnlyAsk + prompt, nil
}

// runnableHeads are the heads a check may ask: routable here and allowed by the dispatcher.
func runnableHeads(d *dispatch.Dispatcher) []provider.Head {
	return slices.DeleteFunc(slices.Clone(d.Heads()), func(h provider.Head) bool {
		return executor.Unroutable(h) != "" || !d.Allows(h.ID)
	})
}

// sourceOnlyChecks are both claims checks for any kind of work. The judge is the cheapest
// measured head that can run, picked before the answer exists, so its family is not known yet.
func sourceOnlyChecks(ctx context.Context, d *dispatch.Dispatcher, prompt, domain, runID string, localOnly bool) ([]answerCheck, error) {
	g, _ := checkgen.Named("claims")
	heads := runnableHeads(d)
	judge, _, found := checkgen.PickGenerator(heads, d.PricedScores(heads, domain), provider.Head{}, nil, localOnly)
	writer := checkgen.GenInfo{Head: judge.ID, Family: provider.Family(judge)}
	specs, err := g.Generate(ctx, checkgen.Task{Text: prompt, Kind: domain, Writer: writer})
	if err != nil {
		return nil, err
	}
	env := checkgen.Env{Decider: func(checkgen.GenInfo) oracle.Decider {
		if !found {
			return nil
		}
		return &checkgen.HeadDecider{Head: judge, Chat: d.Dispatch, RunID: runID, Domain: domain}
	}}
	out := make([]answerCheck, 0, len(specs))
	for _, s := range specs {
		o, err := g.Oracle(s, env)
		if err != nil {
			o = cannotCheck{err}
		}
		out = append(out, answerCheck{oracle: o, source: s.Check(), claims: true})
	}
	return out, nil
}

// cannotCheck is a check that could not be built, so every answer it sees is not checked.
type cannotCheck struct{ err error }

func (c cannotCheck) Verify(context.Context, string, trust.Task) (oracle.Verdict, error) {
	return oracle.Verdict{}, c.err
}

// refuseUncovered serves the answer only when a claims check passed it and none failed.
// Otherwise it prints the fixed reply, never the answer, and returns the exit code.
func refuseUncovered(checks []answerCheck, esc *decide.OnFail) error {
	all := esc.Verdicts()
	served := all[len(all)-len(checks):]
	var failed []string
	passed := false
	for k, c := range checks {
		j := served[k]
		switch {
		case !c.claims || !j.Checked:
		case j.Passed:
			passed = true
		default:
			failed = append(failed, c.source+": "+j.Detail)
		}
	}
	if len(failed) > 0 {
		fmt.Fprintf(stdout, "\n%s\n\n", notCoveredReply)
		for _, f := range failed {
			fmt.Fprintf(stdout, "  %s\n", dimStyle.Render(f))
		}
		fmt.Fprintln(stdout)
		return exitStatus(groundUnsupported)
	}
	if !passed {
		fmt.Fprintf(stdout, "\n%s\n\n", "the reply could not be checked against the documents, so it is not shown")
		return exitStatus(groundNotChecked)
	}
	return nil
}
