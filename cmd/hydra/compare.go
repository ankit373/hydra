// SPDX-License-Identifier: MIT

package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ankit373/hydra/internal/stats"
	"github.com/ankit373/hydra/internal/trust"
)

// maxCompareN caps the search for how many answers would separate two heads.
const maxCompareN = 1 << 20

// compareSide is one head's checked answers in one kind of work. Passed and
// Checked are the whole record; Since* the record after its last change.
type compareSide struct {
	Head         string    `json:"head"`
	Checked      int       `json:"checked"`
	Passed       int       `json:"passed"`
	PassRate     float64   `json:"pass_rate"`
	Lower        float64   `json:"lower"`
	Changed      time.Time `json:"changed,omitzero"`
	SinceChecked int       `json:"since_checked"`
	SincePassed  int       `json:"since_passed"`
	SinceLower   float64   `json:"since_lower"`
	SinceUpper   float64   `json:"since_upper"`
	// CostPerChecked stays nil until cost rows are joined to judged answers.
	CostPerChecked *float64 `json:"cost_per_checked_usd"`
}

// comparison is the verdict: Ahead names a head only when the intervals do not
// overlap, and MoreEach is a rough count of answers per head that would tell.
type comparison struct {
	Kind     string        `json:"kind"`
	Heads    []compareSide `json:"heads"`
	Ahead    string        `json:"ahead,omitempty"`
	MoreEach int           `json:"more_each,omitempty"`
	Verdict  string        `json:"verdict"`
	// Basis says where the numbers came from when it is not the record; Replay
	// is set for a replayed sample.
	Basis  string        `json:"basis,omitempty"`
	Replay *replayResult `json:"replay,omitempty"`
}

func cmdCompare() *cobra.Command {
	var kind string
	var jsonOut, yes bool
	var replay int
	var maxCost float64
	cmd := &cobra.Command{
		Use:   "compare <head-a> <head-b> --kind <kind>[.<area>]",
		Short: "Two heads side by side on one kind of work, from the record",
		Long: "Reads each head's checked answers in one kind of work from the calibration\n" +
			"record. Calls no model. A head is called ahead only when its 95% lower bound\n" +
			"is above the other's upper bound, judged on the record since any change.\n\n" +
			"The record holds only what each head happened to be given. --replay N sends up to N of the\n" +
			"newest stored tasks of the kind to both heads and runs each task's check again: ground, and\n" +
			"generated checks whose spec is stored. Other tasks are skipped and counted. It prints the\n" +
			"worst-case cost and asks first unless --yes, and stops before a task that could pass\n" +
			"--max-cost. The answers are filed, so the replay adds to the record.",
		Args: cobra.ExactArgs(2),
		RunE: func(cc *cobra.Command, args []string) error {
			k, err := trust.ParseKind(kind)
			if err != nil {
				return err
			}
			if args[0] == args[1] {
				return errors.New("compare needs two different heads")
			}
			if replay < 0 {
				return errors.New("--replay needs a number of tasks, 1 or more")
			}
			if replay > 0 {
				return compareReplay(cc.Context(), replayArgs{kind: k, heads: [2]string{args[0], args[1]}, n: replay,
					yes: yes, maxCost: maxCost, maxCostSet: cc.Flags().Changed("max-cost"), jsonOut: jsonOut})
			}
			cal, err := trust.New(trust.DefaultPath())
			if err != nil {
				return err
			}
			c, err := compareHeads(cal, k, args[0], args[1])
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(c)
			}
			printComparison(c)
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "kind of work, <kind>[.<area>]: "+strings.Join(trust.Kinds, ", "))
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the comparison as JSON")
	cmd.Flags().IntVar(&replay, "replay", 0, "send N stored tasks of this kind to both heads and run their checks again, instead of reading the record")
	cmd.Flags().BoolVar(&yes, "yes", false, "with --replay, do not ask before spending")
	cmd.Flags().Float64Var(&maxCost, "max-cost", 0, "with --replay, stop before a task that could take the spend past this many USD (default policy.yaml max_cost_usd)")
	_ = cmd.MarkFlagRequired("kind")
	return cmd
}

// compareHeads reads both heads' cells for kind. A head with no record in any
// kind is an error, since it is more likely a typo than an unused head.
func compareHeads(cal *trust.Calibrator, kind, a, b string) (comparison, error) {
	report := cal.Report()
	known := map[string]bool{}
	for _, s := range report {
		known[s.Source] = true
	}
	c := comparison{Kind: kind}
	for _, head := range []string{a, b} {
		if !known[head] {
			return c, fmt.Errorf("no checked answers recorded for head %q; heads with a record: %s",
				head, strings.Join(slices.Sorted(maps.Keys(known)), ", "))
		}
		side := compareSide{Head: head}
		side.Passed, side.Checked = cal.CommitmentsIn(head, kind)
		if i := slices.IndexFunc(report, func(s trust.Stat) bool {
			return s.Source == head && s.Domain == trust.Domain(kind)
		}); i >= 0 {
			side.Changed = report[i].Changed
			side.SincePassed, side.SinceChecked = report[i].SinceCorrect, report[i].SinceTotal
		}
		if side.Checked > 0 {
			side.PassRate = float64(side.Passed) / float64(side.Checked)
		}
		// Counts read off one cell always satisfy 0 <= k <= n, and n = 0 is the whole range.
		side.Lower, _, _ = stats.Wilson(side.Passed, side.Checked, stats.Z95)
		side.SinceLower, side.SinceUpper, _ = stats.Wilson(side.SincePassed, side.SinceChecked, stats.Z95)
		c.Heads = append(c.Heads, side)
	}
	c.decide()
	return c, nil
}

// decide sets the verdict from the bounds since any change: ahead only when one
// head's lower bound is above the other's upper bound.
func (c *comparison) decide() {
	x, y := c.Heads[0], c.Heads[1]
	switch {
	case x.SinceChecked == 0 || y.SinceChecked == 0:
		c.Verdict = "no_record"
	case x.SinceLower > y.SinceUpper:
		c.Verdict, c.Ahead = "ahead", x.Head
	case y.SinceLower > x.SinceUpper:
		c.Verdict, c.Ahead = "ahead", y.Head
	default:
		c.Verdict = "no_clear_difference"
		c.MoreEach = moreToSeparate(x, y)
	}
}

// moreToSeparate is how many more answers each head would need for the
// intervals to part, if both kept their rates. 0 when the rates are equal.
func moreToSeparate(x, y compareSide) int {
	px := float64(x.SincePassed) / float64(x.SinceChecked)
	py := float64(y.SincePassed) / float64(y.SinceChecked)
	parted := func(n int) bool {
		xl, xh, _ := stats.Wilson(int(px*float64(n)+0.5), n, stats.Z95)
		yl, yh, _ := stats.Wilson(int(py*float64(n)+0.5), n, stats.Z95)
		return xl > yh || yl > xh
	}
	have := min(x.SinceChecked, y.SinceChecked)
	hi := max(have, 1)
	for !parted(hi) {
		if hi >= maxCompareN {
			return 0
		}
		hi *= 2
	}
	lo := hi / 2 // parted(hi) holds, and parted(lo) does not unless hi == have
	for lo+1 < hi {
		mid := lo + (hi-lo)/2
		if parted(mid) {
			hi = mid
		} else {
			lo = mid
		}
	}
	return max(hi-have, 1)
}

func printComparison(c comparison) {
	basis := cmp.Or(c.Basis, "checked answers from the record, 95% bounds")
	fmt.Fprintf(stdout, "\n  %s  %s\n\n", cortexStyle.Render(c.Kind), dimStyle.Render(basis))
	fmt.Fprintf(stdout, "  %-28s %8s %6s %7s   %-26s %s\n",
		"Head", "checked", "pass", "lower", "since change", "cost/checked")
	fmt.Fprintln(stdout, "  "+strings.Repeat("─", 96))
	for _, s := range c.Heads {
		since := "none"
		if !s.Changed.IsZero() {
			since = fmt.Sprintf("%d of %d since %s", s.SincePassed, s.SinceChecked,
				s.Changed.Local().Format(time.DateOnly))
		}
		fmt.Fprintf(stdout, "  %-28.28s %8d %6.2f %7.2f   %-26s %s\n",
			s.Head, s.Checked, s.PassRate, s.Lower, since, "unknown")
	}
	x, y := c.Heads[0], c.Heads[1]
	fmt.Fprintln(stdout)
	switch c.Verdict {
	case "no_record":
		fmt.Fprintf(stdout, "  %s\n", warnStyle.Render(
			"No verdict: a head has no checked answers in this kind of work yet."))
	case "ahead":
		lead, other := x, y
		if c.Ahead == y.Head {
			lead, other = y, x
		}
		fmt.Fprintf(stdout, "  %s is ahead: its lower bound %.2f is above %s's upper bound %.2f.\n",
			lead.Head, lead.SinceLower, other.Head, other.SinceUpper)
	case "no_clear_difference":
		more := "the two pass at the same rate so far, so more answers may never tell"
		if c.MoreEach > 0 {
			more = fmt.Sprintf("about %d more checked answers each would tell, if the rates hold", c.MoreEach)
		}
		fmt.Fprintf(stdout, "  No clear difference yet: %s.\n", more)
	}
	if c.Replay != nil {
		fmt.Fprintf(stdout, "  %s\n\n", dimStyle.Render(replayFooter(*c.Replay)))
		return
	}
	fmt.Fprintf(stdout, "  %s\n\n", dimStyle.Render(
		"Judged on the record since any change. Cost per checked answer is not joined to the record yet."))
}
