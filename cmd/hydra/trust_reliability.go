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
	"github.com/ankit373/hydra/internal/trust"
	"github.com/spf13/cobra"
)

// calibrationSlack is how far the average stated probability may sit from the
// observed rate before a report calls it over- or underconfident.
const calibrationSlack = 0.05

// The diagram's bars share trace_view's barWidth on purpose, so a reliability
// bar and a timeline bar printed in the same terminal are the same width.

// reliabilityLog is what the report joins out of the run log: Hydra's own
// confidence-routed runs, and every decision source's answers by domain.
type reliabilityLog struct {
	hydra   reliability.Tally
	sources map[reliability.Key]*reliability.Tally
}

// readReliabilityLog pairs each stated probability with whatever later judged
// its span. source, when set, keeps only that head's decision answers.
//
// Both halves were already recorded and nothing joined them: swarm writes the
// SPRT run's final confidence on its root span, a decision source's answer is
// its own span, and `hyctl trace score` (or an oracle) appends a verdict to it.
func readReliabilityLog(source string) (reliabilityLog, error) {
	rec := reliabilityLog{sources: map[reliability.Key]*reliability.Tally{}}
	runs, err := runlog.Runs()
	if err != nil {
		return rec, err
	}
	for _, id := range runs {
		events, lerr := runlog.Load(id)
		if lerr != nil {
			continue // a single unreadable run must not sink the report
		}
		verdicts := runlog.ScoresBySpan(events)
		for _, e := range events {
			tally := rec.tallyFor(e, source)
			if tally == nil {
				continue
			}
			passed, known := runlog.Verdict(verdicts[e.SpanID])
			if !known {
				tally.Unscored++
				continue
			}
			tally.Observations = append(tally.Observations, reliability.Observation{
				Predicted: e.Confidence,
				Correct:   passed,
				Ref:       fmt.Sprintf("run %s span %s", id, e.SpanID),
			})
		}
	}
	return rec, nil
}

// tallyFor is where an event's stated probability is counted, or nil when it
// states none this report reads.
func (l *reliabilityLog) tallyFor(e runlog.Event, source string) *reliability.Tally {
	switch {
	case e.SpanID == "":
		return nil
	// Confidence is omitempty, so a non-zero value is what marks a task as
	// confidence-routed; every other task finishes without one.
	case e.Kind == runlog.KindTaskFinished && e.Confidence > 0:
		return &l.hydra
	case e.Kind == runlog.KindDecision && (source == "" || e.Head == source):
		key := reliability.Key{Source: e.Head, Domain: trust.Domain(e.Domain)}
		if l.sources[key] == nil {
			l.sources[key] = &reliability.Tally{}
		}
		return l.sources[key]
	}
	return nil
}

func cmdTrustReliability() *cobra.Command {
	var bins int
	var jsonOut bool
	var source string
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
rate is perfectly reliable and completely useless, which is why both are shown.

Decision sources state a probability with every answer, and the same question
applies to them on your own work. Every source is listed side by side per domain;
--source <head> reports that one alone, with its full decomposition in --json.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			rec, err := readReliabilityLog(source)
			if err != nil {
				return err
			}
			sources := reliability.EvaluateSources(rec.sources, bins)
			if source != "" {
				return reportSource(source, sources, jsonOut)
			}
			return reportHydra(rec.hydra, sources, bins, jsonOut)
		},
	}
	cmd.Flags().IntVar(&bins, "bins", reliability.DefaultBins, "number of equal-width confidence bins")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	cmd.Flags().StringVar(&source, "source", "",
		"report one decision source (a head id) per domain")
	return cmd
}

// reportHydra reports Hydra's own stated confidence, then every decision source
// beside it.
func reportHydra(own reliability.Tally, sources []reliability.SourceReport, bins int, jsonOut bool) error {
	scored, unscored := len(own.Observations), own.Unscored
	rep, err := reliability.Evaluate(own.Observations, bins)
	if err != nil {
		if jsonOut {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{
				"scored": scored, "unscored": unscored, "error": err.Error(), "sources": sources,
			})
		}
		fmt.Fprintf(stdout, "\n  %d scored run(s), %d awaiting a verdict.\n", scored, unscored)
		fmt.Fprintf(stdout, "  %v.\n\n", err)
		fmt.Fprintln(stdout, "  Attach verdicts with `hyctl trace score <run-id> --span <id> --name tests --value 1`,")
		fmt.Fprintln(stdout, "  then this reports whether the confidence Hydra stated held up.")
		fmt.Fprintln(stdout)
		renderSources(sources)
		return nil
	}
	if jsonOut {
		return json.NewEncoder(os.Stdout).Encode(struct {
			reliability.Report
			Unscored int                        `json:"unscored"`
			Sources  []reliability.SourceReport `json:"sources"`
		}{rep, unscored, sources})
	}
	renderReliability(rep, unscored)
	renderSources(sources)
	return nil
}

// reportSource reports one decision source, a row per domain it answered in.
func reportSource(source string, sources []reliability.SourceReport, jsonOut bool) error {
	if jsonOut {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"sources": sources})
	}
	if len(sources) == 0 {
		fmt.Fprintf(stdout, "\n  No decision answers recorded from %s.\n\n", source)
		return nil
	}
	renderSources(sources)
	return nil
}

// renderSources prints one row per source and domain. A row under the floor or
// with an unusable record says why, never a number it cannot back.
func renderSources(sources []reliability.SourceReport) {
	if len(sources) == 0 {
		return
	}
	fmt.Fprintf(stdout, "  %-28s %-10s %8s %8s %8s %8s %7s\n",
		"DECISION SOURCE", "DOMAIN", "VERDICTS", "AWAITING", "ECE", "BRIER", "GAP")
	fmt.Fprintln(stdout, "  "+strings.Repeat("─", 84))
	for _, s := range sources {
		lead := fmt.Sprintf("  %-28s %-10s %8d %8d", s.Source, s.Domain, s.Scored, s.Unscored)
		if s.Err != "" {
			fmt.Fprintf(stdout, "%s  %s\n", lead, warnStyle.Render(s.Err))
			continue
		}
		if s.Report == nil {
			note := fmt.Sprintf("not enough verdicts yet, %d more needed", s.Need)
			fmt.Fprintf(stdout, "%s  %s\n", lead, dimStyle.Render(note))
			continue
		}
		gap := s.Report.SignedGap()
		style := okStyle
		if math.Abs(gap) > calibrationSlack {
			style = warnStyle
		}
		fmt.Fprintf(stdout, "%s %8.4f %8.4f %s\n", lead, s.Report.ECE, s.Report.Brier,
			style.Render(fmt.Sprintf("%+7.1f", gap*100)))
	}
	legend := "GAP is stated minus observed, in points: positive is overconfident"
	fmt.Fprintf(stdout, "\n  %s\n\n", dimStyle.Render(legend))
}

func renderReliability(rep reliability.Report, unscored int) {
	fmt.Fprintf(stdout, "\n  %d scored run(s)", rep.N)
	if unscored > 0 {
		fmt.Fprintf(stdout, "  ·  %s", dimStyle.Render(fmt.Sprintf("%d awaiting a verdict", unscored)))
	}
	fmt.Fprintf(stdout, "  ·  %.1f%% correct overall\n\n", rep.BaseRate*100)

	fmt.Fprintf(stdout, "  %-14s %8s  %s\n", "STATED", "RUNS", "ACTUALLY CORRECT")
	fmt.Fprintln(stdout, "  "+strings.Repeat("─", 62))
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
		fmt.Fprintf(stdout, "  %5.0f-%-3.0f%% %8d  %s %s\n",
			b.Lo*100, b.Hi*100, b.N, style.Render(bar),
			style.Render(fmt.Sprintf("%5.1f%%  (%+.1f)", b.Observed*100, b.Gap()*100)))
	}
	fmt.Fprintf(stdout, "\n  %s\n", dimStyle.Render("┃ marks the stated confidence; the bar is what actually happened"))

	fmt.Fprintf(stdout, "\n  brier %.4f  =  reliability %.4f  -  resolution %.4f  +  uncertainty %.4f\n",
		rep.Brier, rep.Reliability, rep.Resolution, rep.Uncertainty)
	fmt.Fprintf(stdout, "  ECE %.4f   worst bin %.4f\n", rep.ECE, rep.MCE)

	verdict := okStyle.Render("confidences track outcomes")
	if gap := rep.SignedGap(); gap > calibrationSlack {
		verdict = warnStyle.Render(fmt.Sprintf(
			"overconfident by %.1f points on average: a stated confidence is worth less than it says", gap*100))
	} else if gap < -calibrationSlack {
		verdict = warnStyle.Render(fmt.Sprintf(
			"underconfident by %.1f points on average: runs are sampling more than they need to", -gap*100))
	}
	fmt.Fprintf(stdout, "  %s\n", verdict)
	if rep.Resolution < 0.01 {
		fmt.Fprintf(stdout, "  %s\n", warnStyle.Render(
			"resolution near zero: the confidences barely discriminate, so being well calibrated means little here"))
	}
	fmt.Fprintln(stdout)
}
