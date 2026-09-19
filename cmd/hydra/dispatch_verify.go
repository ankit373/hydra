// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"fmt"

	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/swarm"
	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/internal/verify"
)

// verifyAndScoreRun runs the workspace verifier against a finished confidence
// run and records the verdict in both places that read it: the run log, which
// `hyctl trust reliability` joins to the confidence the run stated, and the
// calibrator, which `ApplyRunOutcome` trains from the whole ledger so the
// sources that disagreed are scored too (#843).
//
// Nothing here fails the dispatch. The answer is the work; a verifier that
// cannot run, or that fails, is a verdict about the answer, not an error in
// producing it.
func verifyAndScoreRun(ctx context.Context, res *swarm.SPRTResult, runID, taskID, file, domain string) {
	argv, label := verify.Command(file)
	if len(argv) == 0 {
		fmt.Printf("  %s\n", warnStyle.Render(
			"--verify: no verifier configured for this work, so the run stays unjudged. "+
				"Add a validator to registry/workspace.yaml, or score it by hand with `hyctl trace score`."))
		return
	}

	src := "verifier:" + label
	fmt.Printf("  %s\n", dimStyle.Render("verifying with "+label))
	v, err := (&oracle.CommandOracle{Args: argv, Source: src}).Verify(ctx, res.Trust.Candidate, trust.Task{Domain: domain})
	if err != nil {
		fmt.Printf("  %s\n", warnStyle.Render("--verify: "+err.Error()+"; the run stays unjudged"))
		return
	}

	outcome := trust.OutcomeIncorrect
	value := 0.0
	mark := warnStyle.Render("✘")
	if v.Passed {
		outcome, value, mark = trust.OutcomeCorrect, 1, okStyle.Render("✔")
	}
	fmt.Printf("  %s %s\n", mark, label)
	if !v.Passed && v.Detail != "" {
		fmt.Printf("    %s\n", dimStyle.Render(v.Detail))
	}

	// No corpus example is filed here. The verifier is the repo's own suite and
	// a dispatch applies nothing, so this verdict is about the working tree and
	// not about the candidate; filing it labelled the corpus with an unrelated
	// outcome (#982). Ground truth needs the answer applied first, which is
	// `hyctl edit`, not this path.
	if err := runlog.AppendScore(runID, swarm.SPRTSpanID(taskID), runlog.Score{
		Name: "verify", Value: value, Comment: v.Detail, Source: src,
	}); err != nil {
		fmt.Printf("  %s\n", dimStyle.Render("span score: "+err.Error()))
	}

	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		fmt.Printf("  %s\n", dimStyle.Render("calibration: "+err.Error()))
		return
	}
	n, err := trust.ApplyRunOutcome(cal, trust.Domain(domain), res.Trust.Ledger, outcome)
	if err != nil {
		fmt.Printf("  %s\n", dimStyle.Render("calibration: "+err.Error()))
		return
	}
	fmt.Printf("  %s\n", dimStyle.Render(fmt.Sprintf(
		"trained %d source(s) in %q · hyctl trust calibration", n, trust.Domain(domain))))
}
