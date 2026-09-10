// SPDX-License-Identifier: MIT

package shellpath

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/testutil"
)

// fakeShell puts a shell the test controls in $SHELL, so what loginPath does
// with it is asserted the same way on all three platforms.
//
// The assertion these replace ran the developer's real interactive login shell
// and was guarded on $SHELL being set, so it checked nothing wherever it was
// not set, and where it was, it depended on that shell starting inside
// loginPathTimeout. Under a full-suite run it sometimes did not, and the
// timeout then read as the very bug the test was looking for (#816). Setting
// SHELL here also means whatever a CI runner image happens to export cannot
// make the check vacuous again.
func fakeShell(t *testing.T, prints string) *testutil.Sandbox {
	t.Helper()
	sb := testutil.NewSandbox(t)
	t.Setenv("SHELL", sb.FakeBinary(t, "fakeshell", testutil.EchoScript(prints)))
	return sb
}

// script returns a shell body for this platform, since a .bat is not a .sh.
func script(unix, batch string) string {
	if runtime.GOOS == "windows" {
		return "@echo off\r\n" + batch + "\r\n"
	}
	return "#!/bin/sh\n" + unix + "\n"
}

func TestLoginPath_ReadsWhatTheShellPrints(t *testing.T) {
	want := sep("/opt/homebrew/bin", "/usr/bin")
	fakeShell(t, want)

	if got := loginPath(context.Background()); got != want {
		t.Errorf("loginPath = %q, want %q", got, want)
	}
}

// The invocation is the load-bearing detail: a plain login shell does not read
// .zshrc, which is where a great many people set PATH, so it has to be
// interactive as well. A fake shell can assert that. A real one cannot, it can
// only be observed to have answered.
func TestLoginPath_AsksForAnInteractiveLoginShell(t *testing.T) {
	sb := testutil.NewSandbox(t)
	argv := filepath.Join(t.TempDir(), "argv.txt")

	t.Setenv("SHELL", sb.FakeBinary(t, "argvshell", script(
		`printf '%s\n' "$@" > `+argv,
		`echo %* > `+argv,
	)))

	loginPath(context.Background())

	raw, err := os.ReadFile(argv)
	if err != nil {
		t.Fatalf("the shell was never invoked: %v", err)
	}
	got := string(raw)
	if !strings.Contains(got, "-ilc") {
		t.Errorf("argv %q does not ask for an interactive login shell", got)
	}
	if !strings.Contains(got, "PATH") {
		t.Errorf("argv %q does not ask the shell for its PATH", got)
	}
}

// Three ways a shell can fail to answer. All three must leave PATH to the
// caller rather than truncating it, which is what mergePath("") encodes.
func TestLoginPath_SaysNothingRatherThanGuessing(t *testing.T) {
	t.Run("no SHELL set", func(t *testing.T) {
		testutil.NewSandbox(t)
		t.Setenv("SHELL", "")
		if got := loginPath(context.Background()); got != "" {
			t.Errorf("loginPath = %q with no shell to ask", got)
		}
	})

	t.Run("the shell exits non-zero", func(t *testing.T) {
		sb := testutil.NewSandbox(t)
		t.Setenv("SHELL", sb.FakeBinary(t, "brokenshell", script("exit 3", "exit /b 3")))
		if got := loginPath(context.Background()); got != "" {
			t.Errorf("loginPath = %q from a shell that exited non-zero", got)
		}
	})

	// The real #816 failure, made deterministic: the shell outlives the
	// deadline. Adopt must carry on with the PATH it has.
	t.Run("the shell never answers", func(t *testing.T) {
		sb := testutil.NewSandbox(t)
		t.Setenv("SHELL", sb.FakeBinary(t, "hangingshell", script(
			"sleep 30", "ping -n 30 127.0.0.1 > nul")))

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		start := time.Now()
		if got := loginPath(ctx); got != "" {
			t.Errorf("loginPath = %q past its deadline", got)
		}
		// Bounded well below the shell's 30s sleep and well above the 100ms
		// deadline, so it separates "the context was honoured" from "the sleep
		// ran to completion" without racing a fork on a loaded machine.
		if elapsed := time.Since(start); elapsed > 10*time.Second {
			t.Errorf("waited %v: the deadline was not honoured", elapsed)
		}
	})
}
