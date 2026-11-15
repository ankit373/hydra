// SPDX-License-Identifier: MIT

package checkgen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/trust"
)

// The three labels Label gives. No number: only certified mode states a bound.
const (
	LabelAllPassed  = "all passed"
	LabelSomeFailed = "some failed"
	LabelNotChecked = "not checked"
)

// Result is one spec's outcome on one answer. Err set means the check never ran.
type Result struct {
	Spec    Spec
	Verdict oracle.Verdict
	Err     error
}

// Checked says whether the check ran, so a broken oracle is never read as a pass.
func (r Result) Checked() bool { return r.Err == nil }

// SpecsDir holds every spec ever run, beside the eval set and never pruned.
func SpecsDir() string {
	return filepath.Join(filepath.Dir(evalset.DefaultPath()), "checks")
}

// Run judges answer with each spec, logs a verdict per check that ran and
// scores span when it names one. The error joins log writes that failed.
func Run(ctx context.Context, specs []Spec, answer string, answering provider.Head, span runlog.Event, env Env) ([]Result, error) {
	out := make([]Result, 0, len(specs))
	var errs []error
	for _, s := range specs {
		if err := storeSpec(s); err != nil {
			errs = append(errs, err)
		}
		r := Result{Spec: s}
		r.Verdict, r.Err = verify(ctx, s, answer, span.Domain, env)
		out = append(out, r)
		if !r.Checked() {
			continue
		}
		if err := record(r, answer, answering, span); err != nil {
			errs = append(errs, err)
		}
	}
	return out, errors.Join(errs...)
}

func verify(ctx context.Context, s Spec, answer, domain string, env Env) (oracle.Verdict, error) {
	g, ok := generatorOf(s.Kind)
	if !ok {
		return oracle.Verdict{}, fmt.Errorf("checkgen: no generator for spec kind %q", s.Kind)
	}
	o, err := g.Oracle(s, env)
	if err != nil {
		return oracle.Verdict{}, err
	}
	return o.Verify(ctx, answer, trust.Task{Domain: domain})
}

func record(r Result, answer string, answering provider.Head, span runlog.Event) error {
	check := r.Spec.Check()
	detail := "spec " + r.Spec.ID
	if r.Spec.Generator.SameFamily {
		detail += ", written by the answer's own family"
	}
	if r.Verdict.Detail != "" {
		detail += ": " + r.Verdict.Detail
	}
	err := evalset.AddVerdict(evalset.VerdictsPath(), evalset.Verdict{
		SpanID: span.SpanID, RunID: span.RunID, TaskHash: r.Spec.TaskHash,
		CandidateHash: evalset.Hash(answer), Head: answering.ID, Domain: span.Domain,
		Check: check, Passed: r.Verdict.Passed, Detail: detail, GenFamily: r.Spec.Generator.Family,
	})
	if span.SpanID == "" {
		return err
	}
	val := 0.0
	if r.Verdict.Passed {
		val = 1
	}
	return errors.Join(err, runlog.AppendScore(span.RunID, span.SpanID, runlog.Score{
		Name: check, Value: val, Comment: detail, Source: check,
	}))
}

// storeSpec writes s once under its ID; a second write of the same spec is a no-op.
func storeSpec(s Spec) error {
	if s.ID == "" {
		return errors.New("checkgen: spec has no id; build it with NewSpec")
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	dir := SpecsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Written aside then linked, so a torn write never takes the spec's name.
	tmp, err := os.CreateTemp(dir, ".spec-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	err = os.Link(tmp.Name(), filepath.Join(dir, s.ID+".json"))
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	return err
}

// Label sums results up. A check that could not run means not every check
// passed, so all passed needs every one to have run.
func Label(results []Result) string {
	ran := 0
	for _, r := range results {
		if !r.Checked() {
			continue
		}
		if !r.Verdict.Passed {
			return LabelSomeFailed
		}
		ran++
	}
	if ran == 0 || ran < len(results) {
		return LabelNotChecked
	}
	return LabelAllPassed
}
