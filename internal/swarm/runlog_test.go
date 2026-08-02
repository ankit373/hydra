// SPDX-License-Identifier: MIT

package swarm

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/tree"
	"github.com/ankit373/hydra/internal/trust"
)

func rlSandbox(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HYDRA_RUN_ID", "")
	t.Setenv("HYDRA_TASK_ID", "")
}

func rlAttempt(id string, status HeadStatus, rank int) Attempt {
	return Attempt{
		Head:       registryHead(id, id, 80),
		Status:     status,
		Rank:       rank,
		EstCostUSD: 0.01,
		Duration:   250 * time.Millisecond,
		FinishedAt: time.Now(),
	}
}

func kindsOf(events []runlog.Event) map[runlog.Kind]int {
	out := map[runlog.Kind]int{}
	for _, e := range events {
		out[e.Kind]++
	}
	return out
}

// A five-head swarm must read as five heads. Before #204 this package emitted
// nothing, so the whole fan-out collapsed into whichever single node dispatch
// happened to log.
func TestLogRunEvents_OneAttemptPerHead(t *testing.T) {
	rlSandbox(t)

	attempts := []Attempt{
		rlAttempt("a", StatusOK, 1),
		rlAttempt("b", StatusOK, 2),
		rlAttempt("c", StatusFailed, 0),
	}
	logRunEvents(attempts, ModeBest, Options{RunID: "run-sw", TaskID: "task-sw"})

	events, err := runlog.Load("run-sw")
	if err != nil {
		t.Fatal(err)
	}
	if got := kindsOf(events)[runlog.KindAttempt]; got != 3 {
		t.Errorf("%d attempt events, want 3 (one per executed head)", got)
	}
	for _, e := range events {
		if e.Kind == runlog.KindAttempt && e.Parent != swarmAgent {
			t.Errorf("attempt %q has parent %q, want %q", e.Agent, e.Parent, swarmAgent)
		}
	}
}

// Pending and canceled heads never ran. Recording them as attempts would show
// work that did not happen.
func TestLogRunEvents_SkipsUnexecutedHeads(t *testing.T) {
	rlSandbox(t)

	logRunEvents([]Attempt{
		rlAttempt("ran", StatusOK, 1),
		rlAttempt("pending", StatusPending, 0),
		rlAttempt("canceled", StatusCanceled, 0),
	}, ModeRace, Options{RunID: "run-skip", TaskID: "t"})

	events, err := runlog.Load("run-skip")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind == runlog.KindAttempt && e.Agent != "ran" {
			t.Errorf("recorded an attempt for %q, which never executed", e.Agent)
		}
	}
}

// The winner is the one thing a reader cannot derive from the other fields.
func TestLogRunEvents_MarksWinner(t *testing.T) {
	rlSandbox(t)

	logRunEvents([]Attempt{
		rlAttempt("loser", StatusOK, 2),
		rlAttempt("winner", StatusOK, 1),
	}, ModeBest, Options{RunID: "run-win", TaskID: "t"})

	events, err := runlog.Load("run-win")
	if err != nil {
		t.Fatal(err)
	}
	var marked int
	for _, e := range events {
		if e.Kind != runlog.KindAttempt {
			continue
		}
		isWinner := e.Agent == "winner"
		hasMark := e.Detail == string(ModeBest)+" · winner"
		if isWinner != hasMark {
			t.Errorf("%q: winner=%v but detail=%q", e.Agent, isWinner, e.Detail)
		}
		if hasMark {
			marked++
		}
	}
	if marked != 1 {
		t.Errorf("%d heads marked winner, want exactly 1", marked)
	}
}

// The emitted events must actually reconstruct — a fan-out has to render as a
// fan-out. This is what makes the parent a named node rather than the task id:
// linking to an id no event declares would materialise a phantom node.
func TestLogRunEvents_ReconstructsAsAFanOut(t *testing.T) {
	rlSandbox(t)

	logRunEvents([]Attempt{
		rlAttempt("h1", StatusOK, 1),
		rlAttempt("h2", StatusOK, 2),
		rlAttempt("h3", StatusOK, 3),
	}, ModeAll, Options{RunID: "run-tree", TaskID: "t"})

	events, err := runlog.Load("run-tree")
	if err != nil {
		t.Fatal(err)
	}
	tr, _ := tree.Reconstruct(events)
	rows := tr.Rows()

	if len(rows) != 4 {
		t.Fatalf("%d rows, want 4 (one swarm root + three heads): %+v", len(rows), rows)
	}
	if rows[0].Node.ID != swarmAgent {
		t.Errorf("root is %q, want %q — a phantom root means Parent named an undeclared node",
			rows[0].Node.ID, swarmAgent)
	}
	for _, r := range rows[1:] {
		if r.Depth != 1 {
			t.Errorf("head %q at depth %d, want 1", r.Node.ID, r.Depth)
		}
	}
}

// SPRT samples come from the LLR ledger, not the attempt list: the running
// confidence after each source is the whole reason --confidence exists.
func TestLogSamples_CarriesRunningConfidence(t *testing.T) {
	rlSandbox(t)

	ledger := []trust.Evidence{
		{Source: "a", Agreed: true, LLR: 1.2, LambdaAfter: 1.2, ConfidenceAfter: 0.768, CostUSD: 0.01},
		{Source: "b", Agreed: true, LLR: 0.9, LambdaAfter: 2.1, ConfidenceAfter: 0.891, CostUSD: 0.02},
		{Source: "c", Agreed: false, LLR: -0.4, LambdaAfter: 1.7, ConfidenceAfter: 0.846, CostUSD: 0.03},
	}
	logSamples(ledger, []Attempt{rlAttempt("a", StatusOK, 1)}, Options{RunID: "run-sprt", TaskID: "t"})

	events, err := runlog.Load("run-sprt")
	if err != nil {
		t.Fatal(err)
	}

	var samples []runlog.Event
	for _, e := range events {
		if e.Kind == runlog.KindSample {
			samples = append(samples, e)
		}
	}
	if len(samples) != len(ledger) {
		t.Fatalf("%d samples, want %d (one per ledger entry)", len(samples), len(ledger))
	}
	for i, s := range samples {
		if s.Confidence != ledger[i].ConfidenceAfter {
			t.Errorf("sample %d confidence = %v, want %v", i, s.Confidence, ledger[i].ConfidenceAfter)
		}
		if s.Agent != ledger[i].Source {
			t.Errorf("sample %d agent = %q, want %q", i, s.Agent, ledger[i].Source)
		}
	}
	// Disagreement is the one thing the confidence number alone cannot show.
	if got := samples[2].Detail; got == "" || got[:9] != "disagreed" {
		t.Errorf("disagreeing sample detail = %q, want it to lead with \"disagreed\"", got)
	}
}

// A run must not be attributed to the wrong ensemble root. swarm and SPRT are
// different things to a reader — racing candidates vs accumulating evidence.
func TestLogSamples_UsesItsOwnRoot(t *testing.T) {
	rlSandbox(t)

	logSamples([]trust.Evidence{{Source: "a", ConfidenceAfter: 0.9}}, nil,
		Options{RunID: "run-root", TaskID: "t"})

	events, err := runlog.Load("run-root")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind == runlog.KindSample && e.Parent != sprtAgent {
			t.Errorf("sample parent = %q, want %q", e.Parent, sprtAgent)
		}
		if e.Agent == swarmAgent {
			t.Error("an SPRT run must not hang off the swarm root")
		}
	}
}

// Observability must never fail the work. An unwritable log directory has to be
// survivable, not a panic or a hang.
func TestLogRunEvents_SurvivesUnwritableLogDir(t *testing.T) {
	rlSandbox(t)

	// A file where the runs directory must go: MkdirAll fails, every append
	// errors, and all of them are dropped at the call site.
	dir := runlog.Dir()
	if err := writeBlockingFile(t, dir); err != nil {
		t.Skipf("could not stage an unwritable log dir: %v", err)
	}

	logRunEvents([]Attempt{rlAttempt("a", StatusOK, 1)}, ModeBest,
		Options{RunID: "run-block", TaskID: "t"})
	logSamples([]trust.Evidence{{Source: "a"}}, nil, Options{RunID: "run-block", TaskID: "t"})
}

// writeBlockingFile puts a regular file where a directory is required, so
// MkdirAll cannot succeed.
func writeBlockingFile(t *testing.T, dir string) error {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	return os.WriteFile(dir, []byte("not a directory"), 0o600)
}
