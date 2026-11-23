// SPDX-License-Identifier: MIT

package bench

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/provider"
)

// Every committed task can be graded: its right answer passes, and redaction hides
// every value it planted, so the "on" side really runs on placeholders.
func TestCommittedRedactTasks_GradeTheirOwnAnswerAndHideEveryValue(t *testing.T) {
	tasks, err := ReadRedactTasks("../../bench/redaction/tasks.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) < 30 {
		t.Fatalf("%d tasks, want at least 30", len(tasks))
	}
	for _, task := range tasks {
		if ok, why, err := task.Grade(context.Background(), task.Ref); err != nil || !ok {
			t.Errorf("%s: its own answer fails: %s %v", task.ID, why, err)
		}
		sent := RedactRequest(task, policy.NewVault()).Prompt
		for _, v := range task.Values {
			if !strings.Contains(task.Source, v) || strings.Contains(sent, v) {
				t.Errorf("%s: %q is not in the source, or redaction left it in", task.ID, v)
			}
		}
	}
}

// echoExec answers a copy or read task: the placeholder it was sent if any, else the email.
type echoExec struct{ sent []string }

var placeholder = regexp.MustCompile(`<<[A-Z_]+_\d+>>`)

func (e *echoExec) Execute(_ context.Context, req executor.Request) (*executor.Response, error) {
	e.sent = append(e.sent, req.Prompt)
	ans := "ana@example.com"
	if p := placeholder.FindString(req.Prompt); p != "" {
		ans = p
	}
	if strings.Contains(req.Prompt, "domain") && ans == "ana@example.com" {
		ans = "example.com"
	}
	return &executor.Response{Output: ans, Model: "m", InputTokens: 10, OutputTokens: 3}, nil
}

// A copy answer survives redaction once restored; a read answer cannot see the value,
// so it is the discordant pair the table counts.
func TestRunRedaction_RestoresBeforeGradingAndCountsWhatRedactionCost(t *testing.T) {
	src := "Customer ana@example.com asks about order 12."
	tasks := []RedactTask{
		{ID: "copy", Work: "support", Needs: "copy", Source: src, Ask: "Which email?", Pattern: `ana@example\.com`},
		{ID: "read", Work: "support", Needs: "read", Source: src, Ask: "Which domain?", Pattern: `example\.com`},
	}
	exec := &echoExec{}
	d, err := NewDirect("r", ArmConfig{Kind: KindDirect, Models: []string{"openrouter:m"}}, testPrices("m"),
		func(provider.Head) executor.Executor { return exec }, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := RunRedaction(context.Background(), d, tasks, NewBudget(100, 0), noSpend)
	if err != nil {
		t.Fatal(err)
	}
	if !rows[0].Off || !rows[0].On || rows[0].Restored != "ana@example.com" {
		t.Errorf("copy: %+v", rows[0])
	}
	if !rows[1].Off || rows[1].On {
		t.Errorf("read: %+v, want passed off and failed on", rows[1])
	}
	for i, p := range exec.sent {
		if redacted := i%2 == 1; redacted == strings.Contains(p, "ana@example.com") {
			t.Errorf("call %d (redacted %v) sent %q", i, redacted, p)
		}
	}
	table, err := RedactTable(rows, "ollama:m")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(table, "| all | 2 | 100 (34–100) | 50 (9–91) | 1 | 0 | 1 |") || !strings.Contains(table, "m (local)") {
		t.Errorf("table:\n%s", table)
	}
}

func TestSnapshotFor_PricesALocalModelAtZero(t *testing.T) {
	s, err := SnapshotFor("ollama:qwen3:8b", nil, time.Time{})
	if err != nil || s["qwen3:8b"] != (Price{Model: "qwen3:8b", Ref: "local", Source: "local"}) {
		t.Fatalf("%+v, %v", s, err)
	}
}
