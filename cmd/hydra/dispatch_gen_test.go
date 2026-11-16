// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/util"
)

const genPrompt = "Is this a refund, billing or shipping question? I want my money back."

// genHeads answers with cody (sourcegraph) and lets claude (anthropic), another family, write the checks.
func genHeads(t *testing.T, answer string) {
	t.Helper()
	s, _ := dispatchable(t, answer)
	s.FakeBinary(t, "claude", testutil.EchoScript(`["refund", "billing", "shipping"]`))
}

func TestDispatch_CheckGen_RunsGeneratorForKind(t *testing.T) {
	genHeads(t, "refund")
	out, cobraOut, err := run(t, "dispatch", "--no-stream", "--tier", "6", "--kind", "classification", "--check", "gen", genPrompt)
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	if !strings.Contains(out, "PASS  gen:labels") || !strings.Contains(out, "generated checks: all passed") ||
		!strings.Contains(out, "by claude") {
		t.Fatalf("no label check verdict printed:\n%s", out)
	}
	answers, err := evalset.Joint(evalset.VerdictsPath())
	if err != nil || len(answers) != 1 || len(answers[0].Checks) != 1 {
		t.Fatalf("verdict log = %+v, %v", answers, err)
	}
	if v := answers[0].Checks[0]; v.Check != "gen:labels" || !v.Passed || v.GenFamily != "anthropic" || v.Head != "cody" {
		t.Errorf("verdict = %+v, want a passing gen:labels written by anthropic about cody's answer", v)
	}
}

func TestDispatch_CheckGen_NoKindNotChecked(t *testing.T) {
	genHeads(t, "refund")
	out, cobraOut, err := run(t, "dispatch", "--no-stream", "--tier", "6", "--check", "gen", genPrompt)
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	if !strings.Contains(out, "NOT CHECKED  gen  no kind of work") || strings.Contains(out, "PASS") ||
		!strings.Contains(out, "not verified: gen: no kind of work") {
		t.Fatalf("a dispatch with no kind did not say so:\n%s", out)
	}
}

func TestDispatch_CheckGen_BudgetStopsChecks(t *testing.T) {
	genHeads(t, "refund")
	out, cobraOut, err := run(t, "dispatch", "--no-stream", "--tier", "6", "--kind", "classification",
		"--check", "gen", "--max-usd", "0.000000001", genPrompt)
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	if !strings.Contains(out, "NOT CHECKED  gen:labels  budget") || !strings.Contains(out, "generated checks: not checked") {
		t.Fatalf("a spent budget did not stop the checks:\n%s", out)
	}
}

func TestDispatch_CheckGen_UnknownRefusedBeforeRouting(t *testing.T) {
	genHeads(t, "refund")
	_, _, err := run(t, "dispatch", "--no-stream", "--tier", "6", "--check", "gen:vibes", genPrompt)
	if err == nil || !strings.Contains(err.Error(), `no generated check "vibes"`) {
		t.Fatalf("err = %v, want gen:vibes refused", err)
	}
}

func TestDispatch_CheckGenFields_PrintsEveryBrokenField(t *testing.T) {
	s, _ := dispatchable(t, `{"vendor": "Apex", "total": "9", "date": "30 June"}`)
	s.FakeBinary(t, "claude", testutil.EchoScript(`[{"name": "vendor", "type": "string", "required": true, "from_source": true},
{"name": "total", "type": "number", "required": true}, {"name": "date", "type": "string", "from_source": true},
{"name": "ref", "type": "string", "required": true}]`))
	prompt := "Pull vendor, total, date and ref as JSON.\n" + util.WrapUntrusted("DOCUMENT", "Acme Ltd, total 1,200.50, due 30 June")
	out, cobraOut, err := run(t, "dispatch", "--no-stream", "--tier", "6", "--kind", "extraction", "--check", "gen:fields", prompt)
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	for _, want := range []string{"FAIL  gen:fields", "not_in_source  vendor", "wrong_type     total", "missing        ref", "1 of 4 fields ok"} {
		if !strings.Contains(out, want) {
			t.Errorf("output has no %q:\n%s", want, out)
		}
	}
}

// One run with both field checks prints each field once, merged.
func TestDispatch_FieldsAndRoundtripMergePerField(t *testing.T) {
	s, _ := dispatchable(t, `{"vendor": "Acme Ltd", "total": 9, "date": "30 June", "paid": true}`)
	// The writer's one reply serves both asks: the field list is the [...] part, the re-extraction the fenced object.
	s.FakeBinary(t, "claude", testutil.EchoScript("```json\n"+`{"vendor": "acme ltd", "total": 1200.5, "paid": true}`+"\n```\n"+
		`[{"name": "vendor", "type": "string", "required": true, "from_source": true}, {"name": "total", "type": "number", "required": true},
{"name": "date", "type": "string", "from_source": true}, {"name": "paid", "type": "bool", "required": true}]`))
	prompt := "Pull vendor, total, date and paid as JSON.\n" + util.WrapUntrusted("DOCUMENT", "Acme Ltd, total 1,200.50, due 30 June, paid")
	out, cobraOut, err := run(t, "dispatch", "--no-stream", "--tier", "6", "--kind", "extraction",
		"--check", "gen:fields", "--check", "gen:roundtrip", prompt)
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	for _, want := range []string{"PASS  gen:fields", "FAIL  gen:roundtrip", "in_source_and_agreed vendor",
		"differs              total", "in_source            date", "agreed               paid", "3 of 4 fields ok"} {
		if !strings.Contains(out, want) {
			t.Errorf("output has no %q:\n%s", want, out)
		}
	}
	if n := regexp.MustCompile(`(?m)^ {4}\S+ +vendor  `).FindAllString(out, -1); len(n) != 1 {
		t.Errorf("vendor printed %d times, want once:\n%s", len(n), out)
	}
}

// The schema is the field list, so the writer model is never asked for one.
func TestDispatch_SchemaSkipsWriterCall(t *testing.T) {
	s, _ := dispatchable(t, `{"vendor": "Apex", "total": 1200.5}`)
	// Asked, the writer would add a field the schema does not have.
	s.FakeBinary(t, "claude", testutil.EchoScript(`[{"name": "writer_field", "type": "string", "required": true}]`))
	schema := filepath.Join(t.TempDir(), "invoice.schema.json")
	body := `{"required": ["vendor", "total"], "patternProperties": {}, "properties": {"vendor": {"type": "string"}, "total": {"type": "number"}}}`
	if err := os.WriteFile(schema, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	prompt := "Pull vendor and total as JSON.\n" + util.WrapUntrusted("DOCUMENT", "Acme Ltd, total 1,200.50")
	out, cobraOut, err := run(t, "dispatch", "--no-stream", "--tier", "6", "--schema", schema, prompt)
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	if !strings.Contains(out, "FAIL  gen:fields") || !strings.Contains(out, "not_in_source  vendor") ||
		!strings.Contains(out, "these keywords are not read: patternProperties") {
		t.Errorf("the schema's fields were not checked:\n%s", out)
	}
	if strings.Contains(out, "writer_field") {
		t.Errorf("the writer was asked for a field list the schema already gave:\n%s", out)
	}
}

func TestDispatch_BadSchemaRefusedBeforeRouting(t *testing.T) {
	cliSandbox(t)
	schema := filepath.Join(t.TempDir(), "deep.json")
	if err := os.WriteFile(schema, []byte(`{"properties": {"a": {"type": "object", "properties": {"b": {"type": "object"}}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, "dispatch", "--schema", schema, "q"); err == nil || !strings.Contains(err.Error(), "more than one level") {
		t.Fatalf("err = %v, want deep nesting refused", err)
	}
}

// gen:tests needs the file under change and runs Go only; anything else is not checked.
func TestDispatch_CheckGenTests_NotCheckedWithoutAGoFile(t *testing.T) {
	for check, want := range map[string]string{
		"gen":              "NOT CHECKED  gen:tests  no file under change",
		"gen:tests=app.py": "NOT CHECKED  gen:tests  generated tests cover Go only for now, so app.py is not checked",
	} {
		genHeads(t, "def f(): pass")
		out, cobraOut, err := run(t, "dispatch", "--no-stream", "--tier", "6", "--kind", "code", "--check", check, "fix f")
		if err != nil {
			t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
		}
		if !strings.Contains(out, want) || strings.Contains(out, "PASS") {
			t.Errorf("--check %s printed:\n%s\nwant %q", check, out, want)
		}
	}
}

func TestDispatch_CheckGen_OnlyTestsNamesAFile(t *testing.T) {
	genHeads(t, "refund")
	_, _, err := run(t, "dispatch", "--no-stream", "--tier", "6", "--check", "gen:labels=x.txt", genPrompt)
	if err == nil || !strings.Contains(err.Error(), "only gen:tests names a file") {
		t.Fatalf("err = %v, want gen:labels=<file> refused", err)
	}
}

// The writer names a total beside the field list, so plain gen adds a sum check that catches a made-up total.
func TestDispatch_CheckGenAddsTheWritersSum(t *testing.T) {
	s, _ := dispatchable(t, `{"items": [{"amount": 40}, {"amount": 2.50}], "total": 45.00}`)
	s.FakeBinary(t, "claude", testutil.EchoScript(`{"fields": [{"name": "items", "type": "list", "required": true},
{"name": "total", "type": "number", "required": true}], "sums": {"total": "items[].amount"}}`))
	out, cobraOut, err := run(t, "dispatch", "--no-stream", "--tier", "6", "--kind", "extraction", "--check", "gen", "Pull the invoice lines and total as JSON.")
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	for _, want := range []string{"PASS  gen:fields", "FAIL  gen:sum-total", "adds up to 42.50", "generated checks: some failed"} {
		if !strings.Contains(out, want) {
			t.Errorf("output has no %q:\n%s", want, out)
		}
	}
}
