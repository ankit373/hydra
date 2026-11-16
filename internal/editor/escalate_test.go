// SPDX-License-Identifier: MIT

//go:build !windows

// The validator is a shell script, like corpus_test.go's beside it.

package editor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/cost"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/trust"
)

const escalateOriginal = "package main\n"

// escalateSandbox is cody (tier 6, which MODERATE pins) answering cody's reply
// and claude (tier 1, outside the pin) answering claude's, a validator that
// passes only a file holding "good", and each head's in-class record in go.
func escalateSandbox(t *testing.T, cody, claude string, codyCorrect, claudeCorrect int) string {
	t.Helper()
	repo := editSandbox(t, marked(escalateOriginal, cody))
	bin := os.Getenv("PATH")
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(testutil.EchoScript(marked(escalateOriginal, claude))), 0o700); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceYAML(t, repo, validatorCheckingContent(t, "good"))
	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	for head, correct := range map[string]int{"cody": codyCorrect, "claude": claudeCorrect} {
		for i := range 40 {
			if err := cal.Update(head, "go", true, trust.OutcomeFor(i < correct)); err != nil {
				t.Fatal(err)
			}
		}
	}
	file := filepath.Join(repo, "a.go")
	if err := os.WriteFile(file, []byte(escalateOriginal), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

func escalateEdit(t *testing.T, file string) *Result {
	t.Helper()
	res, err := Edit(context.Background(), Request{
		File: file, Enum: "MODERATE", Prompt: "x", Validate: true, RunID: "run-esc", TaskID: "task-esc",
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// answeredBy is every head that answered in run-esc, in order, and the escalation scores.
func answeredBy(t *testing.T) (heads []string, scores []runlog.Event) {
	t.Helper()
	events, err := runlog.Load("run-esc")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		switch {
		case e.Kind == runlog.KindDispatchFinished && e.TaskID == "task-esc":
			heads = append(heads, e.Head)
		case e.Kind == runlog.KindScore && e.Score != nil && e.Score.Source == "escalation":
			scores = append(scores, e)
		}
	}
	return heads, scores
}

func TestEdit_FailedValidatorRerunsOnStrongerHead(t *testing.T) {
	file := escalateSandbox(t, "bad", "good", 30, 39)
	res := escalateEdit(t, file)
	if res.Status != "ok" || res.Head != "claude" || !res.Escalated || res.FirstHead != "cody" {
		t.Fatalf("result %+v; want claude's answer kept after cody's failed", res)
	}
	if raw, _ := os.ReadFile(file); !strings.Contains(string(raw), "good") {
		t.Errorf("file holds %q, want the rerun's answer", raw)
	}
	heads, scores := answeredBy(t)
	if strings.Join(heads, ",") != "cody,claude" || len(scores) != 1 || scores[0].Head != "cody" ||
		!strings.Contains(scores[0].Score.Comment, validatorSource+" failed") {
		t.Errorf("run log answered by %v with escalation scores %+v; want cody then claude, cody scored once", heads, scores)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"escalated":true`) || !strings.Contains(string(raw), `"first_head":"cody"`) {
		t.Errorf("--json %s does not show the rerun", raw)
	}
}

func TestEdit_SecondFailureRollsBackAndStops(t *testing.T) {
	file := escalateSandbox(t, "bad", "worse", 30, 39)
	res := escalateEdit(t, file)
	if res.Status != "fail" || !res.RolledBack || res.Head != "claude" || !res.Escalated {
		t.Fatalf("result %+v; want claude's failure, rolled back", res)
	}
	if raw, _ := os.ReadFile(file); string(raw) != escalateOriginal {
		t.Errorf("file holds %q after two failures, want the original", raw)
	}
	if heads, _ := answeredBy(t); len(heads) != 2 {
		t.Errorf("answered by %v; want exactly one rerun", heads)
	}
	if _, err := os.Stat(file + ".hydra-bak"); err == nil {
		t.Error("the backup survived the failed edit")
	}
}

func TestEdit_EscalateOnFailFalseDoesNotRerun(t *testing.T) {
	file := escalateSandbox(t, "bad", "good", 30, 39)
	writePolicyApply(t, "      escalate_on_fail: false\n")
	res := escalateEdit(t, file)
	if res.Status != "fail" || res.Head != "cody" || res.Escalated {
		t.Fatalf("result %+v; want cody's failure and no rerun", res)
	}
	if heads, _ := answeredBy(t); len(heads) != 1 {
		t.Errorf("answered by %v; escalate_on_fail: false must not rerun", heads)
	}
}

func TestEdit_NoMeasuredHeadBeatsItKeepsFailure(t *testing.T) {
	file := escalateSandbox(t, "bad", "good", 39, 30)
	res := escalateEdit(t, file)
	if res.Status != "fail" || res.Head != "cody" || res.Escalated ||
		!strings.HasSuffix(res.Error, "; no stronger measured head") {
		t.Fatalf("result %+v; want cody's failure, saying no head beats it", res)
	}
}

// The rerun gets what the first answer left of the ceiling, not the ceiling again.
func TestEdit_SecondDispatchSpendsWithinWhatIsLeft(t *testing.T) {
	file := escalateSandbox(t, "bad", "good", 30, 39)
	// Room for cody's answer; claude's estimate is over the ceiling either way.
	const ceiling = 0.003
	writePolicyApply(t, fmt.Sprintf("      max_cost_usd: %g\n", ceiling))
	res := escalateEdit(t, file)
	rows, err := cost.LoadAll()
	if err != nil || len(rows) != 1 || rows[0].Head != "cody" {
		t.Fatalf("cost rows %+v (%v); want cody's answer alone", rows, err)
	}
	left := fmt.Sprintf("exceeds limit $%.4f", ceiling-rows[0].EstCostUSD)
	if res.Status != "fail" || res.Escalated || !strings.Contains(res.Error, "rerun on claude failed") ||
		!strings.Contains(res.Error, left) {
		t.Fatalf("error %q; want claude refused against %q", res.Error, left)
	}
}
