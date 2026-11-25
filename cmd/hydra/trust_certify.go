// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ankit373/hydra/internal/aver"
	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/trust"
)

func cmdTrustCertifyTable() *cobra.Command {
	var kind string
	var delta float64
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "certify-table",
		Short: "Each check's bound on passing a wrong answer, per model and kind of work",
		Long: `hyctl trust certify-table reads the verdict log and prints, for each check, model
and kind of work, how often the check passed a wrong answer from that model and an
upper bound on that rate (q-bar). Certified acceptance is built on these bounds.

The bound is calibrated per model and never pooled: a test suite can pass a weak
model's wrong answers rarely and a strong model's often, because the strong model's
mistakes are subtler. Pooling them would make the bound too optimistic for the
strong model. A model with few wrong answers gets a wide bound, which is valid and
simply counts for little.

s is how often the check passed a right answer from that model. A pass is worth
s / q-bar. When s is not above q-bar the check is marked skip and its verdict
counts for nothing, since it does not separate right answers from wrong ones.

Only answers with ground truth count. Models that voted in an ensemble run count
as checks too.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			joint, err := evalset.Joint(evalset.VerdictsPath())
			if err != nil {
				return err
			}
			runs, err := trust.LoadRuns(trust.DefaultLogPath())
			if err != nil {
				return err
			}
			joint, _ = evalset.AddModelVotes(joint, runs)
			answers := averAnswers(joint)
			if kind != "" {
				kept := answers[:0]
				for _, a := range answers {
					if a.Kind == kind {
						kept = append(kept, a)
					}
				}
				answers = kept
			}
			table, err := aver.Fit(answers, delta)
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(table)
			}
			renderCertifyTable(table)
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "one kind of work only")
	cmd.Flags().Float64Var(&delta, "delta", aver.DefaultDelta, "chance that any bound in a kind is wrong, split over its checks")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	return cmd
}

func renderCertifyTable(t aver.Table) {
	if len(t.Cells) == 0 {
		fmt.Fprintln(stdout, "\n  No answers with ground truth yet.")
		fmt.Fprintln(stdout, "  Record truth with `hyctl review reject`, `hyctl trust outcome` or `hyctl trace score`.")
		fmt.Fprintln(stdout)
		return
	}
	kind := ""
	for _, c := range t.Cells {
		if c.Kind != kind {
			kind = c.Kind
			fmt.Fprintf(stdout, "\n  %s  ·  delta %.2g\n", kind, t.Delta)
			fmt.Fprintf(stdout, "  %-28s %-24s %6s %6s %7s %6s %9s\n", "CHECK", "MODEL", "WRONG", "PASSED", "Q-BAR", "S", "E ON PASS")
			fmt.Fprintln(stdout, "  "+strings.Repeat("─", 92))
		}
		e := fmt.Sprintf("%.2f", c.EPass())
		if c.Skip {
			e = "skip"
		}
		fmt.Fprintf(stdout, "  %-28.28s %-24.24s %6d %6d %7.3f %6.3f %9s\n",
			c.Check, c.Head, c.Wrong, c.FalsePass, c.QBar, c.S, e)
	}
	fmt.Fprintln(stdout)
}

// averAnswers keeps the verdict log's answers that have ground truth. A missing
// kind or head is "unknown", as hyctl trust joint names a missing domain.
func averAnswers(joint []evalset.Answer) []aver.Answer {
	var out []aver.Answer
	for _, a := range joint {
		if a.Truth == nil {
			continue
		}
		out = append(out, aver.Answer{
			Kind:     orUnknown(a.Domain),
			Head:     orUnknown(a.Head),
			Correct:  a.Truth.Outcome == evalset.OutcomeCorrect,
			Verdicts: a.ByCheck(),
		})
	}
	return out
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
