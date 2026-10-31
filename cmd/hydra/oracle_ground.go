// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ankit373/hydra/internal/ground"
	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/trust"
)

// cmdOracleGround checks an answer against the context its prompt carried.
//
// Deliberately a command rather than something every dispatch runs. A verdict
// written automatically would land on the span as a score, and
// internal/reliability reads span scores to judge whether Hydra's stated
// confidence is honest; feeding it an unmeasured check would change what that
// number means without anyone choosing to. Evidence is asked for here, not
// produced as a side effect.
func cmdOracleGround() *cobra.Command {
	var promptFile, candidateFile, source, domain, record, scoreRun, scoreSpan string
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "ground",
		Short: "Check an answer against the context its prompt carried",
		Long: `hyctl oracle ground verifies that an answer's claims appear in the context the
head was given, and reports the result as calibrated evidence.

It answers one narrow question: does the answer state a number, path, flag or
symbol that appears nowhere in the prompt? That is extrinsic hallucination, a
model contradicting ground truth sitting in its own context, and it is the part
of the problem a check with no trained model can speak to.

It declines everything else. A prompt with no fenced context produces no verdict
at all, and neither does an answer that states nothing checkable: "nothing to
check" and "checked and fine" are different facts and only one is evidence.

Never blocks and never rewrites. The verdict is evidence; what to do about it is
the caller's decision.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if promptFile == "" || candidateFile == "" {
				return errors.New("both --prompt and --candidate are required")
			}
			promptRaw, err := os.ReadFile(promptFile)
			if err != nil {
				return err
			}
			candidateRaw, err := os.ReadFile(candidateFile)
			if err != nil {
				return err
			}

			var recordOutcome trust.Outcome
			if record != "" {
				recordOutcome = trust.ParseOutcome(record)
				if recordOutcome == trust.OutcomeUnknown {
					return fmt.Errorf("--record must be correct|incorrect (got %q)", record)
				}
			}

			o := &ground.Oracle{Prompt: string(promptRaw), Source: source}
			v, verr := o.Verify(cmd.Context(), string(candidateRaw), trust.Task{Domain: domain})
			src := o.Key()

			// Not an error the command fails on: "there was nothing to check"
			// is the honest answer to the question that was asked, and exiting
			// non-zero would make a caller treat it as a failed check.
			if errors.Is(verr, ground.ErrNoContext) || errors.Is(verr, ground.ErrNoClaims) {
				if jsonOut {
					return printJSON(map[string]any{
						"source": src, "checked": false, "reason": verr.Error(),
						"verdict": o.Last,
					})
				}
				fmt.Printf("\n  %s  %s\n", warnStyle.Render("NOT CHECKED"), dimStyle.Render(src))
				fmt.Printf("  %s\n\n", dimStyle.Render(verr.Error()))
				return nil
			}
			if verr != nil {
				return verr
			}

			cal, err := trust.New(trust.DefaultPath())
			if err != nil {
				return err
			}
			if record != "" {
				_ = cal.Update(src, domain, v.Passed, recordOutcome)
			}
			measured, why := oracle.Measured(cal, src, domain)
			llr := oracle.LLR(cal, src, domain, v)

			if scoreSpan != "" {
				if err := scoreGrounding(scoreRun, scoreSpan, src, v.Passed, v.Detail); err != nil {
					fmt.Printf("  %s\n", dimStyle.Render("span score: "+err.Error()))
				} else {
					fmt.Printf("  %s\n", dimStyle.Render("scored span "+scoreSpan))
				}
			}

			if jsonOut {
				out := map[string]any{
					"source": src, "checked": true, "grounded": v.Passed, "verdict": o.Last,
				}
				if measured {
					out["llr_nats"] = llr
				} else {
					out["insufficient_evidence"] = why
				}
				return printJSON(out)
			}

			status := cortexStyle.Render("GROUNDED")
			if !v.Passed {
				status = warnStyle.Render("UNSUPPORTED")
			}
			fmt.Printf("\n  %s  %s\n", status, dimStyle.Render(src))
			fmt.Printf("  %s\n", dimStyle.Render(v.Detail))
			fmt.Printf("  %s\n", dimStyle.Render(fmt.Sprintf(
				"%d claims checked against %d bytes of context · %.0f%% of the answer's words appear in it",
				o.Last.Claims, o.Last.Context, o.Last.Overlap*100)))
			if measured {
				fmt.Printf("  calibrated evidence  %+.3f nats\n\n", llr)
			} else {
				// A number here would be the prior wearing a measurement's
				// clothes, which is the one thing an evidence source must not do.
				fmt.Printf("  %s\n", warnStyle.Render("insufficient evidence to state a strength"))
				fmt.Printf("  %s\n\n", dimStyle.Render(why+
					"; train it with --record once you know whether the answer was right"))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&promptFile, "prompt", "", "file holding the prompt the head was given, fences and all")
	cmd.Flags().StringVar(&candidateFile, "candidate", "", "file holding the answer to check")
	cmd.Flags().StringVar(&source, "source", "", "calibration source id (default: "+ground.DefaultSource+")")
	cmd.Flags().StringVar(&domain, "domain", "", "task domain")
	cmd.Flags().StringVar(&record, "record", "", "train calibration with the true outcome: correct|incorrect")
	cmd.Flags().StringVar(&scoreRun, "run", "", "run holding the span to score (default: newest)")
	cmd.Flags().StringVar(&scoreSpan, "span", "", "span this verdict judges, so `hyctl trace view` can show it")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	return cmd
}

// scoreGrounding attaches the verdict to the span that produced the answer, so
// a failing check is visible in `hyctl trace view` beside the work it judges.
func scoreGrounding(runID, spanID, source string, passed bool, detail string) error {
	if runID == "" {
		runs, err := runlog.Runs()
		if err != nil || len(runs) == 0 {
			return errors.New("--span given but no run to attach it to; pass --run")
		}
		runID = runs[0]
	}
	val := 0.0
	if passed {
		val = 1
	}
	return runlog.AppendScore(runID, spanID, runlog.Score{
		Name: "grounding", Value: val, Comment: detail, Source: source,
	})
}

func printJSON(v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(raw))
	return nil
}
