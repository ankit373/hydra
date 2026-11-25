// SPDX-License-Identifier: MIT

package checkgen

import (
	"context"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/trust"
)

// A well name read as HLF-10 by two models that agree: the pattern says the last
// character is a letter, and the detail names the swap that would fit (#1981).
func TestFormat_PatternCatchesAConfusableMisread(t *testing.T) {
	fields := []Field{{Name: "well", Type: "string", Required: true, FromSource: true, Pattern: `[A-Z]{3}-\d[A-Z]`}}
	source := "Monitoring well HLF-1O, sampled in March."

	bad := JudgeFields(fields, source, `{"well": "HLF-10"}`)
	if len(bad) != 1 || bad[0].Status != FieldBadFormat || !bad[0].failed() || bad[0].Good() {
		t.Fatalf("HLF-10 = %+v, want one bad_format that fails", bad)
	}
	if !strings.Contains(bad[0].Detail, `"HLF-1O" would`) {
		t.Errorf("detail = %q, want it to name HLF-1O", bad[0].Detail)
	}

	ok := JudgeFields(fields, source, `{"well": "HLF-1O"}`)
	if ok[0].Status != FieldOK || !ok[0].InSource {
		t.Errorf("HLF-1O = %+v, want ok and found in the source", ok[0])
	}
}

// A value that fits no pattern even with a swap says so without a guess, and the whole
// field is held to the pattern, so a match in the middle of the text does not pass.
func TestFormat_PatternIsAWholeValueMatch(t *testing.T) {
	fields := []Field{{Name: "ref", Type: "string", Pattern: `[A-Z]{2}\d{3}`}}
	for answer, want := range map[string]string{
		`{"ref": "KX482"}`:        FieldOK,
		`{"ref": "xxKX482 yy"}`:   FieldBadFormat,
		`{"ref": "KX48"}`:         FieldBadFormat,
		`{"ref": "no code here"}`: FieldBadFormat,
	} {
		got := JudgeFields(fields, "", answer)[0]
		if got.Status != want {
			t.Errorf("%s = %s (%s), want %s", answer, got.Status, got.Detail, want)
		}
		if got.Status == FieldBadFormat && strings.Contains(got.Detail, "would") && answer != `{"ref": "KX48"}` {
			t.Errorf("%s: detail %q offers a swap that is not there", answer, got.Detail)
		}
	}
}

// In a column of codes, the value with a lookalike where every other has the other class is flagged.
func TestFormat_ColumnMisfit(t *testing.T) {
	fields := []Field{{Name: "ids", Type: "list"}}
	cases := []struct {
		name, answer, want string
	}{
		{"letter O among digits", `{"ids": ["AB12", "AC34", "AD56", "AE7O"]}`, `"AE7O", with 'O' at position 4`},
		{"digit 0 among letters", `{"ids": ["K7QX", "K7QZ", "K7QM", "K7Q0"]}`, `"K7Q0", with '0' at position 4`},
		{"all the same class", `{"ids": ["AB12", "AC34", "AD56", "AE78"]}`, ""},
		{"too few to infer from", `{"ids": ["AB12", "AC34", "AE7O"]}`, ""},
		{"a letter that is not a lookalike", `{"ids": ["AB12", "AC34", "AD56", "AE7K"]}`, ""},
		{"two odd values share the position", `{"ids": ["AB12", "AC34", "AD7O", "AE7O"]}`, ""},
		{"other lengths are left out", `{"ids": ["AB12", "AC34", "AD56", "AE7O", "AE7OX"]}`, `"AE7O", with 'O' at position 4`},
	}
	for _, c := range cases {
		got := JudgeFields(fields, "", c.answer)[0]
		if c.want == "" && got.Status != FieldOK {
			t.Errorf("%s: %s (%s), want ok", c.name, got.Status, got.Detail)
		}
		if c.want != "" && (got.Status != FieldBadFormat || !strings.Contains(got.Detail, c.want)) {
			t.Errorf("%s: %s (%s), want bad_format containing %s", c.name, got.Status, got.Detail, c.want)
		}
	}
}

// The verdict fails the check, with the field named in the reason.
func TestFormat_BadFormatFailsTheCheck(t *testing.T) {
	o := &fieldsOracle{fieldsBody: fieldsBody{Fields: []Field{
		{Name: "well", Type: "string", Required: true, Pattern: `[A-Z]{3}-\d[A-Z]`},
	}}}
	v, err := o.Verify(context.Background(), `{"well": "HLF-10"}`, trust.Task{})
	if err != nil || v.Passed || !strings.Contains(v.Detail, `field "well"`) {
		t.Fatalf("verdict = %+v, %v; want a failure naming the field", v, err)
	}
}

func TestSchemaFields_ReadsPatternAndFormat(t *testing.T) {
	fields, unread, err := SchemaFields([]byte(`{"type": "object", "properties": {
		"well":  {"type": "string", "pattern": "[A-Z]{3}-\\d[A-Z]"},
		"day":   {"type": "string", "format": "date"},
		"note":  {"type": "string", "format": "hostname"},
		"look":  {"type": "string", "pattern": "^(?=a)a"},
		"count": {"type": "integer", "pattern": "x"}
	}}`))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range fields {
		got[f.Name] = f.Pattern
	}
	if got["well"] != `[A-Z]{3}-\d[A-Z]` || got["day"] != schemaFormats["date"] ||
		got["note"] != "" || got["look"] != "" || got["count"] != "" {
		t.Errorf("patterns = %q", got)
	}
	if strings.Join(unread, ",") != "pattern" {
		t.Errorf("unread = %v, want only the pattern RE2 cannot read", unread)
	}
}

// The writer cannot know a code's shape, so a pattern it invents is dropped.
func TestFormat_WriterPatternIsDropped(t *testing.T) {
	task := Task{Text: "x", Kind: "extraction.finance", Writer: GenInfo{Head: "writer"},
		Ask: func(context.Context, string) (string, error) {
			return `[{"name": "ref", "type": "string", "required": true, "pattern": "[0-9]+"}]`, nil
		}}
	specs, err := fieldsGen{}.Generate(context.Background(), task)
	if err != nil || len(specs) != 1 || strings.Contains(string(specs[0].Body), "pattern") {
		t.Fatalf("specs = %v, %v; want a body with no pattern", specs, err)
	}
}

// A pattern that cannot compile (only a hand-edited spec has one) leaves the field unverified, never failed.
func TestFormat_UnusablePatternIsUnverified(t *testing.T) {
	got := JudgeFields([]Field{{Name: "ref", Type: "string", Pattern: "("}}, "", `{"ref": "KX482"}`)[0]
	if got.Status != FieldUnverified || got.failed() {
		t.Errorf("verdict = %+v, want unverified", got)
	}
}
