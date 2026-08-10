// SPDX-License-Identifier: MIT

package oracle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/trust"
)

// An oracle is a high-D evidence source: its verdict can outweigh several
// models' votes. A verdict produced by a broken oracle is therefore worse than
// no oracle at all, so every failure path has to be a failure, not a pass.

func TestFirstLine(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"one", "one"},
		{"first\nsecond", "first"},
		// TrimSpace runs on the whole string first, so leading blank lines are
		// skipped to reach real content — good — but trailing spaces on the
		// first line survive. Both are the actual contract; I expected the
		// opposite of each.
		{"  padded  \nrest", "padded  "},
		{"\n\nleading blanks", "leading blanks"},
		{"", ""},
		{"trailing\n", "trailing"},
	} {
		if got := firstLine(tc.in); got != tc.want {
			t.Errorf("firstLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDefaultWriteTemp_MaterializesAndCleansUp(t *testing.T) {
	path, cleanup, err := defaultWriteTemp("candidate content")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the temp file was not readable: %v", err)
	}
	if string(body) != "candidate content" {
		t.Errorf("temp file holds %q", body)
	}

	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("cleanup left the temp file behind — an oracle runs per candidate, " +
			"so these accumulate")
	}
}

// {file} materialization failing must abort the verification, not run the
// command against a path that does not exist and read its failure as "the
// candidate is wrong".
func TestVerify_WriteTempFailureIsAnErrorNotAFailedVerdict(t *testing.T) {
	o := &CommandOracle{
		Template: "cat {file}",
		Source:   "verifier:test",
		writeTemp: func(string) (string, func(), error) {
			return "", nil, errors.New("disk full")
		},
	}
	v, err := o.Verify(context.Background(), "candidate", trust.Task{})
	if err == nil {
		t.Fatal("a materialization failure produced no error")
	}
	if v.Passed {
		t.Error("a materialization failure reported the candidate as passing")
	}
}

// The verdict is the command's exit status: 0 passes, anything else fails.
func TestVerify_ExitStatusDecidesTheVerdict(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Log("windows: no /bin/sh-style true/false to drive exit codes here")
		return
	}
	cases := []struct {
		name, template string
		wantPass       bool
	}{
		{"exit 0 passes", "true", true},
		{"exit 1 fails", "false", false},
		{"missing binary fails", "definitely-not-a-real-binary-xyz", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := &CommandOracle{Template: tc.template, Source: "verifier:test"}
			v, _ := o.Verify(context.Background(), "candidate", trust.Task{})
			if v.Passed != tc.wantPass {
				t.Errorf("Passed = %v, want %v", v.Passed, tc.wantPass)
			}
		})
	}
}

// A missing binary must not be read as "the candidate is wrong" — that is the
// oracle being unavailable, and treating it as evidence would let a broken
// toolchain veto correct answers.
func TestVerify_MissingBinaryIsAnError(t *testing.T) {
	o := &CommandOracle{Template: "definitely-not-a-real-binary-xyz --check", Source: "v"}
	_, err := o.Verify(context.Background(), "x", trust.Task{})
	if err == nil {
		t.Error("a missing oracle binary produced no error; a broken toolchain " +
			"would be indistinguishable from a wrong answer")
	}
}

func TestVerify_EmptyTemplateIsAnError(t *testing.T) {
	o := &CommandOracle{Template: "   ", Source: "v"}
	if _, err := o.Verify(context.Background(), "x", trust.Task{}); err == nil {
		t.Error("an empty oracle template verified successfully")
	}
}

// buildArgs substitutes both placeholders and keeps a materialized path as one
// argument — splitting on whitespace would point the oracle at the wrong file,
// or at several files that do not exist.
func TestBuildArgs_SubstitutionAndSpacing(t *testing.T) {
	const spaced = "/tmp/a file.txt"
	o := &CommandOracle{
		Template:  "check {file} --answer {answer}",
		Source:    "v",
		writeTemp: func(string) (string, func(), error) { return spaced, func() {}, nil },
	}

	parts, cleanup, err := o.buildArgs("the-answer")
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		cleanup()
	}
	if len(parts) == 0 {
		t.Fatal("buildArgs returned nothing")
	}
	if parts[0] != "check" {
		t.Errorf("first arg = %q, want the command", parts[0])
	}

	var sawFile, sawAnswer bool
	for _, a := range parts {
		if a == spaced {
			sawFile = true
		}
		if a == "the-answer" {
			sawAnswer = true
		}
	}
	if !sawFile {
		t.Errorf("the file path was fragmented across args: %q", parts)
	}
	if !sawAnswer {
		t.Errorf("{answer} was not substituted: %q", parts)
	}

	// With no {file}, nothing is materialized and there is nothing to clean up.
	plain := &CommandOracle{Template: "go test ./... {answer}", Source: "v"}
	p2, c2, err := plain.buildArgs("x")
	if err != nil {
		t.Fatal(err)
	}
	if c2 != nil {
		t.Error("a template with no {file} returned a cleanup function")
	}
	if len(p2) < 2 || p2[0] != "go" {
		t.Errorf("buildArgs = %q", p2)
	}
}

// A candidate answer containing whitespace or flag-like tokens must land as
// ONE atomic argv element, never re-split — otherwise it can inject extra
// argv entries into whatever binary the template names (CWE-88 argument
// injection). This is the regression test for the bug: {answer} used to be
// substituted via raw string-replace before Fields-splitting, so whitespace
// in the candidate fragmented into multiple argv tokens.
func TestBuildArgs_AnswerWithWhitespaceIsOneAtomicArg(t *testing.T) {
	o := &CommandOracle{Template: "grep {answer} file.txt", Source: "v"}
	malicious := "ok --exec=rm -rf /"

	parts, cleanup, err := o.buildArgs(malicious)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		cleanup()
	}
	want := []string{"grep", malicious, "file.txt"}
	if len(parts) != len(want) {
		t.Fatalf("buildArgs = %q, want %q — the candidate was fragmented into %d argv tokens instead of 1",
			parts, want, len(parts)-2)
	}
	for i := range want {
		if parts[i] != want[i] {
			t.Errorf("parts[%d] = %q, want %q", i, parts[i], want[i])
		}
	}
}

// The same guarantee must hold when {answer} and {file} share a template,
// each substituting atomically regardless of order.
func TestBuildArgs_AnswerAndFileBothAtomicRegardlessOfOrder(t *testing.T) {
	malicious := "ok --exec=rm -rf /"
	o := &CommandOracle{
		Template:  "diff {answer} {file}",
		Source:    "v",
		writeTemp: func(string) (string, func(), error) { return "/tmp/expected.txt", func() {}, nil },
	}
	parts, cleanup, err := o.buildArgs(malicious)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		cleanup()
	}
	want := []string{"diff", malicious, "/tmp/expected.txt"}
	if len(parts) != len(want) {
		t.Fatalf("buildArgs = %q, want %q", parts, want)
	}
	for i := range want {
		if parts[i] != want[i] {
			t.Errorf("parts[%d] = %q, want %q", i, parts[i], want[i])
		}
	}
}

// A cancelled context must stop the oracle rather than let it run to its own
// timeout — the SPRT loop relies on this to bound a run.
func TestVerify_HonoursContextCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Log("windows: no sleep binary to hold the command open")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	o := &CommandOracle{Template: "sleep 30", Source: "v"}
	v, err := o.Verify(ctx, "x", trust.Task{})
	if err == nil && v.Passed {
		t.Error("a cancelled verification reported a pass")
	}
}

// The verdict carries the command's own output so a user can see why it failed;
// an empty detail on failure is a dead end.
func TestVerify_FailureCarriesTheCommandsOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Log("windows: shell differences make this fixture unportable")
		return
	}
	o := &CommandOracle{Template: "sh -c 'echo the-real-reason >&2; exit 1'", Source: "v"}
	v, _ := o.Verify(context.Background(), "x", trust.Task{})
	if v.Passed {
		t.Fatal("a failing command reported a pass")
	}
	if strings.TrimSpace(v.Detail) == "" {
		t.Error("a failed verdict carries no detail; the user cannot tell why")
	}
}

// defaultWriteTemp's error paths: a full disk or an unwritable temp dir must
// surface, because Verify turns a materialization failure into an error rather
// than a verdict — and that only works if the failure is reported.
func TestDefaultWriteTemp_UnwritableTempDirIsAnError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Log("windows: TMPDIR is not honoured the same way")
		return
	}
	// Point TMPDIR at a path that is a regular file, so CreateTemp cannot work.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", blocker)

	if _, _, err := defaultWriteTemp("content"); err == nil {
		t.Error("writing into an unusable temp dir reported success")
	}
}

// Large candidates must round-trip intact — a truncated write would have the
// oracle verify something the model did not produce.
func TestDefaultWriteTemp_LargeCandidateRoundTrips(t *testing.T) {
	content := strings.Repeat("some candidate line\n", 20000)

	path, cleanup, err := defaultWriteTemp(content)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("wrote %d bytes, read back %d — the oracle would verify "+
			"something other than the candidate", len(content), len(got))
	}
}

// An empty candidate is still a candidate: the file must exist and be empty,
// not be skipped.
func TestDefaultWriteTemp_EmptyCandidateStillMaterializes(t *testing.T) {
	path, cleanup, err := defaultWriteTemp("")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("an empty candidate produced no file: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("empty candidate wrote %d bytes", info.Size())
	}
}
