// SPDX-License-Identifier: MIT

package checkgen

import (
	"context"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/internal/util"
)

const invoiceSchema = `{
	"$schema": "https://json-schema.org/draft/2020-12/schema",
	"title": "Invoice",
	"type": "object",
	"required": ["vendor", "total", "address"],
	"additionalProperties": false,
	"properties": {
		"vendor": {"type": "string", "minLength": 1},
		"total": {"type": "number"},
		"lines": {"type": "integer"},
		"paid": {"type": ["boolean", "null"]},
		"items": {"type": "array", "items": {"type": "object"}},
		"address": {"type": "object", "required": ["city"], "properties": {
			"city": {"type": "string"},
			"zip": {"type": "string"}
		}}
	}
}`

func TestSchemaFields_MapsTypesAndRequired(t *testing.T) {
	fields, unread, err := SchemaFields([]byte(invoiceSchema))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range fields {
		got = append(got, f.Name+":"+f.Type+map[bool]string{true: ":req"}[f.Required]+map[bool]string{true: ":src"}[f.FromSource])
	}
	want := "vendor:string:req:src total:number:req:src lines:number:src paid:bool items:list address.city:string:req:src address.zip:string:src"
	if strings.Join(got, " ") != want {
		t.Errorf("fields = %s\nwant     %s", strings.Join(got, " "), want)
	}
	if strings.Join(unread, ",") != "additionalProperties,items,minLength" {
		t.Errorf("unread keywords = %v", unread)
	}
}

func TestSchemaFields_RefusesDeepNesting(t *testing.T) {
	for schema, want := range map[string]string{
		`{"properties": {"a": {"type": "object", "properties": {"b": {"type": "object"}}}}}`: "a.b: objects nest more than one level",
		`{"properties": {"a": {"type": "null"}}}`:                                            `a: type "null" is not one of`,
		`{"properties": {"a": {}}}`:                                                          `a: no "type"`,
		`{"type": "object"}`:                                                                 "no top-level properties",
		`[1]`:                                                                                "not a JSON object",
	} {
		if _, _, err := SchemaFields([]byte(schema)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", schema, err, want)
		}
	}
}

func TestSchemaFields_CheckedWithoutAWriterAndNested(t *testing.T) {
	fields, _, err := SchemaFields([]byte(invoiceSchema))
	if err != nil {
		t.Fatal(err)
	}
	src := "Acme Ltd, 2 lines, total 1,200.50, ship to Pune 411001"
	task := Task{Text: "Pull the invoice as JSON.\n" + util.WrapUntrusted("DOCUMENT", src), Fields: fields,
		Ask: func(context.Context, string) (string, error) { t.Fatal("the writer was asked"); return "", nil }}
	specs, err := fieldsGen{}.Generate(context.Background(), task)
	if err != nil || len(specs) != 1 || specs[0].Generator.Head != "schema" {
		t.Fatalf("specs = %+v, err = %v", specs, err)
	}
	o, err := fieldsGen{}.Oracle(specs[0], Env{})
	if err != nil {
		t.Fatal(err)
	}
	for answer, passed := range map[string]bool{
		`{"vendor": "Acme Ltd", "total": 1200.5, "address": {"city": "Pune", "zip": "411001"}}`: true,
		`{"vendor": "Acme Ltd", "total": 1200.5, "address": {"zip": "411001"}}`:                 false,
		`{"vendor": "Acme Ltd", "total": 1200.5, "address": {"city": "Mumbai"}}`:                false,
	} {
		v, err := o.Verify(context.Background(), answer, trust.Task{})
		if err != nil || v.Passed != passed {
			t.Errorf("%s: verdict %+v, err %v, want passed=%v", answer, v, err, passed)
		}
	}
}
