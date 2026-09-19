// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/runid"
	"github.com/ankit373/hydra/internal/vet"
)

var (
	blockingStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196"))
	nonBlockingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
)

// vetRouter adapts the real router to vet.Router. internal/vet does not depend
// on dispatch, so it stays testable without a model; this is where the two meet.
type vetRouter struct {
	d     *dispatch.Dispatcher
	runID string
	enum  string
	tier  string
	local bool
}

func (r vetRouter) Review(ctx context.Context, prompt, domain, resource string) (vet.Answer, error) {
	res, err := r.d.Dispatch(ctx, prompt, dispatch.Options{
		TierHint:  r.tier,
		Enum:      r.enum,
		LocalOnly: r.local,
		// The file decides the calibration domain, so the head measured best at
		// reviewing Go on this machine is the one asked about a .go file.
		Domain:   domain,
		Resource: resource,
		RunID:    r.runID,
		TaskID:   runid.New(),
	})
	if err != nil {
		return vet.Answer{}, err
	}
	a := vet.Answer{
		Output: res.Output,
		Head:   res.Head.ID,
		Model:  res.Head.Name,
		Tier:   rank.UITier(res.Head),
	}
	if res.Response != nil {
		a.InputTokens, a.OutputTokens = res.Response.InputTokens, res.Response.OutputTokens
		a.CostUSD = r.d.EstimateCost(a.Tier, a.InputTokens, a.OutputTokens)
	}
	return a, nil
}

func cmdVet() *cobra.Command {
	var (
		from, to, commit   string
		repo, exclude      string
		enum, tier         string
		jsonOut, localOnly bool
		dryRun             bool
		concurrency        int
	)

	cmd := &cobra.Command{
		Use:   "vet",
		Short: "Review a diff with the Heads on this machine",
		Long: "Reviews changed code and reports what is wrong with it.\n\n" +
			"open-code-review resolves which files are worth reviewing and by what rules,\n" +
			"with no model and no spend. Hydra routes each file to a Head, so cost, policy,\n" +
			"fallback and per-domain calibration all apply the way they do to any dispatch.\n\n" +
			"With no flags it reads the workspace: staged, unstaged and untracked together.\n" +
			"Exits 3 when a blocking finding is reported, so a script can gate on it.",
		Example: "  hyctl vet\n" +
			"  hyctl vet --from develop --to HEAD\n" +
			"  hyctl vet --commit 2504ec2\n" +
			"  hyctl vet --local --json",
		RunE: func(c *cobra.Command, _ []string) error {
			ctx := c.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			hint, logEnum, err := resolveVetRouting(enum, tier)
			if err != nil {
				return err
			}

			spec, err := vet.Resolve(ctx, vet.Options{
				Repo: repo, From: from, To: to, Commit: commit, Exclude: exclude,
			})
			if err != nil {
				if errors.Is(err, vet.ErrNoRuleSource) {
					printNoRuleSource(os.Stdout)
					os.Exit(2) // the reviewer is missing, which is setup, not a finding
				}
				return err
			}

			if dryRun || len(spec.Reviewable) == 0 {
				return printVetPlan(os.Stdout, spec, jsonOut)
			}

			d, err := dispatch.New(ctx)
			if err != nil {
				return fmt.Errorf("dispatcher init: %w", err)
			}
			defer d.Close()

			res, err := vet.Run(ctx, vetRouter{
				d: d, runID: runid.New(), enum: logEnum, tier: hint, local: localOnly,
			}, spec, vet.RunOptions{Concurrency: concurrency})
			if err != nil {
				return err
			}

			code, err := renderVet(os.Stdout, res, jsonOut)
			if err != nil {
				return err
			}
			if code != 0 {
				os.Exit(code)
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&from, "from", "", "source ref to diff from (e.g. develop)")
	f.StringVar(&to, "to", "", "target ref to diff to (e.g. HEAD)")
	f.StringVarP(&commit, "commit", "c", "", "review a single commit against its parent")
	f.StringVar(&repo, "repo", "", "repository root (default: current directory)")
	f.StringVar(&exclude, "exclude", "", "comma-separated gitignore-style patterns to skip")
	f.StringVar(&enum, "enum", "HARD", "routing enum key for each file's review")
	f.StringVar(&tier, "tier", "", "pin a tier, overriding --enum")
	f.BoolVar(&localOnly, "local", false, "local Heads only, no API calls")
	f.BoolVar(&jsonOut, "json", false, "emit JSON")
	f.BoolVar(&dryRun, "dry-run", false, "show what would be reviewed, without dispatching")
	f.IntVar(&concurrency, "concurrency", 4, "files reviewed at once")
	return cmd
}

// resolveVetRouting answers what this run routes on and what the cost rows
// should record. A pinned --tier records no enum: a row naming a routing key
// that did not route is the #832 defect. A garbage enum must not fall through
// to unrestricted auto-routing, the #501 rule.
func resolveVetRouting(enum, tier string) (hint, logEnum string, err error) {
	if tier != "" {
		return tier, "", nil
	}
	if !dispatch.IsKnownEnum(enum) {
		return "", "", fmt.Errorf("unknown enum %q", enum)
	}
	return dispatch.EnumToTier(enum), enum, nil
}

// renderVet writes the result and answers with the process exit code, so the
// exit code cannot depend on which rendering ran. --json returning before the
// check is what let a gate pass on a blocking finding.
func renderVet(w io.Writer, res *vet.Result, jsonOut bool) (int, error) {
	if jsonOut {
		if err := json.NewEncoder(w).Encode(res); err != nil {
			return 1, err
		}
	} else {
		printVet(w, res)
	}
	if res.BlockingCount() > 0 {
		return 3, nil // non-zero so callers can gate on it
	}
	return 0, nil
}

func printNoRuleSource(w io.Writer) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  hyctl vet needs open-code-review to decide which files are worth reviewing")
	fmt.Fprintln(w, "  and by what rules, and it is not on PATH.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "      npm install -g @alibaba-group/open-code-review")
	fmt.Fprintln(w)
	fmt.Fprintln(w, dimStyle.Render("  Only its delegate mode is used, which resolves files and rules with no"))
	fmt.Fprintln(w, dimStyle.Render("  model, no API key and no spend. The reviewing is Hydra's own Heads."))
	fmt.Fprintln(w)
}

// scopeLine names the diff under review, so a report is never ambiguous about
// what it read.
func scopeLine(s *vet.Spec) string {
	if s == nil {
		return "unknown scope"
	}
	switch s.Mode {
	case "commit":
		return "commit " + short(s.Commit)
	case "range":
		return fmt.Sprintf("range %s..%s", short(firstNonBlank(s.MergeBase, s.From)), short(firstNonBlank(s.To, "HEAD")))
	default:
		return "workspace"
	}
}

func printVetPlan(w io.Writer, s *vet.Spec, jsonOut bool) error {
	if jsonOut {
		return json.NewEncoder(w).Encode(s)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  vet %s %s %s\n", dimStyle.Render("·"), scopeLine(s), dimStyle.Render("· nothing dispatched"))
	fmt.Fprintln(w)
	if len(s.Reviewable) == 0 {
		fmt.Fprintln(w, "  no reviewable files in this diff")
		if n := len(s.Excluded); n > 0 {
			fmt.Fprintln(w, dimStyle.Render("  "+excludedSummary(s.Excluded)))
		}
		fmt.Fprintln(w)
		return nil
	}
	for _, f := range s.Reviewable {
		rule := dimStyle.Render("no rule")
		if g, ok := s.RuleFor(f.Path); ok {
			rule = dimStyle.Render(g.Pattern)
		}
		fmt.Fprintf(w, "  %-52.52s %9s  %s\n", f.Path, fmt.Sprintf("+%d/-%d", f.Insertions, f.Deletions), rule)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %d file(s) would be reviewed", len(s.Reviewable))
	if n := len(s.Excluded); n > 0 {
		fmt.Fprintf(w, "%s", dimStyle.Render("  ·  "+excludedSummary(s.Excluded)))
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w)
	return nil
}

func printVet(w io.Writer, r *vet.Result) {
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  vet %s %s %s %d file(s)\n", dimStyle.Render("·"), scopeLine(r.Spec), dimStyle.Render("·"), len(r.Files))
	fmt.Fprintln(w)

	byFile := map[string][]vet.Finding{}
	for _, f := range r.Findings {
		byFile[f.File] = append(byFile[f.File], f)
	}

	for _, out := range r.Files {
		head := out.Head
		if head == "" {
			head = dimStyle.Render("—")
		}
		tier := ""
		if out.Tier > 0 {
			tier = fmt.Sprintf("T%d", out.Tier)
		}
		fmt.Fprintf(w, "  %-46.46s %s %s\n", out.File, dimStyle.Render(truncLabel(head, 20)), dimStyle.Render(tier))

		switch {
		case out.Err != "":
			fmt.Fprintf(w, "        %s\n", dimStyle.Render("not reviewed: "+out.Err))
		case out.Unparsed:
			// Never rendered as clean: an unreadable reply is not a pass.
			fmt.Fprintf(w, "        %s\n", nonBlockingStyle.Render("the reply was not readable as findings; --json keeps it verbatim"))
		case len(byFile[out.File]) == 0:
			fmt.Fprintf(w, "        %s\n", dimStyle.Render("no defects reported"))
		}

		for _, f := range byFile[out.File] {
			line := dimStyle.Render("  —")
			if f.Line > 0 {
				line = fmt.Sprintf("%4d", f.Line)
			}
			fmt.Fprintf(w, "    %s  %s  %s\n", line, severityLabel(f.Severity), f.Title)
			for _, line := range wrap(f.Detail, 66) {
				fmt.Fprintf(w, "          %s\n", dimStyle.Render(line))
			}
		}
		if out.Truncated {
			fmt.Fprintf(w, "        %s\n", dimStyle.Render("diff was too large to send whole; the tail was not reviewed"))
		}
		if out.Discarded > 0 {
			fmt.Fprintf(w, "        %s\n", dimStyle.Render(fmt.Sprintf("%d reply/replies discarded: named another file, or carried no claim", out.Discarded)))
		}
		fmt.Fprintln(w)
	}

	blocking := r.BlockingCount()
	fmt.Fprintf(w, "  %d blocking %s %d non-blocking %s %d/%d file(s) reviewed %s $%.4f\n",
		blocking, dimStyle.Render("·"), len(r.Findings)-blocking, dimStyle.Render("·"),
		r.Reviewed(), len(r.Files), dimStyle.Render("·"), r.CostUSD)
	if r.Spec != nil && len(r.Spec.Excluded) > 0 {
		fmt.Fprintln(w, dimStyle.Render("  "+excludedSummary(r.Spec.Excluded)))
	}
	fmt.Fprintln(w)
}

func severityLabel(s string) string {
	if s == vet.Blocking {
		return blockingStyle.Render("blocking    ")
	}
	return nonBlockingStyle.Render("non-blocking")
}

// excludedSummary reports why files were skipped, grouped by reason, so a short
// review is visibly a filtered one rather than a small diff.
func excludedSummary(files []vet.File) string {
	counts := map[string]int{}
	for _, f := range files {
		r := f.Reason
		if r == "" {
			r = "unspecified"
		}
		counts[r]++
	}
	reasons := make([]string, 0, len(counts))
	for r := range counts {
		reasons = append(reasons, r)
	}
	sort.Strings(reasons)
	parts := make([]string, 0, len(reasons))
	for _, r := range reasons {
		parts = append(parts, fmt.Sprintf("%d %s", counts[r], r))
	}
	return fmt.Sprintf("%d skipped (%s)", len(files), strings.Join(parts, ", "))
}

func wrap(s string, width int) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var lines []string
	var cur string
	for _, w := range strings.Fields(s) {
		switch {
		case cur == "":
			cur = w
		case len(cur)+1+len(w) <= width:
			cur += " " + w
		default:
			lines = append(lines, cur)
			cur = w
		}
	}
	return append(lines, cur)
}

func short(rev string) string {
	if len(rev) > 8 && !strings.ContainsAny(rev, "/~^") {
		return rev[:8]
	}
	return rev
}

func firstNonBlank(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
