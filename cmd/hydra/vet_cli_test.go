// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/testutil"
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
