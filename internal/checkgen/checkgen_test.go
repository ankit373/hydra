// SPDX-License-Identifier: MIT

package checkgen

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/trust"
)

// fakeGen writes "labels" specs whose body is the one word the answer must contain.
type fakeGen struct{ broken bool }

func (*fakeGen) Kind() string { return "labels" }

func (*fakeGen) Generate(_ context.Context, task Task) ([]Spec, error) {
	gen := GenInfo{Head: "local:qwen", Family: "qwen"}
	var out []Spec
	for _, w := range []string{"refund", "invoice"} {
		s, err := NewSpec("labels", w, task, gen)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func (g *fakeGen) Oracle(s Spec) (oracle.Oracle, error) {
	if g.broken {
		return brokenOracle{}, nil
	}
	var word string
	if err := json.Unmarshal(s.Body, &word); err != nil {
		return nil, err
	}
	return containsOracle(word), nil
}

type containsOracle string

func (w containsOracle) Verify(_ context.Context, c string, _ trust.Task) (oracle.Verdict, error) {
	if strings.Contains(c, string(w)) {
		return oracle.Verdict{Passed: true}, nil
	}
	return oracle.Verdict{Detail: "missing " + string(w)}, nil
}

type brokenOracle struct{}

func (brokenOracle) Verify(context.Context, string, trust.Task) (oracle.Verdict, error) {
	return oracle.Verdict{}, errors.New("checker crashed")
}

// setup isolates HYDRA_HOME and the registry, registers g, and seeds one span.
func setup(t *testing.T, g Generator) (runlog.Event, []Spec) {
	t.Helper()
	t.Setenv("HYDRA_HOME", t.TempDir())
	registry.Lock()
	saved := registry.byWork
	savedKind := registry.byKind
	registry.byWork, registry.byKind = map[string][]Generator{}, map[string]Generator{}
	registry.Unlock()
	t.Cleanup(func() {
		registry.Lock()
		registry.byWork, registry.byKind = saved, savedKind
		registry.Unlock()
	})
	if err := Register("writing", g); err != nil {
		t.Fatal(err)
	}
	span := runlog.Event{
		Kind: runlog.KindDispatchFinished, RunID: "r1", TaskID: "t1",
		SpanID: runlog.NewSpanID(), Head: "openai/gpt-x", Domain: "writing.support",
	}
	if err := runlog.New("r1").Append(span); err != nil {
		t.Fatal(err)
	}
	specs, err := g.Generate(context.Background(), Task{Text: "reply to the customer", Kind: "writing.support"})
	if err != nil {
		t.Fatal(err)
	}
	return span, specs
}

var answering = provider.Head{ID: "openai/gpt-x", Provider: "openai"}

func TestRun_WritesOneVerdictPerSpecWithBothFamilies(t *testing.T) {
	span, specs := setup(t, &fakeGen{})
	res, err := Run(context.Background(), specs, "your refund is on its way", answering, span)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || !res[0].Verdict.Passed || res[1].Verdict.Passed {
		t.Fatalf("results = %+v, want refund pass, invoice fail", res)
	}
	answers, err := evalset.Joint(evalset.VerdictsPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(answers) != 1 || len(answers[0].Checks) != 2 {
		t.Fatalf("verdict log = %+v, want one answer with two checks", answers)
	}
	for i, v := range answers[0].Checks {
		if v.Check != "gen:labels" || v.Head != "openai/gpt-x" || v.GenFamily != "qwen" ||
			v.Domain != "writing.support" || v.TaskHash == "" || !strings.HasPrefix(v.Detail, "spec "+specs[i].ID) {
			t.Errorf("verdict %d = %+v", i, v)
		}
	}
	events, err := runlog.Load("r1")
	if err != nil {
		t.Fatal(err)
	}
	scores := runlog.ScoresBySpan(events)[span.SpanID]
	if len(scores) != 2 || scores[0].Value != 1 || scores[1].Value != 0 || scores[0].Source != "gen:labels" {
		t.Fatalf("span scores = %+v, want 1 then 0 from gen:labels", scores)
	}
}

func TestRun_OracleErrorIsNotCheckedAndWritesNothing(t *testing.T) {
	span, specs := setup(t, &fakeGen{broken: true})
	res, err := Run(context.Background(), specs, "your refund is on its way", answering, span)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Checked() || r.Verdict.Passed {
			t.Errorf("a crashed oracle read as a verdict: %+v", r)
		}
	}
	if Label(res) != LabelNotChecked {
		t.Errorf("label = %q, want %q", Label(res), LabelNotChecked)
	}
	if _, err := os.Stat(evalset.VerdictsPath()); !os.IsNotExist(err) {
		t.Errorf("verdict log written for checks that never ran: %v", err)
	}
	events, err := runlog.Load("r1")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(runlog.ScoresBySpan(events)[span.SpanID]); n != 0 {
		t.Errorf("span carries %d scores from checks that never ran", n)
	}
}

func TestRun_StoresSpecOnceByHash(t *testing.T) {
	span, specs := setup(t, &fakeGen{})
	for range 2 {
		if _, err := Run(context.Background(), specs[:1], "refund", answering, span); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(SpecsDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != specs[0].ID+".json" {
		t.Fatalf("spec store = %v, want exactly %s.json", entries, specs[0].ID)
	}
	info, err := entries[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o600 {
		t.Errorf("spec mode = %v, want 0600", perm)
	}
	raw, err := os.ReadFile(filepath.Join(SpecsDir(), entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var got Spec
	if err := json.Unmarshal(raw, &got); err != nil || got.ID != specs[0].ID || string(got.Body) != `"refund"` {
		t.Fatalf("stored spec = %s (%v)", raw, err)
	}
}

func TestLabel_AllPassedSomeFailedNotChecked(t *testing.T) {
	pass := Result{Verdict: oracle.Verdict{Passed: true}}
	fail := Result{}
	broken := Result{Err: errors.New("no")}
	for _, tc := range []struct {
		name string
		in   []Result
		want string
	}{
		{"none", nil, LabelNotChecked},
		{"all pass", []Result{pass, pass}, LabelAllPassed},
		{"one fail", []Result{pass, fail}, LabelSomeFailed},
		{"fail beats broken", []Result{broken, fail}, LabelSomeFailed},
		{"pass with one broken", []Result{pass, broken}, LabelNotChecked},
		{"all broken", []Result{broken}, LabelNotChecked},
	} {
		if got := Label(tc.in); got != tc.want {
			t.Errorf("%s: Label = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestRegister_RefusesUnknownKindOfWork(t *testing.T) {
	setup(t, &fakeGen{})
	if err := Register("poetry", &fakeGen{}); err == nil {
		t.Error("registered under a kind of work that is not in trust.Kinds")
	}
	if err := Register("support", &fakeGen{}); err == nil {
		t.Error("registered under an area, not a kind of work")
	}
	if err := Register("summary", &fakeGen{}); err == nil {
		t.Error("a second generator took a spec kind that already has one")
	}
	if got := For("writing.support"); len(got) != 1 {
		t.Errorf("For(writing.support) = %d generators, want 1", len(got))
	}
}

func TestRun_SpecNobodyCanBuildIsNotChecked(t *testing.T) {
	span, _ := setup(t, &fakeGen{})
	if _, err := NewSpec("vibes", "x", Task{}, GenInfo{}); err == nil {
		t.Error("NewSpec took a spec kind that does not exist")
	}
	orphan, err := NewSpec("rubric", "is it polite?", Task{Text: "reply"}, GenInfo{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), []Spec{orphan, {Kind: "labels"}}, "hi", answering, span)
	if err == nil {
		t.Error("a spec with no id was run without an error")
	}
	if res[0].Checked() || Label(res[:1]) != LabelNotChecked {
		t.Errorf("a spec kind with no generator read as checked: %+v", res[0])
	}
}
