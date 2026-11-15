// SPDX-License-Identifier: MIT

package checkgen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/internal/util"
)

const fieldsPrompt = "The request below asks for structured output. List the fields the answer must have, " +
	`as a JSON array of {"name": string, "type": "string"|"number"|"bool"|"list", "required": bool, ` +
	`"from_source": bool}. Set from_source when the value must be copied from the document in the request.` + "\n\n"

var fieldTypes = []string{"string", "number", "bool", "list"}

// Field is one field an extraction answer must carry.
type Field struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Required   bool   `json:"required"`
	FromSource bool   `json:"from_source"`
}

// fieldsBody is the spec: the fields, and the fenced source their values must come from.
type fieldsBody struct {
	Fields []Field `json:"fields"`
	Source string  `json:"source,omitempty"`
}

// fieldsGen asks the writer for the field list; the check itself needs no model.
type fieldsGen struct{}

func init() {
	if err := Register("extraction", fieldsGen{}); err != nil {
		panic(err)
	}
}

func (fieldsGen) Kind() string { return "fields" }

// Generate drops a field with no name or an unknown type. No field left is no check.
func (fieldsGen) Generate(ctx context.Context, task Task) ([]Spec, error) {
	if task.Ask == nil {
		return nil, errors.New("checkgen fields: no writer head to ask")
	}
	reply, err := task.Ask(ctx, fieldsPrompt+util.WrapUntrusted("REQUEST", task.Text))
	if err != nil {
		return nil, err
	}
	var listed []Field
	start, end := strings.Index(reply, "["), strings.LastIndex(reply, "]")
	if start < 0 || end < start || json.Unmarshal([]byte(reply[start:end+1]), &listed) != nil {
		return nil, fmt.Errorf("checkgen fields: writer did not reply with a JSON list: %.80q", reply)
	}
	body := fieldsBody{Source: sourceOf(task.Text)}
	seen := map[string]bool{}
	for _, f := range listed {
		if f.Name = strings.TrimSpace(f.Name); f.Name == "" || seen[f.Name] || !slices.Contains(fieldTypes, f.Type) {
			continue
		}
		seen[f.Name] = true
		body.Fields = append(body.Fields, f)
	}
	if len(body.Fields) == 0 {
		return nil, nil
	}
	s, err := NewSpec("fields", body, task, task.Writer)
	if err != nil {
		return nil, err
	}
	return []Spec{s}, nil
}

func (fieldsGen) Oracle(s Spec) (oracle.Oracle, error) {
	var b fieldsBody
	if err := json.Unmarshal(s.Body, &b); err != nil {
		return nil, fmt.Errorf("checkgen fields: spec %s: %w", s.ID, err)
	}
	return fieldsOracle(b), nil
}

// sourceOf is every span Hydra fenced into the request, the only text a value may be copied from.
func sourceOf(text string) string {
	var parts []string
	for _, u := range util.Unwrap(text) {
		parts = append(parts, u.Content)
	}
	return strings.Join(parts, "\n")
}

type fieldsOracle fieldsBody

// Verify fails on the first broken field: not JSON, missing, wrong type, or not in the source.
func (o fieldsOracle) Verify(_ context.Context, candidate string, _ trust.Task) (oracle.Verdict, error) {
	got, ok := DecodeObject(candidate)
	if !ok {
		return oracle.Verdict{Detail: "the answer is not a JSON object"}, nil
	}
	src := source{text: fold(o.Source), numbers: numbersIn(o.Source)}
	for _, f := range o.Fields {
		v, ok := got[f.Name]
		if !ok || v == nil {
			if f.Required {
				return oracle.Verdict{Detail: fmt.Sprintf("field %q is missing", f.Name)}, nil
			}
			continue
		}
		if t := typeOf(v); t != f.Type {
			return oracle.Verdict{Detail: fmt.Sprintf("field %q is a %s, want a %s", f.Name, t, f.Type)}, nil
		}
		if f.FromSource && src.text != "" && !src.has(v) {
			return oracle.Verdict{Detail: fmt.Sprintf("field %q is not in the source", f.Name)}, nil
		}
	}
	if o.Source == "" && slices.ContainsFunc(o.Fields, func(f Field) bool { return f.FromSource }) {
		return oracle.Verdict{Passed: true, Detail: "no fenced source, so values were not checked against one"}, nil
	}
	return oracle.Verdict{Passed: true}, nil
}

// DecodeObject reads an answer as one JSON object, from inside its first ``` block
// if it has one, with numbers kept as written.
func DecodeObject(answer string) (map[string]any, bool) {
	dec := json.NewDecoder(strings.NewReader(unfence(answer)))
	dec.UseNumber()
	var got map[string]any
	if err := dec.Decode(&got); err != nil || got == nil {
		return nil, false
	}
	return got, true
}

// SameValue compares two field values the way the fields check reads them: numbers
// by value, so "1,200" is 1200, text folded for case and spacing, lists item by item.
func SameValue(a, b any) bool {
	if la, ok := a.([]any); ok {
		lb, ok := b.([]any)
		return ok && slices.EqualFunc(la, lb, SameValue)
	}
	ta, tb := valueText(a), valueText(b)
	if na, okA := number(ta); okA {
		nb, okB := number(tb)
		return okB && na == nb
	}
	return fold(ta) == fold(tb)
}

func valueText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	}
	return fmt.Sprint(v)
}

// number reads s as one whole number with thousands commas allowed, nothing around it.
func number(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if !wholeNumber.MatchString(s) {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", ""), 64)
	return f, err == nil
}

var wholeNumber = regexp.MustCompile(`^` + sourceNumber.String() + `$`)

// unfence takes the inside of the first ``` block, if the answer has one.
func unfence(s string) string {
	_, rest, ok := strings.Cut(s, "```")
	if !ok {
		return s
	}
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[nl+1:] // the info string, e.g. "json"
	}
	body, _, _ := strings.Cut(rest, "```")
	return body
}

func typeOf(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case json.Number:
		return "number"
	case bool:
		return "bool"
	case []any:
		return "list"
	}
	return "object"
}

// source is the fenced text folded once, and every number in it read once.
type source struct {
	text    string
	numbers []float64
}

// has checks a string by folded text and a number by value; a list needs every item found.
func (s source) has(v any) bool {
	switch x := v.(type) {
	case string:
		return strings.Contains(s.text, fold(x))
	case json.Number:
		want, err := x.Float64()
		return err == nil && slices.Contains(s.numbers, want)
	case []any:
		for _, item := range x {
			if !s.has(item) {
				return false
			}
		}
		return true
	}
	return true
}

func fold(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

var sourceNumber = regexp.MustCompile(`-?\d[\d,]*(?:\.\d+)?`)

// numbersIn reads every number in src, with thousands commas dropped, so "1,200.50" is 1200.5.
func numbersIn(src string) []float64 {
	var out []float64
	for _, m := range sourceNumber.FindAllString(src, -1) {
		if f, err := strconv.ParseFloat(strings.ReplaceAll(m, ",", ""), 64); err == nil {
			out = append(out, f)
		}
	}
	return out
}
