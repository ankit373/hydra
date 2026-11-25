// SPDX-License-Identifier: MIT

package replay

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

// outcomeRows: 12 tasks, two heads; the cheap one's check passes 3 tasks in 4,
// and one of those is wrong.
func outcomeRows() []Row {
	var rows []Row
	for i := range 12 {
		task := fmt.Sprintf("t%02d", i)
		rows = append(rows, row(task, "cheap", 1, i%4 != 1, i%4 != 3), row(task, "dear", 5, true, true))
	}
	return rows
}

func TestRun_OutcomesSumToTheReport(t *testing.T) {
	cfg := Config{Policies: []string{"always_strongest", "first_check_pass"}, Alphas: []float64{0.05, 0.1},
		Seeds: []uint64{0, 1, 2}, Check: "format"}
	plain, err := Run(outcomeRows(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	cfg.Outcomes = &buf
	rep, err := Run(outcomeRows(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plain, rep) {
		t.Fatal("writing outcomes changed the report")
	}

	type key struct {
		policy string
		alpha  float64
		seed   uint64
	}
	type tally struct{ tasks, accepted, correct, wrong int }
	got := map[key]*tally{}
	sc := bufio.NewScanner(&buf)
	lines := 0
	for sc.Scan() {
		lines++
		var o TaskOutcome
		if err := json.Unmarshal(sc.Bytes(), &o); err != nil {
			t.Fatalf("line %d: %v", lines, err)
		}
		if (o.Accepted == nil) != (o.Correct == nil) || o.Kind != "math" || o.Task == "" {
			t.Fatalf("line %d is inconsistent: %s", lines, sc.Text())
		}
		k := key{o.Policy, o.Alpha, o.Seed}
		if got[k] == nil {
			got[k] = &tally{}
		}
		g := got[k]
		g.tasks++
		if o.Accepted != nil {
			g.accepted++
			if *o.Correct {
				g.correct++
			} else {
				g.wrong++
			}
		}
	}
	// 2 policies x 2 alphas x 3 seeds x 6 test tasks.
	if lines != 72 || len(rep.Results) != 12 {
		t.Fatalf("%d lines for %d results, want 72 for 12", lines, len(rep.Results))
	}
	for _, r := range rep.Results {
		g := got[key{r.Policy, r.Alpha, r.Seed}]
		if g == nil || g.tasks != r.Tasks || g.accepted != r.Accepted || g.correct != r.CorrectAccepted || g.wrong != r.WrongAccepted {
			t.Fatalf("outcomes %+v do not sum to result %+v", g, r)
		}
	}
}

func TestRun_OutcomesAreByteIdenticalAcrossRuns(t *testing.T) {
	cfg := Config{Policies: []string{"first_check_pass"}, Alphas: []float64{0.05}, Seeds: []uint64{0, 1}, Check: "format"}
	var a, b bytes.Buffer
	cfg.Outcomes = &a
	if _, err := Run(outcomeRows(), cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Outcomes = &b
	if _, err := Run(outcomeRows(), cfg); err != nil {
		t.Fatal(err)
	}
	if a.Len() == 0 || !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("two runs wrote different outcomes")
	}
	var first TaskOutcome
	if err := json.Unmarshal(bytes.SplitN(a.Bytes(), []byte("\n"), 2)[0], &first); err != nil {
		t.Fatal(err)
	}
	if first.Tried == nil {
		t.Fatal("tried must be a list, never null")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, fmt.Errorf("disk full") }

func TestRun_OutcomesWriteErrorFails(t *testing.T) {
	cfg := Config{Policies: []string{"always_strongest"}, Alphas: []float64{0.05}, Seeds: []uint64{0}, Outcomes: failWriter{}}
	if _, err := Run(outcomeRows(), cfg); err == nil {
		t.Fatal("a failed outcomes write was ignored")
	}
}
