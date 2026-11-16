// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ankit373/hydra/internal/checkgen"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/runlog"
)

// genChecks is what --check gen (every default generator) and gen:<kind> (one, by spec kind) asked for.
type genChecks struct {
	all   bool
	named []string
	skip  string // a spec kind already run another way, e.g. claims under --source-only
}

func (g genChecks) asked() bool { return g.all || len(g.named) > 0 }

// parseGen reads a gen spec. ok is false for any other check.
func (g *genChecks) parseGen(spec string) (ok bool, err error) {
	if spec == "gen" {
		g.all = true
		return true, nil
	}
	kind, isGen := strings.CutPrefix(spec, "gen:")
	if !isGen {
		return false, nil
	}
	if _, found := checkgen.Named(kind); !found {
		return true, fmt.Errorf("no generated check %q, want one of %s", kind, strings.Join(checkgen.SpecKinds, ", "))
	}
	g.named = append(g.named, kind)
	return true, nil
}

// generators is gen's default set for the kind of work plus every named one, each once.
func (g genChecks) generators(kind string) []checkgen.Generator {
	var out []checkgen.Generator
	seen := map[string]bool{}
	add := func(gen checkgen.Generator) {
		if !seen[gen.Kind()] && gen.Kind() != g.skip {
			seen[gen.Kind()] = true
			out = append(out, gen)
		}
	}
	if g.all {
		for _, gen := range checkgen.For(kind) {
			add(gen)
		}
	}
	for _, k := range g.named {
		gen, _ := checkgen.Named(k)
		add(gen)
	}
	return out
}

var errGenBudget = errors.New("budget")

// genSpend stops check calls once the answer and the checks have spent maxUSD; 0 is no cap.
type genSpend struct {
	maxUSD, spent float64
	chat          func(ctx context.Context, prompt string, opts dispatch.Options) (*dispatch.Result, error)
}

func (s *genSpend) dispatch(ctx context.Context, prompt string, opts dispatch.Options) (*dispatch.Result, error) {
	if s.maxUSD > 0 && s.spent >= s.maxUSD {
		return nil, errGenBudget
	}
	res, err := s.chat(ctx, prompt, opts)
	if res != nil {
		s.spent += res.CostUSD
	}
	return res, err
}

// genRun is one dispatch's generated checks: what to run, and the dispatcher their calls go through.
type genRun struct {
	want      genChecks
	d         *dispatch.Dispatcher
	kind      string // empty when no kind of work was named
	runID     string
	maxUSD    float64
	localOnly bool
}

// genOutcome is what gen produced: a result per check, or err when none could be written.
type genOutcome struct {
	err     error
	results []checkgen.Result
	by      string // the head that wrote the checks
	same    bool   // that head is the answer's own family
	logErr  error  // verdicts that could not be logged
}

// run prints check's outcome, a line per check and the label, and adds each verdict to v.
// It never fails the dispatch.
func (g genRun) run(ctx context.Context, r *dispatch.Result, prompt string, v *verified) {
	if !g.want.asked() {
		return
	}
	o := g.check(ctx, r, prompt)
	v.addGen(o)
	if o.err != nil {
		printGenNotChecked("gen", o.err)
		return
	}
	if o.logErr != nil {
		fmt.Fprintf(stdout, "  %s\n", dimStyle.Render("verdict log: "+o.logErr.Error()))
	}
	for _, x := range o.results {
		printGenResult(x)
	}
	printFieldVerdicts(checkgen.MergeFields(o.results))
	note := ""
	if o.same {
		note = ", written by the answer's own family"
	}
	fmt.Fprintf(stdout, "  generated checks: %s  %s\n\n", checkgen.Label(o.results), dimStyle.Render("by "+o.by+note))
}

// check generates each check with a head PickGenerator chose and runs it on the answer.
// It prints nothing, so serve can use it too.
func (g genRun) check(ctx context.Context, r *dispatch.Result, prompt string) genOutcome {
	if g.kind == "" {
		return genOutcome{err: errors.New("no kind of work: name one with --kind")}
	}
	gens := g.want.generators(g.kind)
	if len(gens) == 0 {
		return genOutcome{err: fmt.Errorf("no generated check for %s", g.kind)}
	}
	// Only heads that can run: a pick nothing can drive would leave every check not checked.
	heads := runnableHeads(g.d)
	h, same, ok := checkgen.PickGenerator(heads, g.d.PricedScores(heads, g.kind), r.Head, nil, g.localOnly)
	if !ok {
		return genOutcome{err: errors.New("no head can write the checks")}
	}
	spend := &genSpend{maxUSD: g.maxUSD, spent: r.CostUSD, chat: g.d.Dispatch}
	byID := map[string]provider.Head{}
	for _, x := range heads {
		byID[x.ID] = x
	}
	// A check only ever asks the head that wrote it; an unknown one is never left to the router.
	ask := func(ctx context.Context, gen checkgen.GenInfo, prompt string) (string, error) {
		if _, ok := byID[gen.Head]; !ok {
			return "", fmt.Errorf("head %q cannot run here", gen.Head)
		}
		res, err := spend.dispatch(ctx, prompt, dispatch.Options{
			Head: gen.Head, Domain: g.kind, RunID: g.runID, NoCache: true, LocalOnly: g.localOnly,
		})
		if err != nil {
			return "", err
		}
		return res.Output, nil
	}
	env := checkgen.Env{
		Ask: ask,
		Decider: func(gen checkgen.GenInfo) oracle.Decider {
			h, ok := byID[gen.Head]
			if !ok {
				return nil
			}
			return &checkgen.HeadDecider{Head: h, Chat: spend.dispatch, RunID: g.runID, Domain: g.kind}
		},
	}
	writer := checkgen.GenInfo{Head: h.ID, Family: provider.Family(h), SameFamily: same}
	task := checkgen.Task{Text: prompt, Kind: g.kind, Writer: writer,
		Ask: func(ctx context.Context, p string) (string, error) { return ask(ctx, writer, p) }}
	span := runlog.Event{RunID: g.runID, SpanID: r.SpanID, Domain: g.kind}

	o := genOutcome{by: h.ID, same: same}
	var logErrs []error
	for _, gen := range gens {
		specs, err := gen.Generate(ctx, task)
		if err == nil && len(specs) == 0 {
			err = errors.New("nothing in this request to check")
		}
		if err != nil {
			o.results = append(o.results, checkgen.Result{Spec: checkgen.Spec{Kind: gen.Kind()}, Err: err})
			continue
		}
		res, err := checkgen.Run(ctx, specs, r.Output, r.Head, span, env)
		logErrs = append(logErrs, err)
		o.results = append(o.results, res...)
	}
	o.logErr = errors.Join(logErrs...)
	return o
}

func printGenResult(r checkgen.Result) {
	if !r.Checked() {
		printGenNotChecked(r.Spec.Check(), r.Err)
		return
	}
	status := cortexStyle.Render("PASS")
	if !r.Verdict.Passed {
		status = warnStyle.Render("FAIL")
	}
	detail, _, _ := strings.Cut(r.Verdict.Detail, "\n")
	if len(r.Fields) > 0 {
		detail = "" // the field lines below say it one field at a time
	}
	fmt.Fprintf(stdout, "  %s  %s  %s\n", status, r.Spec.Check(), dimStyle.Render(detail))
	printSentences(r.Sentences)
}

// printFieldVerdicts is a line per field that is not plain ok, every field once
// when both field checks ran, then how many are ok.
func printFieldVerdicts(fields []checkgen.FieldVerdict, merged bool) {
	if len(fields) == 0 {
		return
	}
	ok, width := 0, len(checkgen.FieldNotInSource)+1
	for _, f := range fields {
		width = max(width, len(f.Status))
	}
	for _, f := range fields {
		if f.Good() {
			ok++
		}
		if f.Status == checkgen.FieldOK && !merged {
			continue
		}
		fmt.Fprintf(stdout, "    %-*s %s  %s\n", width, f.Status, f.Name, dimStyle.Render(f.Detail))
	}
	fmt.Fprintf(stdout, "    %s\n", dimStyle.Render(fmt.Sprintf("%d of %d fields ok", ok, len(fields))))
}

// printGenNotChecked says why a generated check did not run.
func printGenNotChecked(check string, err error) {
	fmt.Fprintf(stdout, "  %s  %s  %s\n", warnStyle.Render("NOT CHECKED"), check, dimStyle.Render(genWhy(err)))
}

// genWhy names a spent budget as such.
func genWhy(err error) string {
	if errors.Is(err, errGenBudget) {
		return "budget"
	}
	return err.Error()
}
