// SPDX-License-Identifier: MIT

package checkgen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/internal/util"
)

const roundtripPrompt = "Extract these fields from the source below as one JSON object, " +
	"copying each value from the source. Leave out a field the source gives no value for.\nFields: "

// ErrRoundtripReply is the second extraction not being a JSON object: the answer was not confirmed.
var ErrRoundtripReply = errors.New("checkgen roundtrip: the second model did not reply with a JSON object")

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
	required := 0
	for _, f := range b.Fields {
		if f.Required {
			required++
		}
	}
	if required == 0 {
		return nil, fmt.Errorf("checkgen roundtrip: spec %s has no required field to compare", s.ID)
	}
	return &roundtripOracle{body: b, required: required, gen: s.Generator, ask: env.Ask}, nil
}

// roundtripOracle keeps every field's verdict from the last Verify, like fieldsOracle.
type roundtripOracle struct {
	body     fieldsBody
	required int
	gen      GenInfo
	ask      func(ctx context.Context, gen GenInfo, prompt string) (string, error)
	last     []FieldVerdict
}

// Verify passes when the answer and the second extraction agree on every required field.
// A second extraction that is not a JSON object leaves the answer not checked.
func (o *roundtripOracle) Verify(ctx context.Context, candidate string, _ trust.Task) (oracle.Verdict, error) {
	o.last = nil
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
		return oracle.Verdict{}, fmt.Errorf("%w: %s said %.80q", ErrRoundtripReply, o.gen.Head, reply)
	}
	var differ []string
	for _, f := range o.body.Fields {
		fv := FieldVerdict{Name: f.Name, Status: FieldNotCompared}
		if a, b := fieldValue(ours, f.Name), fieldValue(theirs, f.Name); f.Required && (a == nil || b == nil || !SameValue(a, b)) {
			fv.Status, fv.Detail = FieldDiffers, fmt.Sprintf("answer %v, re-extracted %v", show(a), show(b))
			differ = append(differ, f.Name+": "+fv.Detail)
		} else if f.Required {
			fv.Status = FieldAgreed
		}
		o.last = append(o.last, fv)
	}
	if len(differ) > 0 {
		return oracle.Verdict{Detail: "fields differ: " + strings.Join(differ, "; ")}, nil
	}
	return oracle.Verdict{Passed: true, Detail: fmt.Sprintf("all %d required fields agree", o.required)}, nil
}

// JudgeRoundtrip runs the round trip on answer with no spec, for a caller that already has the
// field list and the source, as the bench does. Required fields are the ones compared. An
// answer that is not a JSON object has no verdicts; a second reply that is not one is ErrRoundtripReply.
func JudgeRoundtrip(ctx context.Context, fields []Field, source, answer string,
	ask func(ctx context.Context, prompt string) (string, error)) ([]FieldVerdict, error) {
	o := &roundtripOracle{body: fieldsBody{Fields: fields, Source: source}, gen: GenInfo{Head: "the second model"},
		ask: func(ctx context.Context, _ GenInfo, prompt string) (string, error) { return ask(ctx, prompt) }}
	if _, err := o.Verify(ctx, answer, trust.Task{}); err != nil {
		return nil, err
	}
	return o.last, nil
}

func show(v any) string {
	if v == nil {
		return "nothing"
	}
	return fmt.Sprintf("%q", valueText(v))
}
