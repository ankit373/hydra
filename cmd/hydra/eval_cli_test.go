// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/rollup"
	"github.com/ankit373/hydra/internal/sketch"
)

// seedExamples writes a small corpus through the real Add, so the test sees
// whatever normalization and dedup Add applies rather than a hand-built file.
func seedExamples(t *testing.T) {
	t.Helper()
	for _, e := range []evalset.Example{
		{Domain: "go", Source: "oracle:test", Head: "agy:sonnet", Candidate: "package a", Passed: true},
		{Domain: "go", Source: "oracle:test", Head: "agy:sonnet", Candidate: "package b", Passed: false, Detail: "build failed"},
		{Domain: "sql", Source: "oracle:lint", Head: "ollama:qwen", Candidate: "select 1", Passed: true},
	} {
		if _, err := evalset.Add(evalset.DefaultPath(), e); err != nil {
			t.Fatal(err)
		}
	}
}

// An empty corpus must say how to fill it. "No output" reads as a broken
// command; the whole point of the corpus is that people know to grow it.
func TestCLI_EvalOnAnEmptyCorpusSaysHowToRecordOne(t *testing.T) {
	cliSandbox(t)

	out, _, err := run(t, "eval", "list")
	if err != nil {
		t.Fatalf("eval list on an empty corpus errored: %v", err)
	}
	if !strings.Contains(out, "oracle verify") {
		t.Errorf("empty listing does not name the command that records an example:\n%s", out)
	}

	out, _, err = run(t, "eval", "stats")
	if err != nil {
		t.Fatalf("eval stats on an empty corpus errored: %v", err)
	}
	if !strings.Contains(out, "No verified examples") {
		t.Errorf("empty stats output = %q", out)
	}
}

// The readiness view exists to answer "can I fit routing from this yet". On an
// empty corpus that answer needs the command that starts filling it, including
// the attribution flag, or the corpus grows unusable for the one thing it is for.
func TestCLI_EvalReadinessOnAnEmptyCorpusSaysHowToAttributeOne(t *testing.T) {
	cliSandbox(t)

	out, _, err := run(t, "eval", "readiness")
	if err != nil {
		t.Fatalf("eval readiness on an empty corpus errored: %v", err)
	}
	if !strings.Contains(out, "--enum") {
		t.Errorf("empty readiness does not name the attribution flag:\n%s", out)
	}
}

// The distinction the floor turns on: volume on one head is not comparability.
// Reporting the lopsided enum as ready would recommend fitting on a sample
// measured to lose to the strongest head outright.
func TestCLI_EvalReadinessDistinguishesVolumeFromComparability(t *testing.T) {
	cliSandbox(t)
	seed := func(enum, head string, n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			_, err := evalset.Add(evalset.DefaultPath(), evalset.Example{
				Domain: "go", Source: "oracle:test", Enum: enum, Tier: 8, Head: head,
				Candidate: fmt.Sprintf("package %s%s%d", enum, head, i), Passed: i%2 == 0,
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	seed("LOPSIDED", "head-a", evalset.MinObservationsPerHead*2)
	seed("SPREAD", "head-a", evalset.MinObservationsPerHead)
	seed("SPREAD", "head-b", evalset.MinObservationsPerHead)

	out, _, err := run(t, "eval", "readiness", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got []evalset.EnumStat
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("readiness --json is not valid JSON: %v\n%s", err, out)
	}
	byEnum := map[string]evalset.EnumStat{}
	for _, s := range got {
		byEnum[s.Enum] = s
	}
	if s := byEnum["LOPSIDED"]; s.Ready {
		t.Errorf("2x the floor on one head read as ready: %+v", s)
	}
	if s := byEnum["SPREAD"]; !s.Ready {
		t.Errorf("the floor on two heads did not read as ready: %+v", s)
	}
}

// An oracle verdict that does not say which routing decision it judges cannot
// improve routing, which is the only reason the corpus is kept forever.
func TestCLI_OracleVerifyRecordsTheRoutingDecision(t *testing.T) {
	s := cliSandbox(t)
	f := filepath.Join(t.TempDir(), "candidate.go")
	if err := os.WriteFile(f, []byte("package a\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// FakeBinary's default body exits 0 on either platform, which is all a
	// passing verifier has to do. A POSIX shell path would skip on Windows,
	// and this contract is not platform-specific.
	if _, _, err := run(t, "oracle", "verify", "--candidate", f,
		"--domain", "go", "--enum", "SIMPLE", "--tier", "8",
		"--", s.FakeBinary(t, "always-pass")); err != nil {
		t.Fatalf("verify: %v", err)
	}

	got, err := evalset.Load(evalset.DefaultPath())
	if err != nil || len(got) != 1 {
		t.Fatalf("got %d examples, err=%v", len(got), err)
	}
	if got[0].Enum != "SIMPLE" || got[0].Tier != 8 {
		t.Errorf("routing decision not recorded: enum=%q tier=%d", got[0].Enum, got[0].Tier)
	}
}

func TestCLI_EvalListShowsEveryExampleAndItsVerdict(t *testing.T) {
	cliSandbox(t)
	seedExamples(t)

	out, _, err := run(t, "eval", "list")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out, "PASS"); n != 2 {
		t.Errorf("got %d PASS rows, want 2:\n%s", n, out)
	}
	if n := strings.Count(out, "FAIL"); n != 1 {
		t.Errorf("got %d FAIL rows, want 1:\n%s", n, out)
	}
	for _, domain := range []string{"go", "sql"} {
		if !strings.Contains(out, domain) {
			t.Errorf("listing omits domain %q:\n%s", domain, out)
		}
	}
}

// --failed is how someone finds what the router got wrong, which is the only
// reason to keep failures at all.
func TestCLI_EvalListFailedShowsOnlyRejectedExamples(t *testing.T) {
	cliSandbox(t)
	seedExamples(t)

	out, _, err := run(t, "eval", "list", "--failed")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "PASS") {
		t.Errorf("--failed listed a passing example:\n%s", out)
	}
	if !strings.Contains(out, "FAIL") {
		t.Errorf("--failed listed nothing:\n%s", out)
	}
}

func TestCLI_EvalListJSONIsParseableAndNewestFirst(t *testing.T) {
	cliSandbox(t)
	seedExamples(t)

	out, _, err := run(t, "eval", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got []evalset.Example
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("--json did not emit parseable JSON: %v\n%s", err, out)
	}
	if len(got) != 3 {
		t.Fatalf("got %d examples, want 3", len(got))
	}
	if got[0].Candidate != "select 1" {
		t.Errorf("first example is %q, want the newest (%q)", got[0].Candidate, "select 1")
	}
}

func TestCLI_EvalListLimitCaps(t *testing.T) {
	cliSandbox(t)
	seedExamples(t)

	out, _, err := run(t, "eval", "list", "--limit", "1", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got []evalset.Example
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("--limit 1 returned %d examples", len(got))
	}
}

func TestCLI_EvalStatsReportsPassRatePerDomain(t *testing.T) {
	cliSandbox(t)
	seedExamples(t)

	out, _, err := run(t, "eval", "stats")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "PASS RATE") {
		t.Errorf("stats has no pass-rate column:\n%s", out)
	}
	// go: 1 of 2 passed.
	if !strings.Contains(out, "50.0%") {
		t.Errorf("stats does not report the go domain's 50%% pass rate:\n%s", out)
	}
	if !strings.Contains(out, "never pruned") {
		t.Errorf("stats does not state the corpus is exempt from retention:\n%s", out)
	}
}

func TestCLI_EvalStatsJSONIsParseable(t *testing.T) {
	cliSandbox(t)
	seedExamples(t)

	out, _, err := run(t, "eval", "stats", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got []evalset.DomainStat
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("--json did not emit parseable JSON: %v\n%s", err, out)
	}
	if len(got) != 2 {
		t.Fatalf("got %d domains, want 2 (go, sql)", len(got))
	}
}

// --- latency rendering -----------------------------------------------------

func latencyRollup(t *testing.T, model string, tier int, calls int64, vals ...float64) rollup.Row {
	t.Helper()
	s := sketch.New(sketch.DefaultAlpha)
	for _, v := range vals {
		s.Add(v)
	}
	return rollup.Row{
		Key:     rollup.Key{Model: model, Tier: tier},
		Calls:   calls,
		Latency: s,
	}
}

// The whole reason latency lives in a sketch is that per-day rows merge into an
// all-time percentile. If mergeByModel dropped a day, the p99 would silently be
// computed from a fraction of the data.
func TestMergeByModel_FoldsEveryDayIntoOneRow(t *testing.T) {
	// Two days of one model: a fast day and a slow one. Split evenly so the
	// merged p99 must sit in the slow day, a merge that dropped it would read
	// as ~100ms. (Percentiles need a real sample; with three points p99 is not
	// a meaningful question to ask of any sketch.)
	fast := make([]float64, 100)
	for i := range fast {
		fast[i] = 100
	}
	slow := make([]float64, 100)
	for i := range slow {
		slow[i] = 5000
	}
	rows := []rollup.Row{
		latencyRollup(t, "claude-sonnet-5", 2, 100, fast...),
		latencyRollup(t, "claude-sonnet-5", 2, 100, slow...),
		latencyRollup(t, "qwen3", 10, 3, 50, 60, 70),
	}
	got := mergeByModel(rows)
	if len(got) != 2 {
		t.Fatalf("got %d merged rows, want 2 (one per model/tier)", len(got))
	}
	// Sorted by calls descending, so sonnet leads with 200.
	if got[0].Model != "claude-sonnet-5" || got[0].Calls != 200 {
		t.Fatalf("first row = %+v, want claude-sonnet-5 with 200 calls", got[0])
	}
	if got[0].P99Est < 4000 {
		t.Errorf("p99 = %.0f, want the merged tail near 5000, the slow day was dropped", got[0].P99Est)
	}
	if got[0].P50Est > 200 {
		t.Errorf("p50 = %.0f, want the fast day near 100, the merge lost the small end", got[0].P50Est)
	}
	if got[0].RelErr <= 0 {
		t.Error("merged row reports no relative-error bound, so the estimate reads as exact")
	}
}

// A row whose sketch is nil (a rollup written before latency was recorded)
// must still count its calls rather than panic or vanish.
func TestMergeByModel_ToleratesRowsWithNoSketch(t *testing.T) {
	rows := []rollup.Row{
		{Key: rollup.Key{Model: "old", Tier: 3}, Calls: 7},
		{Key: rollup.Key{Model: "old", Tier: 3}, Calls: 3},
	}
	got := mergeByModel(rows)
	if len(got) != 1 || got[0].Calls != 10 {
		t.Fatalf("got %+v, want one row with 10 calls", got)
	}
	if got[0].P99Est != 0 {
		t.Errorf("p99 = %.0f, want 0 when nothing recorded a latency", got[0].P99Est)
	}
}

func TestRenderLatency_EmptyAndPopulated(t *testing.T) {
	out := captureStdout(t, func() { renderLatency(nil) })
	if !strings.Contains(out, "No dispatches") {
		t.Errorf("empty render = %q", out)
	}

	rows := []rollup.Row{latencyRollup(t, "claude-sonnet-5", 2, 4, 100, 200, 300, 400)}
	out = captureStdout(t, func() { renderLatency(rows) })
	for _, want := range []string{"MODEL", "P99 ms", "claude-sonnet-5", "relative"} {
		if !strings.Contains(out, want) {
			t.Errorf("latency table missing %q:\n%s", want, out)
		}
	}
}

func TestRollupLatencyJSON_MatchesTheRenderedMerge(t *testing.T) {
	rows := []rollup.Row{
		latencyRollup(t, "a", 1, 2, 10, 20),
		latencyRollup(t, "a", 1, 3, 30),
	}
	got := rollupLatencyJSON(rows)
	if len(got) != 1 || got[0].Calls != 5 {
		t.Fatalf("got %+v, want one row of 5 calls", got)
	}
}

// A model id is truncated to fit the column; both ends carry the identifying
// parts, so a truncation that keeps only the prefix makes two models look alike.
func TestTruncateMiddle(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 28, "short"},
		{"exactlyten", 10, "exactlyten"},
		{"anthropic/claude-sonnet-5-20260101", 20, "anthropic\u20265-20260101"},
		{"abcdefgh", 4, "abcd"},
	}
	for _, c := range cases {
		if got := truncateMiddle(c.in, c.n); got != c.want {
			t.Errorf("truncateMiddle(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
	long := truncateMiddle("anthropic/claude-sonnet-5-20260101", 20)
	if len([]rune(long)) != 20 {
		t.Errorf("truncated to %d runes, want exactly 20", len([]rune(long)))
	}
}
