// SPDX-License-Identifier: MIT

package checkgen

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/internal/util"
)

const invoice = "Invoice INV-2291 from Acme  Supplies Ltd.\nTotal due: 1,200.50 USD by 30 June."

var invoiceFields = `[
	{"name": "vendor", "type": "string", "required": true, "from_source": true},
	{"name": "total", "type": "number", "required": true, "from_source": true},
	{"name": "paid", "type": "bool", "required": false},
	{"name": "lines", "type": "list", "required": false},
	{"name": "", "type": "string"},
	{"name": "notes", "type": "html"}
]`

// fieldsCheck generates the invoice spec from req and returns the answer's verdict.
func fieldsCheck(t *testing.T, req, answer string) (bool, string) {
	t.Helper()
	task := Task{Text: req, Kind: "extraction.finance", Writer: GenInfo{Head: "google/gemini", Family: "google"},
		Ask: func(context.Context, string) (string, error) { return invoiceFields, nil }}
	specs, err := fieldsGen{}.Generate(context.Background(), task)
	if err != nil || len(specs) != 1 {
		t.Fatalf("specs = %v, err = %v", specs, err)
	}
	o, err := fieldsGen{}.Oracle(specs[0])
	if err != nil {
		t.Fatal(err)
	}
	v, err := o.Verify(context.Background(), answer, trust.Task{})
	if err != nil {
		t.Fatal(err)
	}
	return v.Passed, v.Detail
}

var withSource = "Pull the vendor and total from this invoice.\n" + util.WrapUntrusted("DOCUMENT", invoice)

func TestFields_MissingRequiredFails(t *testing.T) {
	for _, answer := range []string{`{"vendor": "Acme Supplies Ltd."}`, `{"vendor": "Acme Supplies Ltd.", "total": null}`} {
		if passed, detail := fieldsCheck(t, withSource, answer); passed || !strings.Contains(detail, `"total"`) {
			t.Errorf("%s: passed=%v detail=%q, want total named missing", answer, passed, detail)
		}
	}
	if passed, detail := fieldsCheck(t, withSource, "The vendor is Acme."); passed || !strings.Contains(detail, "not a JSON object") {
		t.Errorf("prose passed=%v detail=%q", passed, detail)
	}
}

func TestFields_WrongTypeFails(t *testing.T) {
	passed, detail := fieldsCheck(t, withSource, `{"vendor": "Acme Supplies Ltd.", "total": "1200.50"}`)
	if passed || detail != `field "total" is a string, want a number` {
		t.Errorf("passed=%v detail=%q", passed, detail)
	}
}

func TestFields_ValueNotInSourceFails(t *testing.T) {
	for _, answer := range []string{
		`{"vendor": "Apex Supplies", "total": 1200.5}`,
		`{"vendor": "Acme Supplies Ltd.", "total": 1250}`,
	} {
		if passed, detail := fieldsCheck(t, withSource, answer); passed || !strings.Contains(detail, "not in the source") {
			t.Errorf("%s: passed=%v detail=%q, want an invented value caught", answer, passed, detail)
		}
	}
}

func TestFields_ValueInSourceWithDifferentSpacingPasses(t *testing.T) {
	answer := "```json\n{\"vendor\": \"acme supplies\\nLTD.\", \"total\": 1200.50, \"paid\": false, \"lines\": [\"INV-2291\"]}\n```"
	if passed, detail := fieldsCheck(t, withSource, answer); !passed || detail != "" {
		t.Errorf("passed=%v detail=%q, want a pass for the source's own values", passed, detail)
	}
}

func TestFields_NoSourceStillChecksShape(t *testing.T) {
	const req = "Give me the vendor and total as JSON."
	passed, detail := fieldsCheck(t, req, `{"vendor": "Anyone", "total": 3}`)
	if !passed || !strings.Contains(detail, "no fenced source") {
		t.Errorf("passed=%v detail=%q, want a pass that says the source was not checked", passed, detail)
	}
	if passed, _ := fieldsCheck(t, req, `{"vendor": 7, "total": 3}`); passed {
		t.Error("a wrong type passed because there was no source")
	}
}

func TestFields_WriterFailuresAreErrors(t *testing.T) {
	for _, ask := range []func(context.Context, string) (string, error){
		nil,
		func(context.Context, string) (string, error) { return "", errors.New("down") },
		func(context.Context, string) (string, error) { return "vendor and total", nil },
	} {
		if _, err := (fieldsGen{}).Generate(context.Background(), Task{Text: "x", Ask: ask}); err == nil {
			t.Error("a writer failure gave no error")
		}
	}
	specs, err := fieldsGen{}.Generate(context.Background(), Task{Text: "x",
		Ask: func(context.Context, string) (string, error) { return `[{"name": "a", "type": "date"}]`, nil }})
	if err != nil || len(specs) != 0 {
		t.Errorf("only unknown types gave specs %v, err %v; want no check", specs, err)
	}
	if _, err := (fieldsGen{}).Oracle(Spec{Body: []byte(`[`)}); err == nil {
		t.Error("a corrupt spec built an oracle")
	}
}

func TestSameValue_NumbersByValueTextFoldedListsInOrder(t *testing.T) {
	for _, tc := range []struct {
		a, b any
		want bool
	}{
		{"Lisbon", " lisbon ", true},
		{"7 pm", "7  PM", true},
		{"1,200", json.Number("1200"), true},
		{"4", json.Number("4.0"), true},
		{"4 people", json.Number("4"), false},
		{"7 pm", "Lisbon", false},
		{"true", true, true},
		{[]any{"a", "b"}, []any{"A", "b"}, true},
		{[]any{"a", "b"}, []any{"b", "a"}, false},
		{[]any{"a"}, "a", false},
	} {
		if got := SameValue(tc.a, tc.b); got != tc.want {
			t.Errorf("SameValue(%#v, %#v) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
	if _, ok := DecodeObject("```json\n{\"a\": 1}\n```"); !ok {
		t.Error("a fenced object was not read")
	}
	if _, ok := DecodeObject(`["a"]`); ok {
		t.Error("a list was read as an object")
	}
}
