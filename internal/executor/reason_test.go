// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

// failingCLI plants a CLI head that writes a reason with a
// key in it, then a second line to stderr, and exits 3 (#1943).
func failingCLI(t *testing.T, key string) provider.Head {
	t.Helper()
	s := testutil.NewSandbox(t)
	body := "#!/bin/sh\necho '' >&2\necho '  Error: not inside a trusted directory, key " + key +
		"' >&2\necho 'see the docs' >&2\nexit 3\n"
	if runtime.GOOS == "windows" {
		body = "@echo off\r\necho Error: not inside a trusted directory, key " + key +
			" 1>&2\r\necho see the docs 1>&2\r\nexit /b 3\r\n"
	}
	return provider.Head{ID: "codex", Name: "codex", Provider: "openai", Source: "cli",
		Executable: s.FakeBinary(t, "codex", body)}
}

func TestReason_CLIFailureIsItsFirstStderrLineRedacted(t *testing.T) {
	key := "sk-" + strings.Repeat("Z", 32)
	head := failingCLI(t, key)
	runs := map[string]func() error{
		"execute": func() error {
			_, err := (&CLIExecutor{}).Execute(context.Background(), Request{Prompt: "hi", Head: head})
			return err
		},
		"stream": func() error {
			_, err := (&CLIExecutor{}).ExecuteStream(context.Background(), Request{Prompt: "hi", Head: head}, nil)
			return err
		},
	}
	for name, run := range runs {
		err := run()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("%s: err = %v, want it to wrap the exit error", name, err)
		}
		if !strings.Contains(err.Error(), "see the docs") || !strings.Contains(err.Error(), key) {
			t.Errorf("%s: Error() = %q, want all of stderr kept for the run log", name, err)
		}
		want := "Error: not inside a trusted directory, key [REDACTED:openai key]"
		if got := Reason(err); got != want {
			t.Errorf("%s: Reason = %q, want %q", name, got, want)
		}
	}
}

func TestReason_FallsBackAndCaps(t *testing.T) {
	quiet := &CLIError{Head: "codex", Err: errors.New("exit status 1"), Stderr: "\n --- \n"}
	if got := Reason(quiet); got != "exit status 1" {
		t.Errorf("Reason with no stderr words = %q, want the exit status", got)
	}
	blanks := &CLIError{Head: "codex", Err: errors.New("exit status 1"), Stderr: "\n  \n--\nreal reason\nnext\n"}
	if got := Reason(blanks); got != "real reason" {
		t.Errorf("Reason = %q, want the first line with a letter or digit", got)
	}
	long := errors.New(strings.Repeat("x", 300) + "\nsecond")
	if got := Reason(long); len([]rune(got)) != reasonRunes+1 || !strings.HasSuffix(got, "…") {
		t.Errorf("Reason = %d runes, want %d and an ellipsis", len([]rune(got)), reasonRunes+1)
	}
}
