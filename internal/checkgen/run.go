// SPDX-License-Identifier: MIT

package checkgen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

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
// Fields is every field's verdict, for a fields check only.
type Result struct {
	Spec    Spec
	Verdict oracle.Verdict
	Fields  []FieldVerdict
	Err     error
	// Sentences is a claims check's verdict per sentence; nil for every other kind.
	Sentences []SentenceVerdict
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
		r := verify(ctx, s, answer, span.Domain, env)
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

func verify(ctx context.Context, s Spec, answer, domain string, env Env) Result {
	r := Result{Spec: s}
	g, ok := Named(s.Kind)
	if !ok {
		r.Err = fmt.Errorf("checkgen: no generator for spec kind %q", s.Kind)
		return r
	}
	o, err := g.Oracle(s, env)
	if err != nil {
		r.Err = err
		return r
	}
	r.Verdict, r.Err = o.Verify(ctx, answer, trust.Task{Domain: domain})
	switch c := o.(type) {
	case *fieldsOracle:
		r.Fields = c.last
	case *roundtripOracle:
		r.Fields = c.last
	case *claimsOracle:
		r.Sentences = c.Last
	}
	return r
}

// MergeFields is one verdict per field when an answer's fields check and round trip both
// ran: a failure from either stands, else in the source, agreed by a second model, or both.
// With only one of the two it is that check's list; with neither, nil. Nothing is re-run.
func MergeFields(results []Result) (fields []FieldVerdict, merged bool) {
	var src, rt []FieldVerdict
	for _, r := range results {
		switch {
		case !r.Checked():
		case r.Spec.Kind == "fields" && src == nil:
			src = r.Fields
		case r.Spec.Kind == "roundtrip" && rt == nil:
			rt = r.Fields
		}
	}
	if src == nil || rt == nil {
		return append(src, rt...), false
	}
	byName := make(map[string]FieldVerdict, len(rt))
	for _, f := range rt {
		byName[f.Name] = f
	}
	for _, f := range src {
		other, ok := byName[f.Name]
		delete(byName, f.Name)
		agreed := ok && other.Status == FieldAgreed
		switch {
		case f.failed():
		case other.Status == FieldDiffers:
			f = other
		case f.InSource && agreed:
			f.Status = FieldInSourceAndAgreed
		case f.InSource:
			f.Status = FieldInSource
		case agreed:
			f.Status = FieldAgreed
		}
		fields = append(fields, f)
	}
	// A field only the round trip's list named, kept in its order.
	for _, f := range rt {
		if _, ok := byName[f.Name]; ok {
			fields = append(fields, f)
		}
	}
	return fields, true
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

// LoadSpecs reads every stored spec, grouped by the task hash it was written
// for and sorted by id, so a replay can rebuild the checks a past answer met.
func LoadSpecs() (map[string][]Spec, error) {
	paths, err := filepath.Glob(filepath.Join(SpecsDir(), "*.json"))
	if err != nil {
		return nil, err
	}
	out := map[string][]Spec{}
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var s Spec
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("checkgen: %s: %w", p, err)
		}
		out[s.TaskHash] = append(out[s.TaskHash], s)
	}
	for _, specs := range out {
		slices.SortFunc(specs, func(a, b Spec) int { return strings.Compare(a.ID, b.ID) })
	}
	return out, nil
}
