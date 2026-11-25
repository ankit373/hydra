// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

// replayFixture is 4 tasks x 3 heads. On every task the cheap head fails its tests,
// the middle one passes them with a wrong answer, and the dear one is right.
func replayFixture(t *testing.T) string {
	t.Helper()
	heads := []struct {
		name          string
		cost          float64
		correct, pass bool
	}{{"cheap", 0.001, false, false}, {"middle", 0.01, false, true}, {"dear", 0.1, true, true}}
	var b strings.Builder
	for i := 0; i < 4; i++ {
		for _, h := range heads {
			fmt.Fprintf(&b, `{"task":"t%d","kind":"code","head":%q,"cost_usd":%v,"correct":%v,"answer_key":"k","verdicts":{"tests":%v},"check_cost_usd":{"tests":0.0005}}`+"\n",
				i, h.name, h.cost, h.correct, h.pass)
		}
	}
	p := filepath.Join(t.TempDir(), "candidates.jsonl")
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBenchReplay_TableFromFixture(t *testing.T) {
	testutil.NewSandbox(t)
	args := []string{"bench", "replay", "--candidates", replayFixture(t), "--check", "tests", "--alpha", "0.05", "--splits", "3"}
	out, _, err := run(t, args...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// Two test tasks per split: 0 of 2 wrong is [0, 0.8419], 2 of 2 is [0.1581, 1].
	for _, want := range []string{
		"false accept per task (mean)",
		"code  always_strongest  0.05   0.0000                        [0.0000, 0.8419]  0.0000       0%                 0.0%     0.100000    0.100000",
		"code  first_check_pass  0.05   1.0000                        [0.1581, 1.0000]  1.0000       100%               0.0%     0.012000    n/a (none correct)",
		"seeds: 0 1 2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	again, _, err := run(t, args...)
	if err != nil || again != out {
		t.Fatalf("same seeds, different table: %v\n%s\n%s", err, out, again)
	}

	js, _, err := run(t, append(args, "--json")...)
	if err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Seeds   []uint64
		Results []struct {
			Policy        string
			Tasks         int
			WrongAccepted int `json:"wrong_accepted"`
		}
	}
	if err := json.Unmarshal([]byte(js), &rep); err != nil {
		t.Fatalf("%v\n%s", err, js)
	}
	if len(rep.Seeds) != 3 || len(rep.Results) != 6 || rep.Results[3].Policy != "first_check_pass" ||
		rep.Results[3].Tasks != 2 || rep.Results[3].WrongAccepted != 2 {
		t.Fatalf("json: %+v", rep)
	}
}

func TestBenchReplay_FirstCheckPassNeedsACheck(t *testing.T) {
	testutil.NewSandbox(t)
	_, _, err := run(t, "bench", "replay", "--candidates", replayFixture(t))
	if err == nil || !strings.Contains(err.Error(), "--check") {
		t.Fatalf("got %v; want a refusal naming --check", err)
	}
}

func TestBenchReplay_DeltaFlagReachesTheReplay(t *testing.T) {
	testutil.NewSandbox(t)
	_, _, err := run(t, "bench", "replay", "--candidates", replayFixture(t), "--policy", "always_strongest", "--delta", "2")
	if err == nil || !strings.Contains(err.Error(), "delta 2") {
		t.Fatalf("want delta 2 refused, got %v", err)
	}
}
