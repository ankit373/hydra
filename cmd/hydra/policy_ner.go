// SPDX-License-Identifier: MIT

package main

import (
	"context"
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
// A command rather than something a dispatch does, and nothing routes on the
// result yet: #901 is explicit that a measured classifier must not ship on
// vibes, and this is the measurement that would earn it.
func cmdPolicyNER() *cobra.Command {
	var headID string
	var jsonOut bool
	var maxTokens int

	cmd := &cobra.Command{
		Use:   "ner",
		Short: "Measure a head at detecting the PII no pattern can find",
		Long: `hyctl policy ner asks a head about 100 labelled texts and reports what it got
right, so its answers can be trusted or refused on evidence.

The regex detectors cover formatted identifiers: cards, SSNs, emails, phone
numbers, IBANs. Names, street addresses and organisations have no format, and on
the presidio-research set they are 1,509 spans no pattern reaches. A head can
read them, but only some heads can: measured here, one model scored 0.88 recall
at no false positives while another scored the same recall and said yes to 85%
of ordinary commit messages, which would force local-only routing on normal work.

Both halves are reported because each alone is trivially passed. A head that
answers NO to everything has a flawless false-positive rate, and one that answers
YES to everything has perfect recall.

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

			ask := headAsk(head, maxTokens)
			started := time.Now()
			rep, err := entity.Measure(ctx, ask)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(map[string]any{
					"head": head.ID, "recall": rep.Recall(),
					"false_positive_rate": rep.FalsePositiveRate(),
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

// headAsk sends one question to one head. Deliberately not a dispatch: naming a
// head is the whole point, so routing would defeat it.
func headAsk(head provider.Head, maxTokens int) entity.Ask {
	exec := executor.For(head)
	return func(ctx context.Context, system, text string) (string, error) {
		resp, err := exec.Execute(ctx, executor.Request{
			Prompt: text, System: system, Head: head, MaxTokens: maxTokens,
		})
		if err != nil {
			return "", err
		}
		return resp.Output, nil
	}
}

func printNER(id string, r entity.Report, took time.Duration) {
	verdict := "not eligible"
	if r.Eligible() {
		verdict = "eligible"
	}
	fmt.Printf("\n  %s\n\n", id)
	fmt.Printf("    recall            %7s   %.2f   (bar %.2f)\n",
		fmt.Sprintf("%d/%d", r.Recalled, r.Positives), r.Recall(), entity.MinRecall)
	fmt.Printf("    false positives   %7s   %.2f   (bar %.2f)\n",
		fmt.Sprintf("%d/%d", r.FalsePos, r.Negatives), r.FalsePositiveRate(), entity.MaxFalsePositive)
	if r.Unreadable > 0 {
		fmt.Printf("    unreadable        %7d           answered neither yes nor no\n", r.Unreadable)
	}
	if r.Failed > 0 {
		fmt.Printf("    unreachable       %7d\n", r.Failed)
	}
	fmt.Printf("\n    %s", verdict)
	if !r.Eligible() {
		fmt.Printf(", so its answers are not evidence")
	}
	fmt.Printf("   (%s)\n\n", took.Round(time.Millisecond))
	fmt.Printf("  Nothing routes on this yet. It is the measurement that would earn it.\n\n")
}

func cmdPolicy() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "policy",
		Short: "Inspect and measure the routing policy's detectors",
	}
	cmd.AddCommand(cmdPolicyNER())
	return cmd
}
