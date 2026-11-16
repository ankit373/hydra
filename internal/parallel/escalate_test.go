// SPDX-License-Identifier: MIT

//go:build !windows

// The validator is a shell script.

package parallel

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/trust"
)

// escalateBatch is cody (which MODERATE pins) answering "bad" and claude
// (measured stronger in go) answering "good", with a validator that passes
// b.go always and any other file only when it holds "good".
func escalateBatch(t *testing.T) string {
	t.Helper()
	repo := editSandbox(t, marked("package main // bad"))
	bin := os.Getenv("PATH")
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(testutil.EchoScript(marked("package main // good"))), 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "validate.sh")
	body := "#!/bin/sh\ncase \"$1\" in *b.go) exit 0;; esac\n" +
		"while IFS= read -r line; do case \"$line\" in *good*) exit 0;; esac; done < \"$1\"\n" +
		"echo \"$1: wants good\"\nexit 1\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceYAML(t, repo, script+" {file}")
	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	for head, correct := range map[string]int{"cody": 30, "claude": 39} {
		for i := range 40 {
			if err := cal.Update(head, "go", true, trust.OutcomeFor(i < correct)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return repo
}

// answers is the head of every answer in run, in order.
func answers(t *testing.T, run string) []string {
	t.Helper()
	events, err := runlog.Load(run)
	if err != nil {
		t.Fatal(err)
	}
	var heads []string
	for _, e := range events {
		if e.Kind == runlog.KindDispatchFinished {
			heads = append(heads, e.Head)
		}
	}
	return heads
}

func TestParallel_EditTaskRerunsOnceOnFailedValidator(t *testing.T) {
	repo := escalateBatch(t)
	a, b := filepath.Join(repo, "a.go"), filepath.Join(repo, "b.go")
	for _, f := range []string{a, b} {
		if err := os.WriteFile(f, []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	results, err := Run(context.Background(), []Task{
		{Label: "a", Enum: "MODERATE", File: a, Prompt: "x"},
		{Label: "b", Enum: "MODERATE", File: b, Prompt: "x"},
	}, Options{RunID: "run-par"})
	if err != nil {
		t.Fatal(err)
	}
	var got [2]EditResult
	for i, r := range results {
		if err := json.Unmarshal(r.raw, &got[i]); err != nil {
			t.Fatal(err)
		}
	}
	if got[0].Status != "ok" || got[0].Head != "claude" || !got[0].Escalated || got[0].FirstHead != "cody" {
		t.Errorf("task a = %+v; want claude's rerun kept after cody's failed", got[0])
	}
	if got[1].Status != "ok" || got[1].Head != "cody" || got[1].Escalated {
		t.Errorf("task b = %+v; want cody's answer, no rerun", got[1])
	}
	// Each task kept its own file through the rerun.
	for f, want := range map[string]string{a: "good", b: "bad"} {
		if raw, _ := os.ReadFile(f); !strings.Contains(string(raw), want) {
			t.Errorf("%s holds %q, want %q", filepath.Base(f), raw, want)
		}
	}
	// The rollback took a.go's backup; review still needs it as the baseline.
	if _, err := os.Stat(a + ".hydra-bak"); err != nil {
		t.Errorf("a.go's backup is gone after the rerun: %v", err)
	}
	if heads := answers(t, "run-par"); len(heads) != 3 || strings.Count(strings.Join(heads, ","), "claude") != 1 {
		t.Errorf("answered by %v; want cody twice and one claude rerun", heads)
	}
}

// A text task has no check, so nothing ever fails it into a rerun.
func TestParallel_TextTaskNeverReruns(t *testing.T) {
	escalateBatch(t)
	results, err := Run(context.Background(), []Task{{Label: "t", Enum: "MODERATE", Prompt: "x"}}, Options{RunID: "run-text"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(results[0].raw), `"status":"ok"`) || strings.Contains(string(results[0].raw), "escalated") {
		t.Errorf("text result %s", results[0].raw)
	}
	if heads := answers(t, "run-text"); len(heads) != 1 || heads[0] != "cody" {
		t.Errorf("answered by %v; want cody once", heads)
	}
}
