// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/graph"
	"github.com/ankit373/hydra/internal/swarm"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/internal/vet"
)

func blockingResult() *vet.Result {
	return &vet.Result{
		Spec:     &vet.Spec{Mode: "workspace"},
		Findings: []vet.Finding{{File: "a.go", Line: 2, Severity: vet.Blocking, Title: "boom", Head: "h"}},
		Files:    []vet.FileOutcome{{File: "a.go", Head: "h", Tier: 10, Findings: 1}},
	}
}

// The exit code must not depend on which rendering ran. --json returned before
// the check, so a gate reading JSON passed on a blocking finding.
func TestRenderVet_BothRenderingsGateTheSame(t *testing.T) {
	for _, jsonOut := range []bool{false, true} {
		var buf bytes.Buffer
		code, err := renderVet(&buf, blockingResult(), jsonOut)
		if err != nil {
			t.Fatalf("json=%v: %v", jsonOut, err)
		}
		if code != 3 {
			t.Errorf("json=%v exited %d, want 3: a script gating on blocking findings would not fire", jsonOut, code)
		}
		if buf.Len() == 0 {
			t.Errorf("json=%v rendered nothing", jsonOut)
		}
	}
}

func TestRenderVet_CleanRunExitsZero(t *testing.T) {
	clean := &vet.Result{
		Spec:  &vet.Spec{Mode: "workspace"},
		Files: []vet.FileOutcome{{File: "a.go", Head: "h", Tier: 10}},
	}
	for _, jsonOut := range []bool{false, true} {
		var buf bytes.Buffer
		if code, _ := renderVet(&buf, clean, jsonOut); code != 0 {
			t.Errorf("json=%v exited %d on a clean review, want 0", jsonOut, code)
		}
	}
}

// --json is what a machine reads, so it has to carry the findings themselves.
func TestRenderVet_JSONCarriesTheFindings(t *testing.T) {
	var buf bytes.Buffer
	if _, err := renderVet(&buf, blockingResult(), true); err != nil {
		t.Fatal(err)
	}
	var got vet.Result
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("--json did not emit valid JSON: %v", err)
	}
	if len(got.Findings) != 1 || got.Findings[0].Title != "boom" {
		t.Fatalf("findings did not survive the round trip: %+v", got.Findings)
	}
}

// A pinned --tier must not record an enum it did not route on (#832), and a
// garbage enum must not fall through to unrestricted auto-routing (#501).
func TestResolveVetRouting_RecordsOnlyWhatActuallyRouted(t *testing.T) {
	hint, logEnum, err := resolveVetRouting("HARD", "8")
	if err != nil {
		t.Fatal(err)
	}
	if hint != "8" {
		t.Errorf("hint = %q, want the pinned tier", hint)
	}
	if logEnum != "" {
		t.Errorf("logEnum = %q: the cost row would name a routing key that did not route", logEnum)
	}

	if hint, logEnum, err = resolveVetRouting("HARD", ""); err != nil || logEnum != "HARD" || hint == "" {
		t.Errorf("enum routing = %q,%q,%v", hint, logEnum, err)
	}
	if _, _, err = resolveVetRouting("NOT_AN_ENUM", ""); err == nil {
		t.Error("a garbage enum resolved; it would fall through to unrestricted auto-routing")
	}
}

// An unreadable reply is not a clean file, and the table is where that is
// easiest to hide.
func TestPrintVet_UnreadableReplyIsNotRenderedAsClean(t *testing.T) {
	var buf bytes.Buffer
	printVet(&buf, &vet.Result{
		Spec:  &vet.Spec{Mode: "workspace"},
		Files: []vet.FileOutcome{{File: "a.go", Head: "h", Unparsed: true, Raw: "nope"}},
	})
	out := buf.String()
	if strings.Contains(out, "no defects reported") {
		t.Fatalf("an unparsed reply rendered as a clean file:\n%s", out)
	}
	if !strings.Contains(out, "not readable as findings") {
		t.Fatalf("the unreadable reply was not reported at all:\n%s", out)
	}
}

// A short review has to be visibly a filtered one rather than a small diff.
func TestPrintVet_SkippedFilesAreAccountedFor(t *testing.T) {
	var buf bytes.Buffer
	printVet(&buf, &vet.Result{
		Spec: &vet.Spec{Mode: "workspace", Excluded: []vet.File{
			{Path: "a.md", Reason: "unsupported_ext"},
			{Path: "b_test.go", Reason: "default_path"},
			{Path: "c_test.go", Reason: "default_path"},
		}},
		Files: []vet.FileOutcome{{File: "a.go", Head: "h"}},
	})
	out := buf.String()
	for _, want := range []string{"3 skipped", "2 default_path", "1 unsupported_ext"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary is missing %q:\n%s", want, out)
		}
	}
}

// The renderer must survive a result with no spec rather than taking the
// process down while reporting findings it already has.
func TestPrintVet_NilSpecDoesNotPanic(t *testing.T) {
	var buf bytes.Buffer
	printVet(&buf, &vet.Result{Files: []vet.FileOutcome{{File: "a.go"}}})
	if buf.Len() == 0 {
		t.Fatal("rendered nothing")
	}
}

func TestPrintVetPlan_NoReviewableFilesSaysSo(t *testing.T) {
	var buf bytes.Buffer
	if err := printVetPlan(&buf, &vet.Spec{Mode: "range", From: "develop", To: "HEAD",
		MergeBase: "abcdef1234567890"}, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "no reviewable files") {
		t.Errorf("an empty plan did not say so:\n%s", out)
	}
	if !strings.Contains(out, "abcdef12") {
		t.Errorf("the plan did not name the diff it read:\n%s", out)
	}
}

func TestWrap_KeepsEveryWord(t *testing.T) {
	in := "the deferred Close overwrites the primary error so a failed write reports success"
	lines := wrap(in, 20)
	if len(lines) < 2 {
		t.Fatalf("no wrapping happened: %v", lines)
	}
	if got := strings.Join(lines, " "); got != in {
		t.Fatalf("words were lost or reordered:\n got %q\nwant %q", got, in)
	}
	for _, l := range lines {
		if len(l) > 20 && !strings.Contains(l, " ") {
			continue // a single word longer than the width cannot be broken
		}
		if len(l) > 20 {
			t.Errorf("line over width: %q", l)
		}
	}
}

// The refusal has to name the tool and the install. This is the #866 shape:
// a command whose dependency is missing must say so, not fail obscurely.
func TestPrintNoRuleSource_NamesTheToolAndTheInstall(t *testing.T) {
	var buf bytes.Buffer
	printNoRuleSource(&buf)
	out := buf.String()
	for _, want := range []string{"open-code-review", "npm install -g @alibaba-group/open-code-review", "no spend"} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal is missing %q:\n%s", want, out)
		}
	}
}

// --dry-run is the answer to "what would this cost me", so it has to name every
// file and the rule that would govern it.
func TestPrintVetPlan_ListsTheFilesAndTheirRules(t *testing.T) {
	spec := &vet.Spec{
		Mode: "commit", Commit: "2504ec2f00",
		Reviewable: []vet.File{{Path: "internal/a.go", Insertions: 12, Deletions: 3}},
		Excluded:   []vet.File{{Path: "b_test.go", Reason: "default_path"}},
		Groups:     []vet.Group{{Pattern: "**/*.go", Files: []string{"internal/a.go"}}},
	}
	var buf bytes.Buffer
	if err := printVetPlan(&buf, spec, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"internal/a.go", "+12/-3", "**/*.go", "commit 2504ec2", "1 skipped"} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan is missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "nothing dispatched") {
		t.Errorf("the plan does not say it spent nothing:\n%s", out)
	}
}

// A file the rules do not cover must be visible in the plan, or it looks
// reviewed when the run comes back saying nothing about it.
func TestPrintVetPlan_AFileWithNoRuleSaysSo(t *testing.T) {
	var buf bytes.Buffer
	if err := printVetPlan(&buf, &vet.Spec{
		Mode:       "workspace",
		Reviewable: []vet.File{{Path: "orphan.go"}},
	}, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "no rule") {
		t.Errorf("an unruled file did not say so:\n%s", buf.String())
	}
}

func TestPrintVetPlan_JSONIsTheSpec(t *testing.T) {
	var buf bytes.Buffer
	if err := printVetPlan(&buf, &vet.Spec{Mode: "workspace",
		Reviewable: []vet.File{{Path: "a.go"}}}, true); err != nil {
		t.Fatal(err)
	}
	var got vet.Spec
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("--dry-run --json did not emit valid JSON: %v", err)
	}
	if got.Mode != "workspace" || len(got.Reviewable) != 1 {
		t.Fatalf("the spec did not survive the round trip: %+v", got)
	}
}

// A report must never be ambiguous about which diff it read.
func TestScopeLine_NamesWhatWasRead(t *testing.T) {
	cases := []struct {
		spec *vet.Spec
		want string
	}{
		{nil, "unknown scope"},
		{&vet.Spec{Mode: "workspace"}, "workspace"},
		{&vet.Spec{Mode: "commit", Commit: "abcdef1234"}, "commit abcdef12"},
		{&vet.Spec{Mode: "range", MergeBase: "1234567890", To: "feature"}, "range 12345678..feature"},
		// No merge base recorded: From is the only ref left to name.
		{&vet.Spec{Mode: "range", From: "develop"}, "range develop..HEAD"},
	}
	for _, c := range cases {
		if got := scopeLine(c.spec); got != c.want {
			t.Errorf("scopeLine = %q, want %q", got, c.want)
		}
	}
}

func TestSeverityLabel_CarriesEitherWord(t *testing.T) {
	if !strings.Contains(severityLabel(vet.Blocking), "blocking") {
		t.Error("the blocking label lost its word")
	}
	if !strings.Contains(severityLabel(vet.NonBlocking), "non-blocking") {
		t.Error("the non-blocking label lost its word")
	}
}

// A garbage enum must be refused before anything is resolved or dispatched, or
// it falls through to unrestricted auto-routing (#501).
func TestCmdVet_GarbageEnumIsRefusedBeforeAnythingRuns(t *testing.T) {
	testutil.NewSandbox(t)
	_, _, err := run(t, "vet", "--enum", "NOT_AN_ENUM")
	if err == nil {
		t.Fatal("a garbage enum was accepted")
	}
	if !strings.Contains(err.Error(), "NOT_AN_ENUM") {
		t.Errorf("the refusal does not name the enum: %v", err)
	}
}

// The adapter is the only place internal/vet and the router meet, so what it
// drops is invisible everywhere else: a finding with no head cannot be
// attributed, and a cost it forgets never reaches the run's total.
func TestVetRouter_CarriesTheHeadTierAndCostBack(t *testing.T) {
	dispatchable(t, `[{"line":1,"severity":"blocking","title":"x"}]`)

	ctx := context.Background()
	d, err := dispatch.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	ans, err := vetRouter{
		d: d, runID: "run-1", enum: "MODERATE", tier: dispatch.EnumToTier("MODERATE"),
	}.Review(ctx, "review this diff", "go", "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ans.Output, "blocking") {
		t.Errorf("the head's answer did not survive the adapter: %q", ans.Output)
	}
	if ans.Head == "" {
		t.Error("no head recorded, so every finding would be unattributable")
	}
	if ans.Tier <= 0 {
		t.Errorf("tier = %d, so the report cannot say what answered", ans.Tier)
	}
}

// ── the per-file confidence bar ───────────────────────────────────────────────

// A radius Hydra did not read off a graph is a default. Reporting it as a
// measurement makes a run with no graph read like blast-radius-aware routing
// when nothing measured anything (#251).
func TestBarFor_AnUnmeasuredRadiusSaysSo(t *testing.T) {
	r := vetRouter{floor: 0.5} // no graph loaded at all
	bar := r.barFor("go", "internal/auth/token.go")
	if bar.Measured {
		t.Error("a radius with no graph behind it claimed to be measured")
	}
	if bar.Radius != 1.0 {
		t.Errorf("radius = %v, want the 1.0 default", bar.Radius)
	}
	if bar.Target < 0.5 {
		t.Errorf("target %v fell below the caller's floor", bar.Target)
	}
}

// The floor is a floor: the blast radius raises the bar above it and never
// lowers it, or a risky file would be held to a laxer standard than asked for.
func TestBarFor_TheBlastRadiusOnlyRaisesTheFloor(t *testing.T) {
	low := vetRouter{floor: 0.01}.barFor("go", "a.go")
	if low.Target <= 0.01 {
		t.Errorf("target %v: the defect model never raised the bar", low.Target)
	}
	high := vetRouter{floor: 0.99}.barFor("go", "a.go")
	if high.Target < 0.99 {
		t.Errorf("target %v dropped below the caller's floor of 0.99", high.Target)
	}
}

// The refusal has to name a head the router would actually sample, or the
// command it prints records under a key nothing reads and earns the same
// refusal again (#835).
func TestNoEvidenceAdvice_NamesAHeadTheRouterWouldSample(t *testing.T) {
	got := noEvidenceAdvice(&trust.NoEvidenceError{
		Domain:  "go",
		Sources: []string{"ollama/qwen3:8b", "agy"},
	})
	for _, want := range []string{"ollama/qwen3:8b", "--domain go", "hyctl trust record", "drop --confidence"} {
		if !strings.Contains(got, want) {
			t.Errorf("the advice is missing %q:\n%s", want, got)
		}
	}
}

// With no head to name it must still be actionable rather than printing a
// command with an empty source.
func TestNoEvidenceAdvice_SurvivesWithNoSources(t *testing.T) {
	got := noEvidenceAdvice(&trust.NoEvidenceError{Domain: "rust"})
	if strings.Contains(got, "--source --domain") || strings.Contains(got, "--source  ") {
		t.Errorf("the command has an empty source:\n%s", got)
	}
	if !strings.Contains(got, "rust") {
		t.Errorf("the advice does not name the domain:\n%s", got)
	}
}

func TestBarLine_SaysWhetherTheRadiusWasMeasured(t *testing.T) {
	unmeasured := barLine(vet.FileOutcome{Bar: vet.Bar{Target: 0.9, Radius: 1.0}, Confidence: 0.95, Samples: 2})
	if !strings.Contains(unmeasured, "default, not measured") {
		t.Errorf("an unmeasured radius did not say so: %s", unmeasured)
	}
	measured := barLine(vet.FileOutcome{
		Bar: vet.Bar{Target: 0.9, Radius: 12, Measured: true}, Confidence: 0.95, Samples: 3,
	})
	if strings.Contains(measured, "default") {
		t.Errorf("a measured radius was called a default: %s", measured)
	}
	if !strings.Contains(measured, "reached") || !strings.Contains(measured, "3 head") {
		t.Errorf("the line does not report the outcome: %s", measured)
	}
}

// A file that fell short must be visible as such in the report, not rendered
// identically to one that cleared.
func TestPrintVet_ShortOfBarIsVisible(t *testing.T) {
	var buf bytes.Buffer
	printVet(&buf, &vet.Result{
		Spec: &vet.Spec{Mode: "workspace"},
		Files: []vet.FileOutcome{{
			File: "a.go", Head: "h", Bar: vet.Bar{Target: 0.9, Radius: 1}, Confidence: 0.6, Samples: 4,
		}},
	})
	out := buf.String()
	if !strings.Contains(out, "short of") {
		t.Errorf("a file under its bar did not say so:\n%s", out)
	}
	if !strings.Contains(out, "their findings stand, the confidence does not") {
		t.Errorf("the summary does not separate the findings from the confidence:\n%s", out)
	}
}

// The run-stopping reason is printed once, in the summary, not blamed on each
// file in turn.
func TestPrintVet_ARunStoppingReasonIsSaidOnce(t *testing.T) {
	var buf bytes.Buffer
	printVet(&buf, &vet.Result{
		Spec: &vet.Spec{Mode: "workspace"},
		Files: []vet.FileOutcome{
			{File: "a.go", Err: "cannot review any file: nothing is calibrated", Fatal: true},
			{File: "b.go", Err: "cannot review any file: nothing is calibrated", Fatal: true},
		},
	})
	out := buf.String()
	if n := strings.Count(out, "nothing is calibrated"); n != 1 {
		t.Errorf("the reason appears %d times, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, "the run stopped") {
		t.Errorf("the summary does not say the run stopped:\n%s", out)
	}
}

// A real graph makes the radius a measurement, and a hub file is held to a
// higher bar than a leaf: that difference is the whole point of the flag.
func TestBarFor_AMeasuredRadiusRaisesTheBarForAHubFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "graph.json")
	// hub.go is depended on by three files; leaf.go by none.
	doc := `{"nodes":[
	  {"id":"hub","file":"hub.go"},{"id":"leaf","file":"leaf.go"},
	  {"id":"a","file":"a.go"},{"id":"b","file":"b.go"},{"id":"c","file":"c.go"}],
	 "edges":[{"from":"a","to":"hub"},{"from":"b","to":"hub"},{"from":"c","to":"hub"},
	  {"from":"b","to":"a"}]}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := graph.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	r := vetRouter{floor: 0.5, graph: g, graphPath: path}

	hub := r.barFor("go", "hub.go")
	if !hub.Measured {
		t.Fatal("a radius read off a real graph did not report as measured")
	}
	leaf := r.barFor("go", "leaf.go")
	if !(hub.Radius > leaf.Radius) {
		t.Fatalf("hub radius %v is not above leaf radius %v, so the graph changed nothing",
			hub.Radius, leaf.Radius)
	}
	if !(hub.Target >= leaf.Target) {
		t.Errorf("the hub file was held to a lower bar (%v) than the leaf (%v)", hub.Target, leaf.Target)
	}

	// A file the graph has never heard of is a default again, not a reading.
	unknown := r.barFor("go", "nowhere.go")
	if unknown.Measured {
		t.Error("a file absent from the graph reported a measured radius")
	}
}

// The warning has to come out once, before anything is spent, or a run with no
// graph reads exactly like blast-radius-aware routing (#251).
func TestPrintVetBar_SaysWhenEveryRadiusIsADefault(t *testing.T) {
	var buf bytes.Buffer
	printVetBar(&buf, nil, "graph.json", 0.7)
	out := buf.String()
	if !strings.Contains(out, "default rather than a measurement") {
		t.Errorf("a missing graph was not called out:\n%s", out)
	}
	if !strings.Contains(out, "70%") {
		t.Errorf("the floor was not stated:\n%s", out)
	}

	// With a real graph there is nothing to warn about.
	dir := t.TempDir()
	path := filepath.Join(dir, "graph.json")
	if err := os.WriteFile(path, []byte(`{"nodes":[{"id":"a","file":"a.go"}],"edges":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := graph.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	printVetBar(&buf, g, path, 0.7)
	if strings.Contains(buf.String(), "default rather than a measurement") {
		t.Errorf("a real graph still warned about defaults:\n%s", buf.String())
	}
}

// A fresh machine has no calibration, so the ensemble refuses. That refusal has
// to arrive as ErrCannotSample or Run pays it once per file and the report
// blames each file for a problem none of them has.
func TestReviewEnsemble_AnUncalibratedDomainStopsTheRun(t *testing.T) {
	dispatchable(t, "unused")

	ctx := context.Background()
	d, err := dispatch.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	r := vetRouter{d: d, sw: swarm.New(d, d.Heads(), d), floor: 0.7, graphPath: "graph.json"}
	_, err = r.reviewEnsemble(ctx, "review this", "go", "a.go")
	if err == nil {
		t.Fatal("an uncalibrated domain produced an answer")
	}
	if !errors.Is(err, vet.ErrCannotSample) {
		t.Fatalf("the refusal was not marked as stopping the run, so every file pays it: %v", err)
	}
	// It must stay actionable through the wrap, not become a bare sentinel.
	if !strings.Contains(err.Error(), "hyctl trust record") {
		t.Errorf("the advice was lost in the wrap: %v", err)
	}
}
