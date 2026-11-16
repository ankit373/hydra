// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/ground"
	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/trust"
)

// Exit codes of `hyctl oracle ground`, the same as hyverify's.
const (
	groundUnsupported = 1
	groundNotChecked  = 2
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
	var jsonOut, bySentence bool

	cmd := &cobra.Command{
		Use:   "ground",
		Short: "Check an answer against the context its prompt carried",
		Long: `hyctl oracle ground verifies that an answer's claims appear in the context the
head was given, and reports the result as calibrated evidence.

It answers one narrow question: does the answer state a number, date, name,
path, flag or symbol, or quote a phrase, that appears nowhere in the prompt?
That is extrinsic hallucination, a model contradicting ground truth sitting in
its own context, and it is the part of the problem a check with no trained
model can speak to.

It declines everything else. A prompt with no fenced context produces no verdict
at all, and neither does an answer that states nothing checkable: "nothing to
check" and "checked and fine" are different facts and only one is evidence.

Never rewrites. The exit code is the verdict, so a script can gate on it:
0 grounded, 1 unsupported, 2 not checked. A failed run (a missing file, a bad
flag) also exits 1.

--by-sentence prints each sentence numbered with "supported [<block> L<lines>]",
"NOT SUPPORTED: <what>" or "nothing to check"; with --json it prints
{"v":1,"sentences":[{"n","text","status","unsupported","sources":[{"label",
"start_line","end_line"}]}],"supported","not_supported","nothing_to_check"}.
It only reports: no calibration, span score or eval-set entry.`,
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
			if bySentence {
				if record != "" || scoreSpan != "" {
					return errors.New("--by-sentence only reports; drop --record and --span")
				}
				return groundBySentence(cmd.Context(), string(promptRaw), string(candidateRaw), jsonOut)
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

			// Its own code, as hyverify's: 0 would read "not checked" as a pass
			// and 1 as a failed check, and it is neither.
			if errors.Is(verr, ground.ErrNoContext) || errors.Is(verr, ground.ErrNoClaims) {
				if jsonOut {
					if err := printJSON(map[string]any{
						"source": src, "checked": false, "reason": verr.Error(),
						"verdict": o.Last,
					}); err != nil {
						return err
					}
				} else {
					printNotChecked(src, verr)
				}
				return exitStatus(groundNotChecked)
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

			var spanEv runlog.Event
			if scoreSpan != "" {
				ev, err := scoreVerdict(scoreRun, scoreSpan, "grounding", src, v.Passed, v.Detail)
				if err != nil {
					fmt.Fprintf(stdout, "  %s\n", dimStyle.Render("span score: "+err.Error()))
				} else {
					fmt.Fprintf(stdout, "  %s\n", dimStyle.Render("scored span "+scoreSpan))
				}
				spanEv = ev
			}
			fileVerdict(spanEv, evalset.Verdict{
				CandidateHash: candidateHash(string(candidateRaw)),
				Domain:        domain, Check: src, Passed: v.Passed, Detail: v.Detail,
			})

			if jsonOut {
				out := map[string]any{
					"source": src, "checked": true, "grounded": v.Passed, "verdict": o.Last,
				}
				if measured {
					out["llr_nats"] = llr
				} else {
					out["insufficient_evidence"] = why
				}
				if err := printJSON(out); err != nil {
					return err
				}
			} else {
				printGrounding(src, o.Last, v, measured, why, llr)
			}
			if !v.Passed {
				return exitStatus(groundUnsupported) // the same code as `oracle verify` on FAIL
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
	cmd.Flags().BoolVar(&bySentence, "by-sentence", false, "check one sentence at a time and show each with the source lines that back it")
	return cmd
}

// printNotChecked says the check had nothing to judge or could not run. Never a pass.
func printNotChecked(src string, reason error) {
	fmt.Fprintf(stdout, "\n  %s  %s\n", warnStyle.Render("NOT CHECKED"), dimStyle.Render(src))
	fmt.Fprintf(stdout, "  %s\n\n", dimStyle.Render(reason.Error()))
}

// printGrounding prints a grounding verdict, its unsupported spans and its
// calibrated strength, or why no strength can be stated yet.
func printGrounding(src string, last ground.Verdict, v oracle.Verdict, measured bool, why string, llr float64) {
	status := cortexStyle.Render("GROUNDED")
	if !v.Passed {
		status = warnStyle.Render("UNSUPPORTED")
	}
	fmt.Fprintf(stdout, "\n  %s  %s\n", status, dimStyle.Render(src))
	fmt.Fprintf(stdout, "  %s\n", dimStyle.Render(v.Detail))
	fmt.Fprintf(stdout, "  %s\n", dimStyle.Render(fmt.Sprintf(
		"%d claims checked against %d bytes of context · %.0f%% of the answer's words appear in it",
		last.Claims, last.Context, last.Overlap*100)))
	if measured {
		fmt.Fprintf(stdout, "  calibrated evidence  %+.3f nats\n\n", llr)
		return
	}
	// A number here would be the prior wearing a measurement's clothes.
	fmt.Fprintf(stdout, "  %s\n", warnStyle.Render("insufficient evidence to state a strength"))
	fmt.Fprintf(stdout, "  %s\n\n", dimStyle.Render(why+
		"; train it with `hyctl oracle ground --record` once you know whether the answer was right"))
}

// scoreVerdict attaches a verdict to the span that produced the answer, so a
// failing check is visible in `hyctl trace view` beside the work it judges.
func scoreVerdict(runID, spanID, name, source string, passed bool, detail string) (runlog.Event, error) {
	ev, err := loadSpan(runID, spanID)
	if err != nil {
		return ev, err
	}
	val := 0.0
	if passed {
		val = 1
	}
	return ev, runlog.AppendScore(ev.RunID, ev.SpanID, runlog.Score{
		Name: name, Value: val, Comment: detail, Source: source,
	})
}

func printJSON(v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, string(raw))
	return nil
}
