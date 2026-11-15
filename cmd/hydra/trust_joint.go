// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/stats"
	"github.com/ankit373/hydra/internal/trust"
)

// minSharedWrong is the fewest wrong answers a rate is printed on. Below it
// the 95% upper bound is wider than any rate it could certify.
const minSharedWrong = 20

// jointAlpha is the one-sided level of every upper bound in the report.
const jointAlpha = 0.05

// falsePass is how often one check, or two together, passed a wrong answer.
// Rate and Upper are nil below minSharedWrong.
type falsePass struct {
	Checks []string `json:"checks"`
	Wrong  int      `json:"wrong"`
	Passed int      `json:"passed"`
	Rate   *float64 `json:"rate,omitempty"`
	Upper  *float64 `json:"upper_95,omitempty"`
	// Independent is the product of the two single rates, what the pair's
	// rate would be if the checks failed independently. Pairs only.
	Independent *float64 `json:"independent,omitempty"`
}

type jointDomain struct {
	Domain string      `json:"domain"`
	Wrong  int         `json:"wrong_answers"`
	Checks []falsePass `json:"checks"`
	Pairs  []falsePass `json:"pairs"`
}

// rows is every single check, then every pair, in report order.
func (d jointDomain) rows() []falsePass {
	return append(append(make([]falsePass, 0, len(d.Checks)+len(d.Pairs)), d.Checks...), d.Pairs...)
}

type jointReport struct {
	Domains     []jointDomain `json:"domains"`
	SkippedRuns int           `json:"skipped_runs_without_span"`
}

func newFalsePass(checks []string, wrong, passed int) falsePass {
	fp := falsePass{Checks: checks, Wrong: wrong, Passed: passed}
	if wrong < minSharedWrong {
		return fp
	}
	rate := float64(passed) / float64(wrong)
	// Cannot fail: wrong > 0 and passed <= wrong by construction.
	upper, _ := stats.ClopperPearsonUpper(passed, wrong, jointAlpha)
	fp.Rate, fp.Upper = &rate, &upper
	return fp
}

// buildJoint counts, per domain, how often each check and each pair of checks
// passed an answer that ground truth says was wrong. Cost is one pass over the
// answers plus c² per answer for its c checks.
func buildJoint(answers []evalset.Answer, domain string) []jointDomain {
	type tally struct{ wrong, passed int }
	type acc struct {
		wrong  int
		single map[string]*tally
		pair   map[[2]string]*tally
	}
	byDomain := map[string]*acc{}

	for _, a := range answers {
		if a.Truth == nil || a.Truth.Outcome != evalset.OutcomeIncorrect {
			continue
		}
		d := a.Domain
		if d == "" {
			d = "unknown"
		}
		if domain != "" && d != domain {
			continue
		}
		// One verdict per check on this answer: a check that ran twice passed
		// only if it passed both times, the same rule as a span's verdict.
		passed := map[string]bool{}
		for _, c := range a.Checks {
			prev, seen := passed[c.Check]
			passed[c.Check] = c.Passed && (!seen || prev)
		}
		names := make([]string, 0, len(passed))
		for n := range passed {
			names = append(names, n)
		}
		sort.Strings(names)

		ac := byDomain[d]
		if ac == nil {
			ac = &acc{single: map[string]*tally{}, pair: map[[2]string]*tally{}}
			byDomain[d] = ac
		}
		ac.wrong++
		for i, x := range names {
			t := ac.single[x]
			if t == nil {
				t = &tally{}
				ac.single[x] = t
			}
			t.wrong++
			if passed[x] {
				t.passed++
			}
			for _, y := range names[i+1:] {
				k := [2]string{x, y}
				p := ac.pair[k]
				if p == nil {
					p = &tally{}
					ac.pair[k] = p
				}
				p.wrong++
				if passed[x] && passed[y] {
					p.passed++
				}
			}
		}
	}

	out := make([]jointDomain, 0, len(byDomain))
	for d, ac := range byDomain {
		jd := jointDomain{Domain: d, Wrong: ac.wrong}
		rates := map[string]*float64{}
		for name, t := range ac.single {
			fp := newFalsePass([]string{name}, t.wrong, t.passed)
			rates[name] = fp.Rate
			jd.Checks = append(jd.Checks, fp)
		}
		for k, t := range ac.pair {
			fp := newFalsePass([]string{k[0], k[1]}, t.wrong, t.passed)
			if ra, rb := rates[k[0]], rates[k[1]]; fp.Rate != nil && ra != nil && rb != nil {
				ind := *ra * *rb
				fp.Independent = &ind
			}
			jd.Pairs = append(jd.Pairs, fp)
		}
		sortFalsePasses(jd.Checks)
		sortFalsePasses(jd.Pairs)
		out = append(out, jd)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return out
}

func sortFalsePasses(fps []falsePass) {
	sort.Slice(fps, func(i, j int) bool {
		return strings.Join(fps[i].Checks, "\x00") < strings.Join(fps[j].Checks, "\x00")
	})
}

func cmdTrustJoint() *cobra.Command {
	var domain string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "joint",
		Short: "How often two checks pass the same wrong answer",
		Long: `hyctl trust joint reads the verdict log and reports, per kind of work, how often
each check passed an answer that later turned out wrong, and how often two checks
both passed it.

Only answers with ground truth count: a human's review, ` + "`hyctl trust outcome`" + `,
or any other source that later said whether the answer was right. Models that
voted in an ensemble run count as checks too.

"Passed the same wrong answer" means both checks said yes to an answer that was
wrong. If two checks fail on different answers, trusting both cuts the risk a lot.
If they fail on the same ones, the second check adds little. The INDEPENDENT
column is what the pair's rate would be if their mistakes were unrelated; a pair
rate well above it means the two should not be counted as separate evidence.

Each rate comes with a one-sided 95% upper bound. Below ` + fmt.Sprint(minSharedWrong) + ` shared wrong answers
no rate is printed, because the bound is then wider than the rate itself.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			answers, err := evalset.Joint(evalset.VerdictsPath())
			if err != nil {
				return err
			}
			runs, err := trust.LoadRuns(trust.DefaultLogPath())
			if err != nil {
				return err
			}
			answers, skipped := evalset.AddModelVotes(answers, runs)
			rep := jointReport{Domains: buildJoint(answers, domain), SkippedRuns: skipped}
			if jsonOut {
				return printJSON(rep)
			}
			renderJoint(rep)
			return nil
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "report one kind of work only")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	return cmd
}

func renderJoint(rep jointReport) {
	if len(rep.Domains) == 0 {
		fmt.Fprintln(stdout, "\n  No wrong answers with ground truth yet.")
		fmt.Fprintln(stdout, "  Record truth with `hyctl review reject`, `hyctl trust outcome` or `hyctl trace score`.")
		fmt.Fprintln(stdout)
		return
	}
	for _, d := range rep.Domains {
		fmt.Fprintf(stdout, "\n  %s  ·  %d wrong answer(s)\n", d.Domain, d.Wrong)
		fmt.Fprintf(stdout, "  %-44s %6s %7s %7s %8s %12s\n", "CHECKS", "WRONG", "PASSED", "RATE", "UPPER95", "INDEPENDENT")
		fmt.Fprintln(stdout, "  "+strings.Repeat("─", 90))
		for _, fp := range d.rows() {
			renderFalsePass(fp)
		}
	}
	if belowFloor(rep) {
		fmt.Fprintf(stdout, "\n  %s\n", dimStyle.Render(fmt.Sprintf("Under %d wrong answers the 95%% bound is wider "+
			"than the rate, so none is shown. Certifying 5%% takes about 60 per domain.", minSharedWrong)))
	}
	if rep.SkippedRuns > 0 {
		fmt.Fprintf(stdout, "\n  %s\n", dimStyle.Render(fmt.Sprintf(
			"%d ensemble run(s) had no span id and could not be joined", rep.SkippedRuns)))
	}
	fmt.Fprintln(stdout)
}

func renderFalsePass(fp falsePass) {
	lead := fmt.Sprintf("  %-44.44s %6d %7d", strings.Join(fp.Checks, " + "), fp.Wrong, fp.Passed)
	if fp.Rate == nil {
		fmt.Fprintf(stdout, "%s  %s\n", lead, dimStyle.Render(fmt.Sprintf(
			"too few wrong answers (%d of %d needed)", fp.Wrong, minSharedWrong)))
		return
	}
	ind := ""
	if fp.Independent != nil {
		ind = fmt.Sprintf("%.3f", *fp.Independent)
	}
	fmt.Fprintf(stdout, "%s %7.3f %8.3f %12s\n", lead, *fp.Rate, *fp.Upper, ind)
}

func belowFloor(rep jointReport) bool {
	for _, d := range rep.Domains {
		for _, fp := range d.rows() {
			if fp.Rate == nil {
				return true
			}
		}
	}
	return false
}
