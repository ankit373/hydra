// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/evalset"
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

	if err := runlog.AppendScore(runID, swarm.SPRTSpanID(taskID), runlog.Score{
		Name: "verify", Value: value, Comment: v.Detail, Source: src,
	}); err != nil {
		fmt.Printf("  %s\n", dimStyle.Render("span score: "+err.Error()))
	}

	// An oracle verdict on a real candidate is a labelled example, and the
	// rarest thing Hydra produces. Filed before calibration, which returns early
	// on its own errors and would otherwise drop the corpus entry with it (#969).
	fileExample(res, v, src, domain)

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

// fileExample records the verified answer in the eval set, the only corpus the
// router can be improved against. Until #969 this path computed ground truth and
// discarded it, so the store had one writer: `hyctl oracle verify --candidate`.
func fileExample(res *swarm.SPRTResult, v oracle.Verdict, src, domain string) {
	cand := res.Trust.Candidate
	if strings.TrimSpace(cand) == "" {
		return
	}
	breadcrumb, _ := config.Breadcrumb()
	added, err := evalset.Add(evalset.DefaultPath(), evalset.Example{
		Domain: domain, Source: src, Candidate: cand,
		Passed: v.Passed, Detail: v.Detail, Config: breadcrumb,
		Enum: res.Enum, Tier: res.Tier, Head: soleAuthor(res.Attempts, cand),
	})
	switch {
	case err != nil:
		// Never fail a verification because its example could not be filed;
		// report it, so the loss is visible rather than silent.
		fmt.Printf("  %s\n", dimStyle.Render("eval set: "+err.Error()))
	case added:
		fmt.Printf("  %s\n", dimStyle.Render("recorded to the eval set"))
	}
}

// soleAuthor names the head whose output *is* the verified answer, and only when
// one head produced it. Agreement is the point of an ensemble, so two heads
// returning byte-identical text leaves the example genuinely unattributed rather
// than credited to whichever was sampled first. Answers judged equivalent but
// textually different are not this case: the text that was verified had one author.
func soleAuthor(attempts []swarm.Attempt, candidate string) string {
	id := ""
	for _, a := range attempts {
		if a.Output != candidate {
			continue
		}
		if id != "" {
			return ""
		}
		id = a.Head.ID
	}
	return id
}
