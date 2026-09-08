// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/ankit373/hydra/internal/reliability"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/spf13/cobra"
)

// The diagram's bars share trace_view's barWidth on purpose, so a reliability
// bar and a timeline bar printed in the same terminal are the same width.

// reliabilityObservations pairs each confidence-routed run's stated confidence
// with whatever later judged it.
//
// Both halves were already recorded and nothing joined them: swarm writes the
// SPRT run's final confidence on its root span, and `hyctl trace score` (or an
// oracle) appends a verdict to that same span. unscored is returned so the
// command can say how much of the log is waiting on a verdict rather than
// silently reporting on a subset.
func reliabilityObservations() (obs []reliability.Observation, unscored int, err error) {
	runs, err := runlog.Runs()
	if err != nil {
		return nil, 0, err
	}
	for _, id := range runs {
		events, lerr := runlog.Load(id)
		if lerr != nil {
			continue // a single unreadable run must not sink the report
		}
		for _, e := range events {
			// Confidence is omitempty, so a non-zero value is what marks a
			// task as confidence-routed; every other task finishes without one.
			if e.Kind != runlog.KindTaskFinished || e.Confidence <= 0 || e.SpanID == "" {
				continue
			}
			passed, known := runlog.Verdict(runlog.Scores(events, e.SpanID))
			if !known {
				unscored++
				continue
			}
			obs = append(obs, reliability.Observation{Predicted: e.Confidence, Correct: passed})
		}
	}
	return obs, unscored, nil
}

func cmdTrustReliability() *cobra.Command {
	var bins int
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "reliability",
		Short: "Is a stated confidence honest? Brier, ECE and a reliability diagram",
		Long: `hyctl trust reliability answers "when Hydra says 90%, is it right 90% of the time".

This is the calibration of the *output*, unlike ` + "`hyctl trust calibration`" + `, which
reports the per-source sensitivity and specificity that feed it. It reads every
confidence-routed run that something has since judged, pairing the confidence the
run stated with the verdict, so it needs verdicts: record them with
` + "`hyctl trace score`" + ` or an oracle.

Brier decomposes as reliability - resolution + uncertainty. Reliability is the
miscalibration and lower is better; resolution is how much the confidences
actually discriminate and higher is better. A router that always states the base
rate is perfectly reliable and completely useless, which is why both are shown.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			obs, unscored, err := reliabilityObservations()
			if err != nil {
				return err
			}
			rep, err := reliability.Evaluate(obs, bins)
			if err != nil {
				if jsonOut {
					return json.NewEncoder(os.Stdout).Encode(map[string]any{
						"scored": len(obs), "unscored": unscored, "error": err.Error(),
					})
				}
				fmt.Printf("\n  %d scored run(s), %d awaiting a verdict.\n", len(obs), unscored)
				fmt.Printf("  %v.\n\n  Attach verdicts with `hyctl trace score <run-id> --span <id> --name tests --value 1`,\n"+
					"  then this reports whether the confidence Hydra stated held up.\n\n", err)
				return nil
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(struct {
					reliability.Report
					Unscored int `json:"unscored"`
				}{rep, unscored})
			}
			renderReliability(rep, unscored)
			return nil
		},
	}
	cmd.Flags().IntVar(&bins, "bins", reliability.DefaultBins, "number of equal-width confidence bins")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	return cmd
}

func renderReliability(rep reliability.Report, unscored int) {
	fmt.Printf("\n  %d scored run(s)", rep.N)
	if unscored > 0 {
		fmt.Printf("  ·  %s", dimStyle.Render(fmt.Sprintf("%d awaiting a verdict", unscored)))
	}
	fmt.Printf("  ·  %.1f%% correct overall\n\n", rep.BaseRate*100)

	fmt.Printf("  %-14s %8s  %s\n", "STATED", "RUNS", "ACTUALLY CORRECT")
	fmt.Println("  " + strings.Repeat("─", 62))
	for _, b := range rep.Bins {
		filled := int(math.Round(b.Observed * barWidth))
		cells := make([]rune, barWidth)
		for i := range cells {
			cells[i] = '·'
			if i < filled {
				cells[i] = '█'
			}
		}
		// The stated rate is marked on the bar, so a bin reads as calibrated or
		// not without the reader doing the subtraction. Indexed by rune: every
		// cell here is multi-byte.
		mark := int(math.Round(b.MeanPredicted * barWidth))
		if mark >= barWidth {
			mark = barWidth - 1
		}
		cells[mark] = '┃'
		bar := string(cells)
		style := okStyle
		if math.Abs(b.Gap()) > 0.1 {
			style = warnStyle
		}
		fmt.Printf("  %5.0f-%-3.0f%% %8d  %s %s\n",
			b.Lo*100, b.Hi*100, b.N, style.Render(bar),
			style.Render(fmt.Sprintf("%5.1f%%  (%+.1f)", b.Observed*100, b.Gap()*100)))
	}
	fmt.Printf("\n  %s\n", dimStyle.Render("┃ marks the stated confidence; the bar is what actually happened"))

	fmt.Printf("\n  brier %.4f  =  reliability %.4f  -  resolution %.4f  +  uncertainty %.4f\n",
		rep.Brier, rep.Reliability, rep.Resolution, rep.Uncertainty)
	fmt.Printf("  ECE %.4f   worst bin %.4f\n", rep.ECE, rep.MCE)

	verdict := okStyle.Render("confidences track outcomes")
	if gap := rep.SignedGap(); gap > 0.05 {
		verdict = warnStyle.Render(fmt.Sprintf(
			"overconfident by %.1f points on average: a stated confidence is worth less than it says", gap*100))
	} else if gap < -0.05 {
		verdict = warnStyle.Render(fmt.Sprintf(
			"underconfident by %.1f points on average: runs are sampling more than they need to", -gap*100))
	}
	fmt.Printf("  %s\n", verdict)
	if rep.Resolution < 0.01 {
		fmt.Printf("  %s\n", warnStyle.Render(
			"resolution near zero: the confidences barely discriminate, so being well calibrated means little here"))
	}
	fmt.Println()
}
