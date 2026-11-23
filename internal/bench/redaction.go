// SPDX-License-Identifier: MIT

package bench

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/ground"
	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/stats"
	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/internal/util"
)

// redactAnswerTokens bounds one answer: a sentence or a small JSON object.
const redactAnswerTokens = 300

// RedactTask is one made-up task with values --redact replaces (bench/redaction/tasks.jsonl).
// Needs is "copy" (the answer repeats a value), "read" (it needs a fact inside one, which
// a placeholder hides) or "around" (it needs none of them).
type RedactTask struct {
	ID      string   `json:"id"`
	Work    string   `json:"work"`
	Needs   string   `json:"needs"`
	Source  string   `json:"source"`
	Ask     string   `json:"ask"`
	Pattern string   `json:"pattern"` // the match check on the restored answer
	Ground  bool     `json:"ground"`  // also the ground check against the source
	Ref     string   `json:"ref"`     // a right answer, which must pass its own checks
	Values  []string `json:"values"`  // planted values redaction must replace
}

// Prompt is the ask with the source fenced, so the ground check has context.
func (t RedactTask) Prompt() string {
	return t.Ask + "\n\n" + util.WrapUntrusted("SOURCE", t.Source)
}

// Grade passes an answer that matches the pattern and, when asked, is grounded in the
// source. An answer with no checkable claim is not ungrounded, so ground passes it.
func (t RedactTask) Grade(ctx context.Context, answer string) (bool, string, error) {
	m, err := oracle.NewMatch(t.ID, nil, t.Pattern)
	if err != nil {
		return false, "", err
	}
	v, err := m.Verify(ctx, answer, trust.Task{})
	if err != nil || !v.Passed {
		return false, v.Detail, err
	}
	if !t.Ground {
		return true, "", nil
	}
	g, err := (&ground.Oracle{Prompt: t.Prompt()}).Verify(ctx, answer, trust.Task{})
	switch {
	case errors.Is(err, ground.ErrNoClaims):
		return true, "", nil
	case err != nil:
		return false, "", err
	}
	return g.Passed, g.Detail, nil
}

// ReadRedactTasks loads the task file, refusing a task a run could not grade.
func ReadRedactTasks(path string) ([]RedactTask, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []RedactTask
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var t RedactTask
		if err := json.Unmarshal(sc.Bytes(), &t); err != nil {
			return nil, fmt.Errorf("bench: %s line %d: %w", path, len(out)+1, err)
		}
		if t.ID == "" || t.Work == "" || !slices.Contains([]string{"copy", "read", "around"}, t.Needs) ||
			t.Source == "" || t.Ask == "" || t.Pattern == "" || len(t.Values) == 0 {
			return nil, fmt.Errorf("bench: %s: task %q is incomplete", path, t.ID)
		}
		out = append(out, t)
	}
	return out, sc.Err()
}

// RedactRow is one task answered with redaction off and on, on one model.
type RedactRow struct {
	ID       string `json:"id"`
	Work     string `json:"work"`
	Needs    string `json:"needs"`
	Off      bool   `json:"passed_off"`
	On       bool   `json:"passed_on"`
	OffWhy   string `json:"detail_off,omitempty"`
	OnWhy    string `json:"detail_on,omitempty"`
	Answer   string `json:"answer_off"`
	Restored string `json:"answer_on"` // after the vault put the values back
	Calls    []Call `json:"calls"`
}

// RedactRequest is the request a task sends; redacted, its values are placeholders
// from v, the way dispatch sends a hosted model.
func RedactRequest(t RedactTask, v *policy.Vault) executor.Request {
	zero := 0.0
	p := t.Prompt()
	if v != nil {
		p = policy.RedactReversible(p, v)
	}
	return executor.Request{Prompt: p, Temperature: &zero, MaxTokens: redactAnswerTokens}
}

// RunRedaction asks arm each task with redaction off, then on, and grades both the same
// way. A failed call fails its side; an error from the arm (the cap, a refused key) stops.
func RunRedaction(ctx context.Context, arm Arm, tasks []RedactTask, b *Budget, spend func(Call) error) ([]RedactRow, error) {
	rows := make([]RedactRow, 0, len(tasks))
	for _, t := range tasks {
		r := RedactRow{ID: t.ID, Work: t.Work, Needs: t.Needs}
		for _, on := range []bool{false, true} {
			var v *policy.Vault
			if on {
				v = policy.NewVault()
			}
			att, err := arm.Call(ctx, Item{ID: t.ID}, RedactRequest(t, v), b, spend)
			r.Calls = append(r.Calls, att.Calls...)
			if err != nil {
				return rows, err
			}
			ans, passed, why := att.Answer, false, "call failed: "+att.ErrKind
			if att.ErrKind == "" {
				if on {
					ans, _ = v.Restore(ans)
				}
				if passed, why, err = t.Grade(ctx, ans); err != nil {
					return rows, fmt.Errorf("bench: grading %s: %w", t.ID, err)
				}
			}
			if on {
				r.On, r.OnWhy, r.Restored = passed, why, ans
			} else {
				r.Off, r.OffWhy, r.Answer = passed, why, ans
			}
		}
		rows = append(rows, r)
	}
	return rows, nil
}

// RedactWorstCase is the most one run could cost on arm, for the line printed before it.
func RedactWorstCase(arm Arm, tasks []RedactTask) (float64, error) {
	var sum float64
	for _, t := range tasks {
		for _, v := range []*policy.Vault{nil, policy.NewVault()} {
			c, err := arm.WorstCaseUSD(RedactRequest(t, v))
			if err != nil {
				return 0, err
			}
			sum += c
		}
	}
	return sum, nil
}

// RedactTable is pass rates off and on with Wilson intervals, per kind of work and per
// what the answer needs, with the exact McNemar p on the pairs that disagree.
func RedactTable(rows []RedactRow, model string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "| Group | n | Pass off %% (95%% CI) | Pass on %% (95%% CI) | Off only | On only | McNemar p |\n|---|---|---|---|---|---|---|\n")
	groups := []string{"all"}
	for _, r := range rows {
		for _, g := range []string{r.Work, "needs " + r.Needs} {
			if !slices.Contains(groups, g) {
				groups = append(groups, g)
			}
		}
	}
	for _, g := range groups {
		var n, off, on int
		var d stats.Discordant
		for _, r := range rows {
			if g != "all" && g != r.Work && g != "needs "+r.Needs {
				continue
			}
			n++
			off, on = off+btoi(r.Off), on+btoi(r.On)
			d.B, d.C = d.B+btoi(r.Off && !r.On), d.C+btoi(r.On && !r.Off)
		}
		offLo, offHi, err := stats.Wilson(off, n, stats.Z95)
		if err != nil {
			return "", err
		}
		onLo, onHi, err := stats.Wilson(on, n, stats.Z95)
		if err != nil {
			return "", err
		}
		_, p, err := stats.McNemarExact(d)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "| %s | %d | %.0f (%.0f–%.0f) | %.0f (%.0f–%.0f) | %d | %d | %.3g |\n", g, n,
			100*float64(off)/float64(n), 100*offLo, 100*offHi, 100*float64(on)/float64(n), 100*onLo, 100*onHi, d.B, d.C, p)
	}
	fmt.Fprintf(&b, "\nModel: %s. Temperature 0, one answer per task and side.\n", modelLabel(model))
	return b.String(), nil
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
