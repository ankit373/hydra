// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/bench"
)

// runFixture runs the Banking77 fixture to completion and returns its results dir and run id.
func runFixture(t *testing.T) (results, id string) {
	t.Helper()
	args, _, results := benchFixture(t, "1.0")
	if out, _, err := run(t, args...); err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	dirs, _ := os.ReadDir(results)
	return results, dirs[0].Name()
}

func TestBenchReport_WritesTheSummaryAndPrintsTheTable(t *testing.T) {
	results, id := runFixture(t)
	out, _, err := run(t, "bench", "report", id, "--results", results)
	if err != nil || !strings.Contains(out, "| banking77 | frontier | 4 |") || !strings.Contains(out, "| banking77 | oracle |") {
		t.Fatalf("report: %v\n%s", err, out)
	}
	first, _ := os.ReadFile(filepath.Join(results, id, "summary.json"))
	js, _, err := run(t, "bench", "report", id, "--results", results, "--json")
	if err != nil || js != string(first) {
		t.Fatalf("report --json is not the byte-identical summary.json: %v", err)
	}
	if _, _, err := run(t, "bench", "report", "no-such-run", "--results", results); err == nil {
		t.Fatal("reported a run that does not exist")
	}
}

// Every breadcrumb field is filled for a direct-only run, except the two that
// only a served arm has (scope E7.1-8).
func TestBenchReport_BreadcrumbHasNoEmptyField(t *testing.T) {
	results, id := runFixture(t)
	if _, _, err := run(t, "bench", "report", id, "--results", results); err != nil {
		t.Fatal(err)
	}
	s, err := bench.ReadSummary(filepath.Join(results, id, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	mayBeEmpty := map[string]bool{
		"Config": true, "CalibSHA256": true, // served arms only
		"Dirty": true, "Temperature": true, // false and 0 are readings
	}
	v := reflect.ValueOf(s.Breadcrumb)
	for i := 0; i < v.NumField(); i++ {
		name, f := v.Type().Field(i).Name, v.Field(i)
		if mayBeEmpty[name] {
			continue
		}
		if f.IsZero() || (f.Kind() == reflect.Map && f.Len() == 0) {
			t.Errorf("breadcrumb %s is empty", name)
		}
	}
}

func TestBenchTable_WritesFromSummariesOnly(t *testing.T) {
	results, id := runFixture(t)
	if _, _, err := run(t, "bench", "table", id, "--results", results); err == nil {
		t.Fatal("tabled a run with no summary.json")
	}
	run(t, "bench", "report", id, "--results", results)
	// table reads summary.json and nothing else: remove everything else first.
	os.Remove(filepath.Join(results, id, "items.jsonl"))
	os.Remove(filepath.Join(results, id, "run.json"))
	out, _, err := run(t, "bench", "table", id, "--results", results)
	if err != nil || !strings.Contains(out, "table.md") {
		t.Fatalf("table: %v\n%s", err, out)
	}
	md, _ := os.ReadFile(filepath.Join(results, id, "table.md"))
	if !strings.Contains(string(md), "| banking77 | frontier | 4 |") || !strings.Contains(string(md), "Runs: "+id) {
		t.Fatalf("table.md:\n%s", md)
	}
	if _, _, err := run(t, "bench", "table", id, id, "--results", results); err == nil {
		t.Fatal("two runs tabled with no --out")
	}
	out2 := filepath.Join(t.TempDir(), "t.md")
	if _, _, err := run(t, "bench", "table", id, id, "--results", results, "--out", out2); err != nil {
		t.Fatal(err)
	}
}
