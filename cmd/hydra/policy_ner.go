// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/entity"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/provider"
)

// cmdPolicyNER measures a head at the half of PII detection no pattern reaches.
//
// The saved report is what lets `dispatch --redact=names` ask a local head:
// only one measured eligible on this machine, never one on vibes (#901, #1881).
func cmdPolicyNER() *cobra.Command {
	var headID string
	var jsonOut bool
	var maxTokens int

	cmd := &cobra.Command{
		Use:   "ner",
		Short: "Measure a head at detecting the PII no pattern can find",
		Long: `hyctl policy ner asks a head about 470 labelled texts and reports what it got
right, so its answers can be trusted or refused on evidence.

The regex detectors cover formatted identifiers: cards, SSNs, emails, phone
numbers, IBANs. Names, street addresses and organisations have no format, and on
the presidio-research set they are 1,509 spans no pattern reaches. A head can
read them, but only some heads can: measured here, a 7B scored 0.87 recall at no
false positives, while a 0.5B scored 0.86 recall and said yes to 94% of the
negatives, clearing a recall bar by answering yes to nearly everything.

Both halves are reported because each alone is trivially passed, and both are
judged on the 95% interval rather than the point estimate, so a head is eligible
only when the sample is large enough to say so. Decoding is pinned greedy, or
the same head measures differently on every run.

An answer that is neither yes nor no is counted apart and never read as a no: a
detector that reports clean because the head malfunctioned is worse than none.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			d, err := dispatch.New(ctx)
			if err != nil {
				return err
			}
			head, err := pickHead(d.Heads(), headID)
			if err != nil {
				return err
			}

			ask := dispatch.HeadAsk(head, maxTokens)
			started := time.Now()
			rep, err := entity.Measure(ctx, ask)
			if err != nil {
				return err
			}
			// Kept per head, so --redact names can ask only a head measured eligible here.
			if err := entity.SaveReport(dispatch.NERReportPath(), head.ID, rep, time.Now()); err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(map[string]any{
					"head": head.ID, "recall": rep.Recall(), "recall_low": rep.RecallLow(),
					"false_positive_rate": rep.FalsePositiveRate(),
					"false_positive_high": rep.FalsePositiveHigh(),
					"positives":           rep.Positives, "recalled": rep.Recalled,
					"negatives": rep.Negatives, "false_positives": rep.FalsePos,
					"unreadable": rep.Unreadable, "failed": rep.Failed,
					"eligible": rep.Eligible(), "elapsed_ms": time.Since(started).Milliseconds(),
				})
			}
			printNER(head.ID, rep, time.Since(started))
			return nil
		},
	}
	cmd.Flags().StringVar(&headID, "head", "", "head to measure (default: the first local one)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	cmd.Flags().IntVar(&maxTokens, "max-tokens", 256, "output budget per answer")
	return cmd
}

// pickHead resolves --head, defaulting to a local one.
//
// Local by default because this sends 100 texts from a PII corpus somewhere, and
// the command whose subject is privacy must not quietly pick a head that leaves
// the machine.
func pickHead(heads []provider.Head, want string) (provider.Head, error) {
	if want != "" {
		for _, h := range heads {
			if h.ID == want {
				return h, nil
			}
		}
		return provider.Head{}, fmt.Errorf("no head %q, see: hyctl probe", want)
	}
	for _, h := range heads {
		if h.LocalOnly && executor.Unroutable(h) == "" {
			return h, nil
		}
	}
	return provider.Head{}, fmt.Errorf("no routable local head, name one with --head, see: hyctl probe")
}

func printNER(id string, r entity.Report, took time.Duration) {
	verdict := "not eligible"
	if r.Eligible() {
		verdict = "eligible"
	}
	fmt.Fprintf(stdout, "\n  %s\n\n", id)
	fmt.Fprintf(stdout, "    recall            %8s   %.2f   95%% low  %.2f   (bar %.2f)\n",
		fmt.Sprintf("%d/%d", r.Recalled, r.Positives), r.Recall(), r.RecallLow(), entity.MinRecall)
	fmt.Fprintf(stdout, "    false positives   %8s   %.2f   95%% high %.2f   (bar %.2f)\n",
		fmt.Sprintf("%d/%d", r.FalsePos, r.Negatives), r.FalsePositiveRate(), r.FalsePositiveHigh(), entity.MaxFalsePositive)
	if r.Unreadable > 0 {
		fmt.Fprintf(stdout, "    unreadable        %8d   answered neither yes nor no\n", r.Unreadable)
	}
	if r.Failed > 0 {
		fmt.Fprintf(stdout, "    unreachable       %8d\n", r.Failed)
	}
	fmt.Fprintf(stdout, "\n    %s", verdict)
	if !r.Eligible() {
		fmt.Fprintf(stdout, ", so its answers are not evidence")
	}
	fmt.Fprintf(stdout, "   (%s)\n\n", took.Round(time.Millisecond))
	fmt.Fprintf(stdout, "  Saved. `hyctl dispatch --redact=names` asks only a local model eligible here.\n\n")
}

func cmdPolicy() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "policy",
		Short: "Inspect and measure the routing policy's detectors",
	}
	cmd.AddCommand(cmdPolicyNER())
	return cmd
}
