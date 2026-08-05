// SPDX-License-Identifier: MIT

package oracle

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ankit373/hydra/internal/trust"
)

func TestCommandOracle_PassFail(t *testing.T) {
	ctx := context.Background()

	pass := &CommandOracle{Template: "true"}
	if v, err := pass.Verify(ctx, "anything", trust.Task{}); err != nil || !v.Passed {
		t.Errorf("`true` should pass: v=%+v err=%v", v, err)
	}

	fail := &CommandOracle{Template: "false"}
	if v, err := fail.Verify(ctx, "anything", trust.Task{}); err != nil || v.Passed {
		t.Errorf("`false` should fail (not error): v=%+v err=%v", v, err)
	}
}

// {file} must be substituted with a real path holding the candidate content,
// without fragmenting on spaces.
func TestCommandOracle_FileSubstitution(t *testing.T) {
	ctx := context.Background()

	// `test -s {file}` passes iff the file is non-empty → candidate content lands there.
	o := &CommandOracle{Template: "test -s {file}"}
	if v, err := o.Verify(ctx, "some content", trust.Task{}); err != nil || !v.Passed {
		t.Errorf("non-empty candidate should make `test -s` pass: v=%+v err=%v", v, err)
	}
	if v, err := o.Verify(ctx, "", trust.Task{}); err != nil || v.Passed {
		t.Errorf("empty candidate should make `test -s` fail: v=%+v err=%v", v, err)
	}
}

func TestCommandOracle_FileContentIsCandidate(t *testing.T) {
	// Capture the temp path the oracle writes, and assert its contents.
	var seenPath string
	o := &CommandOracle{
		Template: "true {file}",
		writeTemp: func(content string) (string, func(), error) {
			p := filepath.Join(t.TempDir(), "cand.txt")
			if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
				return "", nil, err
			}
			seenPath = p
			return p, func() {}, nil
		},
	}
	if _, err := o.Verify(context.Background(), "hello oracle", trust.Task{}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(seenPath)
	if string(raw) != "hello oracle" {
		t.Errorf("temp file content = %q, want candidate", string(raw))
	}
}

func TestCommandOracle_LauncherErrorIsError(t *testing.T) {
	o := &CommandOracle{Template: "this-command-does-not-exist-hydra"}
	if _, err := o.Verify(context.Background(), "x", trust.Task{}); err == nil {
		t.Error("a missing verifier binary should return an error, not a fail verdict")
	}
}

// A calibrated verifier contributes a large-magnitude LLR — dominating a model.
func TestLLR_CalibratedVerifierDominates(t *testing.T) {
	cal, _ := trust.New("")
	// Train verifier:tests to near-perfect (se=sp≈0.99).
	for i := 0; i < 990; i++ {
		_ = cal.Update("verifier:tests", "go", true, trust.OutcomeCorrect)
		_ = cal.Update("verifier:tests", "go", false, trust.OutcomeIncorrect)
	}
	for i := 0; i < 10; i++ {
		_ = cal.Update("verifier:tests", "go", false, trust.OutcomeCorrect)
		_ = cal.Update("verifier:tests", "go", true, trust.OutcomeIncorrect)
	}
	// A middling model.
	for i := 0; i < 70; i++ {
		_ = cal.Update("model:x", "go", true, trust.OutcomeCorrect)
		_ = cal.Update("model:x", "go", false, trust.OutcomeIncorrect)
	}
	for i := 0; i < 30; i++ {
		_ = cal.Update("model:x", "go", false, trust.OutcomeCorrect)
		_ = cal.Update("model:x", "go", true, trust.OutcomeIncorrect)
	}

	pass := LLR(cal, "verifier:tests", "go", Verdict{Passed: true})
	fail := LLR(cal, "verifier:tests", "go", Verdict{Passed: false})
	modelPass := LLR(cal, "model:x", "go", Verdict{Passed: true})

	if pass <= 0 || fail >= 0 {
		t.Errorf("verifier pass LLR should be +, fail should be −: pass=%.3f fail=%.3f", pass, fail)
	}
	if pass <= modelPass {
		t.Errorf("calibrated verifier (%.3f) should dominate a middling model (%.3f)", pass, modelPass)
	}
}

// defaultWriteTemp materialises the candidate for a {file} oracle. Its failure
// paths matter because an oracle that cannot stage its input must report that,
// not return a verdict — a verdict drawn from nothing is confident false
// evidence, and an oracle's LLR outweighs several models' votes.
func TestDefaultWriteTemp(t *testing.T) {
	path, cleanup, err := defaultWriteTemp("the candidate answer")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the temp file was not written: %v", err)
	}
	if string(raw) != "the candidate answer" {
		t.Errorf("content = %q", raw)
	}
	cleanup()
	if _, err := os.Stat(path); err == nil {
		t.Error("cleanup left the temp file behind; every {file} verification " +
			"would leak one")
	}

	// An unwritable temp directory must be an error rather than a verdict.
	//
	// All three variables: os.TempDir reads $TMPDIR on unix and %TMP%/%TEMP% on
	// Windows. Setting only TMPDIR made this pass everywhere and assert nothing
	// on the Windows runner, where the write simply succeeded in the real temp
	// directory.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(v, blocker)
	}
	if p, c, err := defaultWriteTemp("x"); err == nil {
		if c != nil {
			c()
		}
		t.Errorf("defaultWriteTemp succeeded at %q with an unusable TMPDIR", p)
	}
}

// A {file} oracle must actually receive the candidate on disk, and the file
// must be gone afterwards.
func TestCommandOracle_FileTemplateStagesAndCleansUp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/test is POSIX; the staging contract is covered by " +
			"TestDefaultWriteTemp on every platform")
	}

	var staged string
	o := &CommandOracle{
		Template: "/bin/test -s {file}",
		Source:   "verifier:test",
		writeTemp: func(content string) (string, func(), error) {
			p, c, err := defaultWriteTemp(content)
			staged = p
			return p, c, err
		},
	}

	v, err := o.Verify(context.Background(), "some content", trust.Task{})
	if err != nil {
		t.Fatal(err)
	}
	if !v.Passed {
		t.Errorf("a non-empty staged file failed `test -s`: %+v", v)
	}
	if staged == "" {
		t.Fatal("nothing was staged")
	}
	if _, err := os.Stat(staged); err == nil {
		t.Error("the staged file survived the verification")
	}
}
