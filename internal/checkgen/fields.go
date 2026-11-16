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
	`as one JSON object {"fields": [{"name": string, "type": "string"|"number"|"bool"|"list", "required": bool, ` +
	`"from_source": bool}], "sums": {"TOTAL": "LIST[].KEY" or "FIELD,FIELD"}}, with field names in place of the capitals. ` +
	`Set from_source when the value must be copied from the document in the request. ` +
	`Add sums only for a total that should equal the sum of other fields; leave it out otherwise.` + "\n\n"

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
	if task.Fields != nil {
		return schemaSpec(task)
	}
	if task.Ask == nil {
		return nil, errors.New("checkgen fields: no writer head to ask")
	}
	reply, err := task.Ask(ctx, fieldsPrompt+util.WrapUntrusted("REQUEST", task.Text))
	if err != nil {
		return nil, err
	}
	listed, sums, err := parseFieldList(reply)
	if err != nil {
		return nil, err
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
	s, err := NewSpec("fields", "", body, task, task.Writer)
	if err != nil {
		return nil, err
	}
	specs := []Spec{s}
	for _, sum := range keepSums(sums, body.Fields) {
		total, _, _ := strings.Cut(sum, "=")
		s, err := NewSpec("sum", total, sumBody{Sum: sum}, task, task.Writer)
		if err != nil {
			return nil, err
		}
		specs = append(specs, s)
	}
	return specs, nil
}

// parseFieldList reads the writer's {"fields": [...], "sums": {...}}, or a bare field array.
func parseFieldList(reply string) ([]Field, map[string]string, error) {
	var obj struct {
		Fields []Field           `json:"fields"`
		Sums   map[string]string `json:"sums"`
	}
	if i := strings.Index(reply, "{"); i >= 0 {
		if err := json.NewDecoder(strings.NewReader(reply[i:])).Decode(&obj); err == nil && len(obj.Fields) > 0 {
			return obj.Fields, obj.Sums, nil
		}
	}
	start, end := strings.Index(reply, "["), strings.LastIndex(reply, "]")
	if start < 0 || end < start || json.Unmarshal([]byte(reply[start:end+1]), &obj.Fields) != nil {
		return nil, nil, fmt.Errorf("checkgen fields: writer did not reply with a JSON list: %.80q", reply)
	}
	return obj.Fields, nil, nil
}

// schemaSpec is the fields spec for the user's schema: no writer call, and a value is
// held to the source only when the request fenced one.
func schemaSpec(task Task) ([]Spec, error) {
	body := fieldsBody{Source: sourceOf(task.Text), Fields: slices.Clone(task.Fields)}
	for i := range body.Fields {
		body.Fields[i].FromSource = body.Fields[i].FromSource && body.Source != ""
	}
	s, err := NewSpec("fields", "", body, task, GenInfo{Head: "schema"})
	if err != nil {
		return nil, err
	}
	return []Spec{s}, nil
}

func (fieldsGen) Oracle(s Spec, _ Env) (oracle.Oracle, error) {
	var b fieldsBody
	if err := json.Unmarshal(s.Body, &b); err != nil {
		return nil, fmt.Errorf("checkgen fields: spec %s: %w", s.ID, err)
	}
	return &fieldsOracle{fieldsBody: b}, nil
}

// sourceOf is every span Hydra fenced into the request, the only text a value may be copied from.
func sourceOf(text string) string {
	var parts []string
	for _, u := range util.Unwrap(text) {
		parts = append(parts, u.Content)
	}
	return strings.Join(parts, "\n")
}

// A field's status. Unverified is a from-source value with no fenced source to check it against.
// Agreed, differs and not_compared come from the round trip; the in_source ones from MergeFields.
const (
	FieldOK                = "ok"
	FieldAbsent            = "absent" // optional, not given
	FieldMissing           = "missing"
	FieldWrongType         = "wrong_type"
	FieldNotInSource       = "not_in_source"
	FieldUnverified        = "unverified"
	FieldAgreed            = "agreed"
	FieldDiffers           = "differs"
	FieldNotCompared       = "not_compared" // optional, so the round trip does not compare it
	FieldInSource          = "in_source"
	FieldInSourceAndAgreed = "in_source_and_agreed"
)

// FieldVerdict is one field's result. InSource is set when the value was found in the fenced source.
type FieldVerdict struct {
	Name, Status, Detail string
	InSource             bool
}

func (f FieldVerdict) failed() bool {
	switch f.Status {
	case FieldMissing, FieldWrongType, FieldNotInSource, FieldDiffers:
		return true
	}
	return false
}

// Good says the field passed: ok, or found in the source, or agreed by a second model.
func (f FieldVerdict) Good() bool {
	switch f.Status {
	case FieldOK, FieldInSource, FieldAgreed, FieldInSourceAndAgreed:
		return true
	}
	return false
}

// fieldsOracle keeps every field's verdict from the last Verify.
type fieldsOracle struct {
	fieldsBody
	last []FieldVerdict
}

// Verify walks every field and fails when any is missing, of the wrong type, or not in the source.
func (o *fieldsOracle) Verify(_ context.Context, candidate string, _ trust.Task) (oracle.Verdict, error) {
	o.last = make([]FieldVerdict, 0, len(o.Fields))
	got, ok := DecodeObject(candidate)
	if !ok {
		return oracle.Verdict{Detail: "the answer is not a JSON object"}, nil
	}
	src := source{text: fold(o.Source), numbers: numbersIn(o.Source)}
	var broken []string
	for _, f := range o.Fields {
		fv := judgeField(f, fieldValue(got, f.Name), src)
		o.last = append(o.last, fv)
		if fv.failed() {
			broken = append(broken, fmt.Sprintf("field %q %s", f.Name, fv.Detail))
		}
	}
	if len(broken) > 0 {
		return oracle.Verdict{Detail: strings.Join(broken, "; ")}, nil
	}
	if slices.ContainsFunc(o.last, func(f FieldVerdict) bool { return f.Status == FieldUnverified }) {
		return oracle.Verdict{Passed: true, Detail: "no fenced source, so values were not checked against one"}, nil
	}
	return oracle.Verdict{Passed: true}, nil
}

// JudgeFields runs the fields check on answer with no spec, for a caller that already
// has the field list and the source, as the bench does. Not a JSON object is no verdicts.
func JudgeFields(fields []Field, source, answer string) []FieldVerdict {
	o := &fieldsOracle{fieldsBody: fieldsBody{Fields: fields, Source: source}}
	_, _ = o.Verify(context.Background(), answer, trust.Task{}) // it never errors
	return o.last
}

func judgeField(f Field, v any, src source) FieldVerdict {
	fv := FieldVerdict{Name: f.Name, Status: FieldOK}
	switch {
	case v == nil && f.Required:
		fv.Status, fv.Detail = FieldMissing, "is missing"
	case v == nil:
		fv.Status = FieldAbsent
	case typeOf(v) != f.Type:
		fv.Status, fv.Detail = FieldWrongType, fmt.Sprintf("is a %s, want a %s", typeOf(v), f.Type)
	case !f.FromSource:
	case src.text == "":
		fv.Status, fv.Detail = FieldUnverified, "no fenced source"
	case !src.has(v):
		fv.Status, fv.Detail = FieldNotInSource, "is not in the source"
	default:
		fv.InSource = true
	}
	return fv
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

// fieldValue is obj's key name, or for a dotted name with no such key, the path a.b.
func fieldValue(obj map[string]any, name string) any {
	if v, ok := obj[name]; ok {
		return v
	}
	parent, child, ok := strings.Cut(name, ".")
	if m, isObj := obj[parent].(map[string]any); ok && isObj {
		return fieldValue(m, child)
	}
	return nil
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
