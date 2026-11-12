// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Every check here is driven to both verdicts against a fixture catalog, so
// nothing in the suite touches the network.

var fixtureDay = time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)

func fixtureOptions() options {
	return options{
		registryDir: filepath.Join("testdata", "registry"),
		watchlist:   filepath.Join("testdata", "watchlist.yaml"),
		maxMovePct:  25,
		maxAgeDays:  90,
		today:       fixtureDay,
	}
}

func fixtureCatalog(t *testing.T) catalog {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cat, err := parseCatalog(f)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCheck_FixtureReportsEachKindOfDrift(t *testing.T) {
	rep, err := check(fixtureOptions(), fixtureCatalog(t))
	if err != nil {
		t.Fatal(err)
	}

	wantMissing := []missing{
		{Source: "testdata/watchlist.yaml: flash", Model: "google/gemini-3.8-flash", Status: "retiring", Expires: "2026-12-31"},
		{Source: "testdata/watchlist.yaml: gone", Model: "openai/gpt-retired", Status: "missing"},
		{Source: "testdata/registry/models.yaml:7", Model: "anthropic/claude-2", Status: "missing"},
	}
	if !reflect.DeepEqual(rep.Missing, wantMissing) {
		t.Errorf("missing =\n%+v\nwant\n%+v", rep.Missing, wantMissing)
	}

	// opus moved -66.7%; sonnet and flash match; auto has no upstream price.
	if len(rep.PriceMoves) != 1 || rep.PriceMoves[0].Tier != "opus" {
		t.Fatalf("price moves = %+v, want only opus", rep.PriceMoves)
	}
	if got := *rep.PriceMoves[0].InputMovePct; got > -66.6 || got < -66.7 {
		t.Errorf("opus input move = %v, want -66.7", got)
	}

	want := []staleDate{{File: "testdata/registry/pricing.yaml", AsOf: "2026-05", AgeDays: 165}}
	if !reflect.DeepEqual(rep.StaleDates, want) {
		t.Errorf("stale dates = %+v, want %+v", rep.StaleDates, want)
	}
	if rep.DriftCount != 5 {
		t.Errorf("drift count = %d, want 5", rep.DriftCount)
	}
}

func TestCheck_NoDriftReportsZero(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "registry", "pricing.yaml"),
		"# as of 2026-11-01\ntiers:\n  1:\n    id: sonnet\n    input_per_million: 3\n    output_per_million: 15\n")
	writeFile(t, filepath.Join(dir, "registry", "models.yaml"), "notes: internal/trust\n")
	writeFile(t, filepath.Join(dir, "watch.yaml"), "upstream:\n  sonnet: anthropic/claude-sonnet-4.6\n")

	opts := fixtureOptions()
	opts.registryDir = filepath.Join(dir, "registry")
	opts.watchlist = filepath.Join(dir, "watch.yaml")
	rep, err := check(opts, fixtureCatalog(t))
	if err != nil {
		t.Fatal(err)
	}
	if rep.DriftCount != 0 {
		t.Fatalf("drift count = %d, want 0: %+v", rep.DriftCount, rep)
	}
	md := rep.markdown()
	if strings.Count(md, "None.") != 3 {
		t.Errorf("a clean report should say None. three times:\n%s", md)
	}
}

// A typo in the watchlist must fail the run, not silently check nothing.
func TestCheck_UnknownTierIsAnError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "watch.yaml"), "upstream:\n  opsu: anthropic/claude-opus-4.6\n")
	opts := fixtureOptions()
	opts.watchlist = filepath.Join(dir, "watch.yaml")
	if _, err := check(opts, fixtureCatalog(t)); err == nil || !strings.Contains(err.Error(), `"opsu"`) {
		t.Fatalf("err = %v, want one naming the unknown tier", err)
	}
}

func TestStaleAsOf(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		today time.Time
		stale bool
		want  staleDate
	}{
		{"no date is drift", "tiers: {}", fixtureDay, true, staleDate{}},
		// A month-only date counts from its last day: May 31 + 90 days is Aug 29.
		{"month at the limit", "As of 2026-05", time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC), false, staleDate{}},
		{"month past the limit", "as of 2026-05", time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC), true, staleDate{AsOf: "2026-05", AgeDays: 91}},
		{"oldest date wins", "as of 2026-11-01, as of 2026-01-15", fixtureDay, true, staleDate{AsOf: "2026-01-15", AgeDays: 301}},
		{"recent day", "as of 2026-11-01", fixtureDay, false, staleDate{}},
	}
	for _, c := range cases {
		got, stale := staleAsOf([]byte(c.text), c.today, 90)
		if stale != c.stale || got != c.want {
			t.Errorf("%s: got (%+v, %v), want (%+v, %v)", c.name, got, stale, c.want, c.stale)
		}
	}
}

func TestExceeds(t *testing.T) {
	if exceeds(movePct(0, 0), 0, 0, 25) {
		t.Error("free and still free is not drift")
	}
	if !exceeds(movePct(0, 1), 0, 1, 25) {
		t.Error("free and now charged is drift")
	}
	if exceeds(movePct(4, 5), 4, 5, 25) {
		t.Error("+25% is at the limit, not past it")
	}
	if !exceeds(movePct(4, 2.9), 4, 2.9, 25) {
		t.Error("-27.5% is past the limit")
	}
}

// An empty catalog is an outage; reading it as "all retired" would file an issue.
func TestParseCatalog_RefusesEmpty(t *testing.T) {
	if _, err := parseCatalog(strings.NewReader(`{"data": []}`)); err == nil {
		t.Error("an empty catalog was accepted")
	}
	if _, err := parseCatalog(strings.NewReader(`<html>`)); err == nil {
		t.Error("a non-JSON body was accepted")
	}
}

func TestFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			http.Error(w, "nope", http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"a/b","pricing":{"prompt":"0","completion":"0"}}]}`)
	}))
	defer srv.Close()

	body, err := fetch(context.Background(), srv.URL+"/ok")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := parseCatalog(body)
	body.Close()
	if err != nil || len(cat) != 1 {
		t.Fatalf("catalog = %v, err = %v", cat, err)
	}
	if _, err := fetch(context.Background(), srv.URL+"/fail"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("err = %v, want the HTTP status", err)
	}
}

// What the workflow reads: the JSON count and the marker in the markdown.
func TestRun_WritesBothReports(t *testing.T) {
	dir := t.TempDir()
	md, js := filepath.Join(dir, "drift.md"), filepath.Join(dir, "drift.json")
	err := run(fixtureOptions(), filepath.Join("testdata", "models.json"), "2026-11-12", md, js)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(js)
	if err != nil {
		t.Fatal(err)
	}
	var rep report
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.DriftCount != 5 || rep.CheckedOn != "2026-11-12" {
		t.Errorf("json report = %+v", rep)
	}

	text, err := os.ReadFile(md)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		marker,
		"| `opus` | `anthropic/claude-opus-4.6` | $15 / $75 | $5 / $25 | -66.7% / -66.7% |",
		"retiring on 2026-12-31",
		"as of 2026-05, 165 days old",
	} {
		if !strings.Contains(string(text), want) {
			t.Errorf("markdown lacks %q:\n%s", want, text)
		}
	}
}
