// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/evalset"
)

// The test binary doubles as the verifier, so a test needs no shell and no
// PATH. TestMain rather than a skipped helper test: the suite's skip budget is
// spent, and a helper that skips is a skip in every package's count.
func TestMain(m *testing.M) {
	if want := os.Getenv("HYVERIFY_TEST_WANT"); want != "" {
		os.Exit(helperVerifier(want, os.Args[len(os.Args)-1]))
	}
	os.Exit(m.Run())
}

// helperVerifier's verdict depends on the candidate's own content, which is what
// makes it a real oracle rather than a constant.
func helperVerifier(want, file string) int {
	b, err := os.ReadFile(file)
	if err != nil {
		fmt.Println("cannot read " + file)
		return 3
	}
	if strings.Contains(string(b), want) {
		fmt.Println("found " + want)
		return 0
	}
	fmt.Println("missing " + want)
	return 1
}

type harness struct {
	dir, candidate, out, home string
	stdout, stderr            bytes.Buffer
}

// noHydraAnywhere points every home Hydra reads at a directory that does not
// exist, so a test proves independence rather than asserting it.
func newHarness(t *testing.T, content, want string) *harness {
	t.Helper()
	dir := t.TempDir()
	absent := filepath.Join(t.TempDir(), "no-such-home")
	t.Setenv("HOME", absent)
	t.Setenv("HYDRA_HOME", absent)
	t.Setenv("XDG_CONFIG_HOME", absent)
	t.Setenv("HYVERIFY_TEST_WANT", want)

	cand := filepath.Join(dir, "a.go")
	if err := os.WriteFile(cand, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return &harness{dir: dir, candidate: cand, home: absent, out: filepath.Join(dir, "corpus.jsonl")}
}

func (h *harness) run(t *testing.T, extra ...string) int {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"--candidate", h.candidate, "--out", h.out}, extra...)
	args = append(args, "--", exe, h.candidate)
	return run(args, &h.stdout, &h.stderr)
}

func (h *harness) corpus(t *testing.T) []evalset.Example {
	t.Helper()
	all, err := evalset.Load(h.out)
	if err != nil {
		t.Fatal(err)
	}
	return all
}

// The whole point of the tool: a verified record on a machine with no Hydra.
func TestRun_RecordsAPassWithNoHydraPresent(t *testing.T) {
	h := newHarness(t, "package main // GOOD\n", "GOOD")
	if code := h.run(t, "--task", "make it good", "--enum", "SIMPLE", "--head", "ollama/qwen3:4b"); code != exitPass {
		t.Fatalf("exit %d, want %d\nstderr: %s", code, exitPass, h.stderr.String())
	}
	all := h.corpus(t)
	if len(all) != 1 {
		t.Fatalf("recorded %d examples, want 1", len(all))
	}
	e := all[0]
	if !e.Passed || e.Enum != "SIMPLE" || e.Head != "ollama/qwen3:4b" || e.Domain != "go" {
		t.Errorf("record is wrong: %+v", e)
	}
	if !strings.Contains(e.Candidate, "GOOD") {
		t.Errorf("the candidate stored is not the file: %q", e.Candidate)
	}
	if e.TaskHash != evalset.TaskHashFor("make it good") {
		t.Error("the task identity is not derived from --task")
	}
	// The criterion, not an assertion about it: a run that reached Hydra's home
	// would have created it.
	if _, err := os.Stat(h.home); !os.IsNotExist(err) {
		t.Errorf("hyverify created or read %s, so it is not standalone", h.home)
	}
}

// A corpus of only passes cannot rank one head against another (#986), so a
// rejection is a record too, and the exit code carries the verdict.
func TestRun_RecordsAFailure(t *testing.T) {
	h := newHarness(t, "package main // BAD\n", "GOOD")
	if code := h.run(t, "--task", "make it good"); code != exitFail {
		t.Fatalf("exit %d, want %d\nstderr: %s", code, exitFail, h.stderr.String())
	}
	all := h.corpus(t)
	if len(all) != 1 || all[0].Passed {
		t.Fatalf("a rejection was not recorded as one: %+v", all)
	}
	if all[0].Detail == "" {
		t.Error("the rejection recorded no detail")
	}
}

// Nothing configured to judge the work is not a pass. Treating it as one is the
// mislabelling that made #982 worth a revert.
func TestRun_NothingConfiguredIsNotAPass(t *testing.T) {
	dir := t.TempDir()
	absent := filepath.Join(t.TempDir(), "no-such-home")
	t.Setenv("HOME", absent)
	t.Setenv("HYDRA_HOME", absent)
	t.Setenv("XDG_CONFIG_HOME", absent)
	cand := filepath.Join(dir, "a.rs")
	if err := os.WriteFile(cand, []byte("fn main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "corpus.jsonl")

	var stdout, stderr bytes.Buffer
	chdir(t, dir)
	if code := run([]string{"--candidate", cand, "--out", out}, &stdout, &stderr); code != exitNoVerdict {
		t.Fatalf("exit %d, want %d", code, exitNoVerdict)
	}
	if !strings.Contains(stderr.String(), "nothing is configured to judge") {
		t.Errorf("the refusal does not say why:\n%s", stderr.String())
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("a corpus was written for a verdict nobody produced")
	}
}

// A verdict is evidence about an answer only if the answer reached the thing
// the oracle inspects. Neither named nor inside the tree is the dispatch case.
func TestRun_RefusesACandidateTheVerifierCannotSee(t *testing.T) {
	h := newHarness(t, "package main // GOOD\n", "GOOD")
	elsewhere := t.TempDir()
	chdir(t, elsewhere)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--candidate", h.candidate, "--out", h.out, "--", "true"}, &stdout, &stderr)
	if code != exitNoVerdict {
		t.Fatalf("exit %d, want %d", code, exitNoVerdict)
	}
	if !strings.Contains(stderr.String(), "verdict would be about something else") {
		t.Errorf("the refusal does not name the reason:\n%s", stderr.String())
	}
	if _, err := os.Stat(h.out); err == nil {
		t.Error("an unreachable candidate was still recorded")
	}
}

// One resolution of what counts as a check, shared with hyctl, or the two
// disagree about whether work passed.
func TestResolve_PrefersTheNamedCommandThenTheRepo(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)

	argv, label := resolve([]string{"my", "checker"}, filepath.Join(dir, "a.go"))
	if len(argv) != 2 || argv[0] != "my" || label != "my checker" {
		t.Errorf("an explicit command was not preferred: %v %q", argv, label)
	}
	argv, label = resolve(nil, filepath.Join(dir, "a.go"))
	if len(argv) == 0 || argv[0] != "go" || label != "go test ./..." {
		t.Errorf("a Go module did not resolve its own suite: %v %q", argv, label)
	}
}

// A record written here is one Hydra reads, and the reverse, which is what
// makes this a corpus rather than a second format.
func TestRecord_RoundTripsWithHydrasOwnWriter(t *testing.T) {
	h := newHarness(t, "package main // GOOD\n", "GOOD")
	if code := h.run(t, "--task", "mine"); code != exitPass {
		t.Fatalf("exit %d\nstderr: %s", code, h.stderr.String())
	}
	// Hydra's own writer appends to the same file, through the same package.
	if _, err := evalset.Add(h.out, evalset.Example{
		TaskHash: evalset.TaskHashFor("theirs"), Domain: "go",
		Source: "editor:validator", Candidate: "package main // OTHER\n", Passed: true,
	}); err != nil {
		t.Fatal(err)
	}
	all := h.corpus(t)
	if len(all) != 2 {
		t.Fatalf("read back %d examples, want 2", len(all))
	}
	if all[0].Source != "hyverify" || all[1].Source != "editor:validator" {
		t.Errorf("the two writers did not both round-trip: %q %q", all[0].Source, all[1].Source)
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

// Three ways to have no verdict before anything runs. Each is exit 2, and none
// of them writes a record.
func TestRun_RefusesBeforeRunningAnything(t *testing.T) {
	h := newHarness(t, "package main // GOOD\n", "GOOD")
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no candidate", []string{"--out", h.out}, "hyverify --candidate"},
		{"unreadable candidate", []string{"--candidate", filepath.Join(h.dir, "gone.go"), "--out", h.out}, "reading the candidate"},
		{"unknown flag", []string{"--candidate", h.candidate, "--nope"}, "flag provided but not defined"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(tc.args, &stdout, &stderr); code != exitNoVerdict {
				t.Fatalf("exit %d, want %d", code, exitNoVerdict)
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("stderr does not contain %q:\n%s", tc.want, stderr.String())
			}
		})
	}
	if _, err := os.Stat(h.out); err == nil {
		t.Error("a refusal still wrote a corpus")
	}
}

// The corpus deduplicates, so running the same verification twice must say so
// rather than report a second record that does not exist.
func TestRun_SaysWhenTheExampleWasAlreadyRecorded(t *testing.T) {
	h := newHarness(t, "package main // GOOD\n", "GOOD")
	if code := h.run(t, "--task", "same"); code != exitPass {
		t.Fatalf("first run: exit %d\n%s", code, h.stderr.String())
	}
	h.stdout.Reset()
	if code := h.run(t, "--task", "same"); code != exitPass {
		t.Fatalf("second run: exit %d\n%s", code, h.stderr.String())
	}
	if !strings.Contains(h.stdout.String(), "already recorded") {
		t.Errorf("a duplicate was reported as a new record:\n%s", h.stdout.String())
	}
	if all := h.corpus(t); len(all) != 1 {
		t.Errorf("the corpus holds %d copies of one example", len(all))
	}
}

// Without --task every example in a domain shares one identity, which silently
// degrades dedup to the candidate alone (#973). Say so rather than let someone
// fill a corpus that cannot tell two tasks apart.
func TestRun_WarnsWhenNoTaskNamesTheWork(t *testing.T) {
	h := newHarness(t, "package main // GOOD\n", "GOOD")
	if code := h.run(t); code != exitPass {
		t.Fatalf("exit %d\n%s", code, h.stderr.String())
	}
	if !strings.Contains(h.stdout.String(), "no --task given") {
		t.Errorf("an unnamed task was not flagged:\n%s", h.stdout.String())
	}
}

// A verifier that cannot start has produced no verdict about the candidate, so
// it must not read as a failing one.
func TestJudge_DistinguishesANonStartFromAFailure(t *testing.T) {
	passed, detail := judge([]string{filepath.Join(t.TempDir(), "not-a-program")})
	if passed {
		t.Fatal("a verifier that never ran reported a pass")
	}
	if !strings.Contains(detail, "verifier did not run") {
		t.Errorf("detail %q does not distinguish a non-start from a rejection", detail)
	}
}
