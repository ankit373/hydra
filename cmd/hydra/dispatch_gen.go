// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ankit373/hydra/internal/checkgen"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/runlog"
)

// genChecks is what --check gen (every default generator) and gen:<kind> (one, by spec kind) asked for.
type genChecks struct {
	all   bool
	named []string
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
		if !seen[gen.Kind()] {
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

// run generates each check with a head PickGenerator chose, runs it on the answer and
// prints a line per check and the label. It never fails the dispatch.
func (g genRun) run(ctx context.Context, r *dispatch.Result, prompt string) {
	if !g.want.asked() {
		return
	}
	if g.kind == "" {
		printGenNotChecked("gen", errors.New("no kind of work: name one with --kind"))
		return
	}
	gens := g.want.generators(g.kind)
	if len(gens) == 0 {
		printGenNotChecked("gen", fmt.Errorf("no generated check for %s", g.kind))
		return
	}
	// Only heads that can run: a pick nothing can drive would leave every check not checked.
	heads := slices.DeleteFunc(slices.Clone(g.d.Heads()), func(h provider.Head) bool {
		return executor.Unroutable(h) != "" || !g.d.Allows(h.ID)
	})
	h, same, ok := checkgen.PickGenerator(heads, g.d.PricedScores(heads, g.kind), r.Head, nil, g.localOnly)
	if !ok {
		printGenNotChecked("gen", errors.New("no head can write the checks"))
		return
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

	var all []checkgen.Result
	for _, gen := range gens {
		specs, err := gen.Generate(ctx, task)
		if err == nil && len(specs) == 0 {
			err = errors.New("nothing in this request to check")
		}
		if err != nil {
			printGenNotChecked("gen:"+gen.Kind(), err)
			all = append(all, checkgen.Result{Spec: checkgen.Spec{Kind: gen.Kind()}, Err: err})
			continue
		}
		res, err := checkgen.Run(ctx, specs, r.Output, r.Head, span, env)
		if err != nil {
			fmt.Fprintf(stdout, "  %s\n", dimStyle.Render("verdict log: "+err.Error()))
		}
		for _, x := range res {
			printGenResult(x)
		}
		all = append(all, res...)
	}
	note := ""
	if same {
		note = ", written by the answer's own family"
	}
	fmt.Fprintf(stdout, "  generated checks: %s  %s\n\n", checkgen.Label(all), dimStyle.Render("by "+h.ID+note))
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
	printFieldVerdicts(r.Fields)
}

// printFieldVerdicts is a line per field that is not ok, then how many are.
func printFieldVerdicts(fields []checkgen.FieldVerdict) {
	if len(fields) == 0 {
		return
	}
	ok := 0
	for _, f := range fields {
		if f.Status == checkgen.FieldOK {
			ok++
			continue
		}
		fmt.Fprintf(stdout, "    %-14s %s  %s\n", f.Status, f.Name, dimStyle.Render(f.Detail))
	}
	fmt.Fprintf(stdout, "    %s\n", dimStyle.Render(fmt.Sprintf("%d of %d fields ok", ok, len(fields))))
}

// printGenNotChecked says why a generated check did not run; a spent budget is named as such.
func printGenNotChecked(check string, err error) {
	why := err.Error()
	if errors.Is(err, errGenBudget) {
		why = "budget"
	}
	fmt.Fprintf(stdout, "  %s  %s  %s\n", warnStyle.Render("NOT CHECKED"), check, dimStyle.Render(why))
}
