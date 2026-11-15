// SPDX-License-Identifier: MIT

package checkgen

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/internal/util"
)

const roundtripPrompt = "Extract these fields from the source below as one JSON object, " +
	"copying each value from the source. Leave out a field the source gives no value for.\nFields: "

// roundtripGen re-extracts the fields spec's fields from the source with the check's
// head, another family where one exists, and compares the two answers field by field.
// It costs one model call per answer, so it runs only when named.
type roundtripGen struct{}

func init() {
	if err := Register("extraction", roundtripGen{}); err != nil {
		panic(err)
	}
}

func (roundtripGen) Kind() string        { return "roundtrip" }
func (roundtripGen) OnlyWhenNamed() bool { return true }

// Generate takes the fields check's field list. With no fenced source there is nothing to re-extract from.
func (roundtripGen) Generate(ctx context.Context, task Task) ([]Spec, error) {
	specs, err := fieldsGen{}.Generate(ctx, task)
	if err != nil || len(specs) == 0 {
		return nil, err
	}
	var b fieldsBody
	if err := json.Unmarshal(specs[0].Body, &b); err != nil {
		return nil, err
	}
	if b.Source == "" {
		return nil, nil
	}
	s, err := NewSpec("roundtrip", "", b, task, task.Writer)
	if err != nil {
		return nil, err
	}
	return []Spec{s}, nil
}

func (roundtripGen) Oracle(s Spec, env Env) (oracle.Oracle, error) {
	var b fieldsBody
	if err := json.Unmarshal(s.Body, &b); err != nil {
		return nil, fmt.Errorf("checkgen roundtrip: spec %s: %w", s.ID, err)
	}
	if env.Ask == nil {
		return nil, errNoModel
	}
	var required []string
	for _, f := range b.Fields {
		if f.Required {
			required = append(required, f.Name)
		}
	}
	if len(required) == 0 {
		return nil, fmt.Errorf("checkgen roundtrip: spec %s has no required field to compare", s.ID)
	}
	return roundtripOracle{body: b, required: required, gen: s.Generator, ask: env.Ask}, nil
}

type roundtripOracle struct {
	body     fieldsBody
	required []string
	gen      GenInfo
	ask      func(ctx context.Context, gen GenInfo, prompt string) (string, error)
}

// Verify passes when the answer and the second extraction agree on every required field.
// A second extraction that is not a JSON object leaves the answer not checked.
func (o roundtripOracle) Verify(ctx context.Context, candidate string, _ trust.Task) (oracle.Verdict, error) {
	ours, ok := DecodeObject(candidate)
	if !ok {
		return oracle.Verdict{Detail: "the answer is not a JSON object"}, nil
	}
	names := make([]string, len(o.body.Fields))
	for i, f := range o.body.Fields {
		names[i] = f.Name + " (" + f.Type + ")"
	}
	reply, err := o.ask(ctx, o.gen, roundtripPrompt+strings.Join(names, ", ")+"\n\n"+util.WrapUntrusted("SOURCE", o.body.Source))
	if err != nil {
		return oracle.Verdict{}, err
	}
	theirs, ok := DecodeObject(reply)
	if !ok {
		return oracle.Verdict{}, fmt.Errorf("checkgen roundtrip: %s did not reply with a JSON object: %.80q", o.gen.Head, reply)
	}
	var differ []string
	for _, name := range o.required {
		a, b := ours[name], theirs[name]
		if a == nil || b == nil || !SameValue(a, b) {
			differ = append(differ, fmt.Sprintf("%s: answer %v, re-extracted %v", name, show(a), show(b)))
		}
	}
	if len(differ) > 0 {
		return oracle.Verdict{Detail: "fields differ: " + strings.Join(differ, "; ")}, nil
	}
	return oracle.Verdict{Passed: true, Detail: fmt.Sprintf("all %d required fields agree", len(o.required))}, nil
}

func show(v any) string {
	if v == nil {
		return "nothing"
	}
	return fmt.Sprintf("%q", valueText(v))
}
