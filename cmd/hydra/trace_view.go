// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/payload"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/waterfall"
)

// barWidth is how many cells the timeline occupies. Fixed rather than
// terminal-relative so two runs printed side by side stay comparable.
const barWidth = 28

func cmdTraceView() *cobra.Command {
	var spanID string
	var jsonOut bool
	var showText bool

	cmd := &cobra.Command{
		Use:   "view [run-id]",
		Short: "Show a run as a waterfall of spans, with what each one asked and answered",
		Long: `hyctl trace view renders a run as nested spans on a timeline: which head
was selected, how the fallback chain moved, what each span cost and how long
it took.

With --span it drills into one span and shows the prompt and response behind
it, resolved from the payload store. That text is only there if capture was
turned on at ` + "`hyctl init`" + `; the output says which of "capture is off",
"not stored" and "evicted" applies rather than rendering an empty box.

Run ids come from ` + "`hyctl trace view`" + ` with no argument, which uses the newest run.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			runID, err := resolveRunID(args)
			if err != nil {
				return err
			}
			events, err := runlog.Load(runID)
			if err != nil {
				return err
			}
			if len(events) == 0 {
				return fmt.Errorf("run %s has no events", runID)
			}
			tr := waterfall.Build(events)
			tr.RunID = runID

			if spanID != "" {
				return renderSpanDetail(tr, spanID, jsonOut)
			}
			if jsonOut {
				raw, err := json.MarshalIndent(tr, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(raw))
				return nil
			}
			renderWaterfall(tr, showText)
			return nil
		},
	}
	cmd.Flags().StringVar(&spanID, "span", "", "drill into one span (id or unique prefix)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	cmd.Flags().BoolVar(&showText, "text", false, "inline the prompt and response under every span")
	return cmd
}

// resolveRunID takes the argument or the newest run. Newest rather than
// prompting: the run someone wants to look at is almost always the last one.
func resolveRunID(args []string) (string, error) {
	if len(args) == 1 && args[0] != "" {
		return args[0], nil
	}
	runs, err := runlog.Runs()
	if err != nil {
		return "", err
	}
	if len(runs) == 0 {
		return "", errors.New("no runs recorded yet; dispatch something first")
	}
	return runs[0], nil // Runs is newest first
}

func renderWaterfall(tr *waterfall.Trace, showText bool) {
	fmt.Printf("\n  %s %s\n", dimStyle.Render("Run"), cortexStyle.Render(tr.RunID))
	if tr.Detail != "" {
		fmt.Printf("  %s\n", dimStyle.Render(oneLine(tr.Detail)))
	}

	total := tr.End.Sub(tr.Start)
	cost, inTok, outTok := tr.Totals()
	fmt.Printf("  %s\n\n", dimStyle.Render(fmt.Sprintf(
		"%s · %d tokens in, %d out · $%.6f", humanDuration(total), inTok, outTok, cost)))

	spans := tr.Flatten()
	if len(spans) == 0 {
		fmt.Printf("  %s\n\n", dimStyle.Render("no spans in this run"))
		return
	}
	for _, s := range spans {
		renderSpanRow(s, tr)
		if showText {
			renderSpanText(s, "        ")
		}
	}
	if tr.Unspanned > 0 {
		fmt.Printf("\n  %s\n", warnStyle.Render(fmt.Sprintf(
			"%d event(s) named no span and are not shown", tr.Unspanned)))
	}
	if tr.OrphanScores > 0 {
		fmt.Printf("\n  %s\n", warnStyle.Render(fmt.Sprintf(
			"%d score(s) judge a span this run does not contain", tr.OrphanScores)))
	}
	fmt.Printf("\n  %s\n\n", dimStyle.Render("hyctl trace view "+tr.RunID+" --span <id>  for one span's prompt and response"))
}

func renderSpanRow(s *waterfall.Span, tr *waterfall.Trace) {
	indent := strings.Repeat("  ", s.Depth)
	name := s.Model
	if name == "" {
		name = s.Head
	}
	if name == "" {
		name = s.Agent
	}
	if name == "" {
		name = string(s.Kind)
	}

	label := fmt.Sprintf("%s%s", indent, name)
	if len(label) > 30 {
		label = label[:29] + "…"
	}

	fmt.Printf("  %-30s %s %s%s %s\n",
		label,
		bar(s, tr),
		statusMark(s),
		verdictMark(s),
		dimStyle.Render(spanFacts(s)))
}

// bar draws the span's position and extent on the run's timeline, which is what
// makes a fallback chain (sequential) and a swarm (overlapping) tell apart at a
// glance.
func bar(s *waterfall.Span, tr *waterfall.Trace) string {
	span := tr.End.Sub(tr.Start).Seconds()
	if span <= 0 || s.Start.IsZero() {
		return dimStyle.Render(strings.Repeat("·", barWidth))
	}
	start := int(s.Start.Sub(tr.Start).Seconds() / span * barWidth)
	if start < 0 {
		start = 0
	}
	if start > barWidth-1 {
		start = barWidth - 1
	}
	// At least one cell: a span that took under a timeline cell still happened,
	// and drawing nothing would read as a span that never ran.
	length := int(s.Elapsed().Seconds()/span*barWidth + 0.5)
	if length < 1 {
		length = 1
	}
	if start+length > barWidth {
		length = barWidth - start
	}
	return dimStyle.Render(strings.Repeat("·", start)) +
		fillStyle(s).Render(strings.Repeat("█", length)) +
		dimStyle.Render(strings.Repeat("·", barWidth-start-length))
}

func fillStyle(s *waterfall.Span) interface{ Render(...string) string } {
	switch s.Level {
	case runlog.LevelError:
		return warnStyle
	case runlog.LevelWarn:
		return warnStyle
	}
	return okStyle
}

func statusMark(s *waterfall.Span) string {
	switch {
	case s.Level == runlog.LevelError:
		return warnStyle.Render("✗")
	case s.Level == runlog.LevelWarn:
		return warnStyle.Render("?")
	case s.Status == "ok":
		return okStyle.Render("✓")
	}
	return dimStyle.Render("·")
}

// verdictMark is whether the work was judged right, which is a separate column
// from whether the call succeeded. A span nobody judged shows a space, not a
// pass: "unverified" and "verified good" must never look the same.
func verdictMark(s *waterfall.Span) string {
	passed, known := s.Verdict()
	switch {
	case !known:
		return " "
	case passed:
		return okStyle.Render("✔")
	default:
		return warnStyle.Render("✘")
	}
}

// spanFacts is the one-line summary beside a bar: the numbers that distinguish
// this span from its siblings, and nothing that repeats the run header.
func spanFacts(s *waterfall.Span) string {
	var parts []string
	if s.Tier != 0 {
		parts = append(parts, fmt.Sprintf("t%d", s.Tier))
	}
	if d := s.Elapsed(); d > 0 {
		parts = append(parts, humanDuration(d))
	}
	if s.TTFTMs > 0 {
		parts = append(parts, fmt.Sprintf("ttft %dms", s.TTFTMs))
	}
	if s.InputTokens > 0 || s.OutputTokens > 0 {
		parts = append(parts, fmt.Sprintf("%d→%d tok", s.InputTokens, s.OutputTokens))
	}
	if s.CostUSD > 0 {
		parts = append(parts, fmt.Sprintf("$%.6f", s.CostUSD))
	}
	if s.Confidence > 0 {
		parts = append(parts, fmt.Sprintf("conf %.2f", s.Confidence))
	}
	if s.InputRef != "" {
		parts = append(parts, s.ID[:min(8, len(s.ID))])
	}
	// Scores go last and are named, because "was it right" is a different
	// question from the status the head reported and must not be confused
	// with it.
	for _, sc := range s.Scores {
		parts = append(parts, fmt.Sprintf("%s=%g", sc.Name, sc.Value))
	}
	if s.Detail != "" && len(parts) < 3 {
		parts = append(parts, oneLine(s.Detail))
	}
	return strings.Join(parts, " · ")
}

func renderSpanDetail(tr *waterfall.Trace, id string, jsonOut bool) error {
	s := tr.Find(id)
	if s == nil {
		return fmt.Errorf("no span in run %s matches %q; "+
			"run `hyctl trace view %s` to list them", tr.RunID, id, tr.RunID)
	}
	if jsonOut {
		in, inWhy := loadPayload(s.InputRef)
		out, outWhy := loadPayload(s.OutputRef)
		raw, err := json.MarshalIndent(map[string]any{
			"span":               s,
			"input":              in,
			"output":             out,
			"input_unavailable":  inWhy,
			"output_unavailable": outWhy,
		}, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(raw))
		return nil
	}

	fmt.Printf("\n  %s %s\n", dimStyle.Render("Span"), cortexStyle.Render(s.ID))
	fmt.Printf("  %s\n", dimStyle.Render(spanHeader(s)))
	if s.Detail != "" {
		fmt.Printf("  %s\n", dimStyle.Render(oneLine(s.Detail)))
	}
	if len(s.Meta) > 0 {
		keys := make([]string, 0, len(s.Meta))
		for k := range s.Meta {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var kv []string
		for _, k := range keys {
			kv = append(kv, fmt.Sprintf("%s=%v", k, s.Meta[k]))
		}
		fmt.Printf("  %s\n", dimStyle.Render(strings.Join(kv, " · ")))
	}
	if len(s.Scores) > 0 {
		fmt.Println()
		fmt.Printf("  %s\n", dimStyle.Render("── judged ──"))
		for _, sc := range s.Scores {
			mark := okStyle.Render("✔")
			if sc.Value <= 0 {
				mark = warnStyle.Render("✘")
			}
			line := fmt.Sprintf("%s %s = %g", mark, sc.Name, sc.Value)
			if sc.Source != "" {
				line += dimStyle.Render(" · " + sc.Source)
			}
			fmt.Printf("    %s\n", line)
			if sc.Comment != "" {
				fmt.Printf("      %s\n", dimStyle.Render(oneLine(sc.Comment)))
			}
		}
	}
	fmt.Println()
	renderSpanText(s, "  ")
	fmt.Println()
	return nil
}

func spanHeader(s *waterfall.Span) string {
	var parts []string
	if s.Model != "" {
		parts = append(parts, s.Model)
	} else if s.Head != "" {
		parts = append(parts, s.Head)
	}
	if s.Tier != 0 {
		parts = append(parts, fmt.Sprintf("tier %d", s.Tier))
	}
	if s.Status != "" {
		parts = append(parts, s.Status)
	}
	if d := s.Elapsed(); d > 0 {
		parts = append(parts, humanDuration(d))
	}
	if s.InputTokens > 0 || s.OutputTokens > 0 {
		parts = append(parts, fmt.Sprintf("%d→%d tokens", s.InputTokens, s.OutputTokens))
	}
	if s.CostUSD > 0 {
		parts = append(parts, fmt.Sprintf("$%.6f", s.CostUSD))
	}
	return strings.Join(parts, " · ")
}

// renderSpanText prints what the span asked and answered, or says precisely why
// it cannot. "Capture is off", "never stored" and "evicted" are three different
// facts and a reader has to be able to tell which one they are looking at.
func renderSpanText(s *waterfall.Span, indent string) {
	for _, side := range []struct {
		title string
		ref   string
	}{{"asked", s.InputRef}, {"answered", s.OutputRef}} {
		segs, why := loadSegments(side.ref)
		fmt.Printf("%s%s\n", indent, dimStyle.Render("── "+side.title+" ──"))
		if why != "" {
			fmt.Printf("%s  %s\n", indent, dimStyle.Render(why))
			continue
		}
		for _, seg := range segs {
			if seg.Label != "" && len(segs) > 1 {
				fmt.Printf("%s  %s\n", indent, dimStyle.Render("["+seg.Label+"]"))
			}
			for _, line := range strings.Split(strings.TrimRight(seg.Content, "\n"), "\n") {
				fmt.Printf("%s  %s\n", indent, line)
			}
		}
	}
}

// unavailableReason distinguishes the three ways text can be missing. Collapsing
// them into one empty box is what makes an observability tool untrustworthy:
// "nothing here" and "you turned this off" are not the same answer.
func unavailableReason(ref string) string {
	if ref == "" {
		cfg, err := config.Load()
		if err != nil || !cfg.CapturePayloads {
			return "payload capture is off; turn it on with capture_payloads = true"
		}
		return "not stored for this span"
	}
	return ""
}

func loadSegments(ref string) ([]payload.Segment, string) {
	if why := unavailableReason(ref); why != "" {
		return nil, why
	}
	store, err := payload.Open(payload.Dir())
	if err != nil {
		return nil, "payload store unreadable: " + err.Error()
	}
	segs, err := store.LoadSegments(ref)
	if errors.Is(err, payload.ErrNotFound) {
		return nil, "evicted: the store passed its budget and dropped this payload"
	}
	if err != nil {
		return nil, "unreadable: " + err.Error()
	}
	return segs, ""
}

func loadPayload(ref string) (string, string) {
	segs, why := loadSegments(ref)
	if why != "" {
		return "", why
	}
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.Content)
	}
	return b.String(), ""
}

// humanDuration formats a span's wall time at the precision a reader needs:
// milliseconds matter for a local head, minutes for a slow remote one.
func humanDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return "0ms"
	case d < time.Millisecond:
		// Sub-millisecond is not nothing, and "0ms" reads as a span that never
		// ran. Swarm spans logged after the fact land here.
		return fmt.Sprintf("%.1fms", float64(d.Nanoseconds())/1e6)
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
}

func cmdTraceScore() *cobra.Command {
	var spanID, name, comment, source string
	var value float64

	cmd := &cobra.Command{
		Use:   "score [run-id]",
		Short: "Record a verdict on a span: whether the work it did was right",
		Long: `hyctl trace score attaches a verdict to a span already in the log.

A trace says what a run cost and how long it took. This is what makes it say
whether the answer was any good, so a later ` + "`hyctl trace view`" + ` can show which
head's output actually held up.

Values are read as pass above zero and fail at or below it, and any failing
score fails the span. Nothing is overwritten: a verdict is appended, so the
span keeps whatever the head itself reported.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			runID, err := resolveRunID(args)
			if err != nil {
				return err
			}
			if spanID == "" {
				return fmt.Errorf("--span is required; run `hyctl trace view %s` to list them", runID)
			}
			err = runlog.AppendScore(runID, spanID, runlog.Score{
				Name: name, Value: value, Comment: comment, Source: source,
			})
			if errors.Is(err, runlog.ErrNoSuchSpan) {
				return fmt.Errorf("%w; run `hyctl trace view %s` to list them", err, runID)
			}
			if err != nil {
				return err
			}
			mark := okStyle.Render("✔")
			if value <= 0 {
				mark = warnStyle.Render("✘")
			}
			fmt.Printf("\n  %s %s = %g %s\n\n", mark, name, value,
				dimStyle.Render("on span "+spanID+" of "+runID))
			return nil
		},
	}
	cmd.Flags().StringVar(&spanID, "span", "", "span to judge (id or unique prefix)")
	cmd.Flags().StringVar(&name, "name", "", "what was checked, e.g. tests or lint")
	cmd.Flags().Float64Var(&value, "value", 0, "the verdict; above zero passes")
	cmd.Flags().StringVar(&comment, "comment", "", "why, in a sentence")
	cmd.Flags().StringVar(&source, "source", "human", "who judged: an oracle id, human, a CI job")
	return cmd
}
