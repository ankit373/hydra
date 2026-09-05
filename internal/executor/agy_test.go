// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

// The agy executor used to mutate a file the user owns: swap the model into
// agy's own settings.json, run, swap it back, serialized end to end because
// two concurrent swaps corrupt the shared file. It now passes the model via
// agy's own --model flag (confirmed against the real CLI, #522), so Execute
// never touches settings.json for model selection and concurrent calls run
// their subprocess work in parallel. recoverAgySwap remains only to clean up
// a sentinel a pre-#522 binary could have left behind mid-swap.

// fakeAgy plants an `agy` on the sandbox's PATH with the given behaviour.
func fakeAgy(t *testing.T, s *testutil.Sandbox, stdout, stderr string, exitCode int) {
	t.Helper()
	var body string
	if runtime.GOOS == "windows" {
		body = "@echo off\r\n"
		if stdout != "" {
			for _, line := range strings.Split(stdout, "\n") {
				body += "echo " + line + "\r\n"
			}
		}
		if stderr != "" {
			for _, line := range strings.Split(stderr, "\n") {
				body += "echo " + line + " 1>&2\r\n"
			}
		}
		body += "exit /b " + itoa(exitCode) + "\r\n"
	} else {
		body = "#!/bin/sh\n"
		if stdout != "" {
			body += "printf '%s\\n' " + shellQuote(stdout) + "\n"
		}
		if stderr != "" {
			body += "printf '%s\\n' " + shellQuote(stderr) + " >&2\n"
		}
		body += "exit " + itoa(exitCode) + "\n"
	}
	s.FakeBinary(t, "agy", body)
}

// fakeAgyCapturingArgs plants an `agy` that appends its argv (one per line) to
// argsFile before printing stdout, so a test can assert exactly what Execute
// invoked it with, e.g. that --model carries the right flag.
func fakeAgyCapturingArgs(t *testing.T, s *testutil.Sandbox, argsFile, stdout string, exitCode int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("no portable arg-dumping .bat")
	}
	body := "#!/bin/sh\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\" >> " + shellQuote(argsFile) + "; done\n"
	if stdout != "" {
		body += "printf '%s\\n' " + shellQuote(stdout) + "\n"
	}
	body += "exit " + itoa(exitCode) + "\n"
	s.FakeBinary(t, "agy", body)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	return string(rune('0' + n))
}

// writeAgySettings plants agy's settings.json under the sandbox home.
func writeAgySettings(t *testing.T, contents map[string]any) string {
	t.Helper()
	path := agySitesPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(contents, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readAgySettings(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("settings.json is not valid JSON after the run: %v\n%s", err, raw)
	}
	return m
}

func agyHead(modelFlag string) provider.Head {
	return provider.Head{
		ID: "agy-tier-3", Name: "agy-tier-3", Provider: "antigravity", Source: "registry",
		Meta: map[string]string{"model_flag": modelFlag, "token_pool": "gemini"},
	}
}

// Model selection goes through agy's own --model flag (confirmed against the
// real CLI, #522) rather than a settings.json swap, so a live user config,
// including a model they picked themselves, must survive a dispatch call
// completely untouched.
func TestAgyExecute_PassesModelViaFlagAndNeverTouchesSettings(t *testing.T) {
	s := testutil.NewSandbox(t)
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	fakeAgyCapturingArgs(t, s, argsFile, "the answer", 0)

	path := writeAgySettings(t, map[string]any{
		"model":     "gemini-3-pro",
		"theme":     "dark", // a setting Hydra does not own
		"telemetry": false,
	})
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := (&AgyExecutor{}).Execute(context.Background(), Request{
		Prompt: "hello", Head: agyHead("claude-sonnet-4.5"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Output != "the answer" {
		t.Errorf("Output = %q", resp.Output)
	}

	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	found := false
	for i, a := range args {
		if a == "--model" && i+1 < len(args) && args[i+1] == "claude-sonnet-4.5" {
			found = true
		}
	}
	if !found {
		t.Errorf("agy invoked with args %v, want --model claude-sonnet-4.5 among them", args)
	}

	// settings.json must be byte-identical, no swap, no restore, nothing.
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("settings.json changed:\nbefore: %s\nafter:  %s", before, after)
	}
	if _, err := os.Stat(path + agyOrigSuffix); err == nil {
		t.Error("a sentinel was written even though nothing was swapped")
	}
	if _, err := os.Stat(path + ".hydra-tmp"); err == nil {
		t.Error("a temp file was written even though nothing was swapped")
	}
}

// agy does not report token usage, so Hydra estimates from character count.
// That estimate must be labelled, or the budget governor and the cost report
// present a guess as measured spend.
func TestAgyExecute_TokensAreLabelledAsEstimates(t *testing.T) {
	s := testutil.NewSandbox(t)
	fakeAgy(t, s, "0123456789012345678901234567890123456789", "", 0)
	writeAgySettings(t, map[string]any{"model": "x"})

	resp, err := (&AgyExecutor{}).Execute(context.Background(), Request{
		Prompt: strings.Repeat("a", 400), Head: agyHead("m"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.TokensEstimated {
		t.Error("TokensEstimated = false; agy reports no usage, so these are char/4 " +
			"guesses and must never be booked as measured spend")
	}
	if resp.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 400/4", resp.InputTokens)
	}
	if resp.OutputTokens != 10 {
		t.Errorf("OutputTokens = %d, want 40/4", resp.OutputTokens)
	}
}

// The auth check reads stderr and the first three lines of stdout only. It must
// not scan the whole answer: a model asked about authentication will happily
// write "please log in" in its response, and treating that as an auth failure
// would discard a paid answer and send the user to a login page.
func TestAgyExecute_AuthPhraseInTheAnswerIsNotAnAuthFailure(t *testing.T) {
	s := testutil.NewSandbox(t)
	answer := "Here is the code:\nline two\nline three\nTo fix it, tell the user: please log in"
	fakeAgy(t, s, answer, "", 0)
	writeAgySettings(t, map[string]any{"model": "x"})

	resp, err := (&AgyExecutor{}).Execute(context.Background(), Request{
		Prompt: "how do I prompt for login?", Head: agyHead("m"),
	})
	if err != nil {
		t.Fatalf("an answer that mentions logging in was treated as an auth failure: %v", err)
	}
	if !strings.Contains(resp.Output, "please log in") {
		t.Errorf("Output = %q, want the full answer", resp.Output)
	}
	if _, err := os.Stat(filepath.Join(config.Dir(), "logs", "auth_required.json")); err == nil {
		t.Error("auth_required.json was written for a successful answer")
	}
}

// A real auth failure must be typed, carry the pool and the URL, and be
// recorded for the TUI to surface.
func TestAgyExecute_RealAuthFailureIsTypedAndRecorded(t *testing.T) {
	s := testutil.NewSandbox(t)
	fakeAgy(t, s, "", "Not authenticated. Please sign in at https://antigravity.google/auth to continue.", 1)
	writeAgySettings(t, map[string]any{"model": "gemini-3-pro"})

	_, err := (&AgyExecutor{}).Execute(context.Background(), Request{
		Prompt: "hi", Head: agyHead("claude-sonnet-4.5"),
	})
	if err == nil {
		t.Fatal("an auth failure was reported as success")
	}
	authErr, ok := err.(*AuthRequiredError)
	if !ok {
		t.Fatalf("error is %T, want *AuthRequiredError, dispatch keys its fallback "+
			"on the type, not on the message", err)
	}
	if authErr.Pool != "gemini" || authErr.ModelFlag != "claude-sonnet-4.5" {
		t.Errorf("AuthRequiredError = %+v, want the pool and model that failed", authErr)
	}
	if !strings.Contains(authErr.AuthURL, "antigravity.google/auth") {
		t.Errorf("AuthURL = %q, want the URL from agy's own message", authErr.AuthURL)
	}
	if msg := authErr.Error(); !strings.Contains(msg, "gemini") || !strings.Contains(msg, authErr.AuthURL) {
		t.Errorf("Error() = %q, want it to name the pool and where to authenticate", msg)
	}

	raw, err := os.ReadFile(filepath.Join(config.Dir(), "logs", "auth_required.json"))
	if err != nil {
		t.Fatalf("no auth_required.json was written for the TUI to surface: %v", err)
	}
	var entry map[string]any
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatal(err)
	}
	if entry["pool"] != "gemini" || entry["model"] != "claude-sonnet-4.5" {
		t.Errorf("auth_required.json = %v", entry)
	}
	if entry["detected_at"] == "" {
		t.Error("auth_required.json carries no timestamp, so a stale one cannot be aged out")
	}
}

// An error with no auth signal must stay an ordinary failure carrying stderr,
// so dispatch falls through to the next head rather than stopping to ask the
// user to log in.
func TestAgyExecute_OrdinaryFailureIsNotAnAuthError(t *testing.T) {
	s := testutil.NewSandbox(t)
	fakeAgy(t, s, "", "model overloaded, try again", 1)
	writeAgySettings(t, map[string]any{"model": "x"})

	_, err := (&AgyExecutor{}).Execute(context.Background(), Request{Prompt: "hi", Head: agyHead("m")})
	if err == nil {
		t.Fatal("a failing agy was reported as success")
	}
	if _, ok := err.(*AuthRequiredError); ok {
		t.Fatalf("an ordinary failure was classified as an auth failure: %v", err)
	}
	if !strings.Contains(err.Error(), "model overloaded") {
		t.Errorf("error = %v, want agy's own stderr", err)
	}
}

// A head with no model_flag cannot be routed to agy at all, swapping in an
// empty model would run whatever agy happened to be set to.
func TestAgyExecute_MissingModelFlagIsRefused(t *testing.T) {
	testutil.NewSandbox(t)

	_, err := (&AgyExecutor{}).Execute(context.Background(), Request{
		Prompt: "hi",
		Head:   provider.Head{ID: "agy-tier-1", Provider: "antigravity", Source: "registry"},
	})
	if err == nil {
		t.Fatal("a head with no model_flag was executed")
	}
	if !strings.Contains(err.Error(), "model_flag") {
		t.Errorf("error = %v, want it to name the missing field", err)
	}
}

// With no settings.json at all, agy still runs, it just cannot be pinned to a
// model. A user who has never opened agy must not get a hard failure.
func TestAgyExecute_NoSettingsFileStillRuns(t *testing.T) {
	s := testutil.NewSandbox(t)
	fakeAgy(t, s, "ran anyway", "", 0)

	resp, err := (&AgyExecutor{}).Execute(context.Background(), Request{Prompt: "hi", Head: agyHead("m")})
	if err != nil {
		t.Fatalf("agy refused to run without a settings.json: %v", err)
	}
	if resp.Output != "ran anyway" {
		t.Errorf("Output = %q", resp.Output)
	}
}

// recoverAgySwap cleans up a stale sentinel a pre-#522 Hydra binary could
// leave behind if SIGKILLed mid its old settings.json swap. Execute no longer
// performs that swap (model selection goes through --model instead), so the
// "killed mid-swap" state is constructed by hand rather than via swapAgyModel.
func TestRecoverAgySwap_UndoesAKilledSwap(t *testing.T) {
	testutil.NewSandbox(t)

	path := writeAgySettings(t, map[string]any{"model": "hydras-model", "theme": "dark"})
	sentinel := `{"model":"the-users-model","theme":"dark"}`
	if err := os.WriteFile(path+agyOrigSuffix, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}

	recoverAgySwap(path)

	after := readAgySettings(t, path)
	if after["model"] != "the-users-model" {
		t.Errorf("model = %v after recovery, want the user's own restored", after["model"])
	}
	if after["theme"] != "dark" {
		t.Errorf("recovery lost the user's other settings: %+v", after)
	}
	if _, err := os.Stat(path + agyOrigSuffix); err == nil {
		t.Error("the sentinel survived recovery, so the next run would recover again")
	}

	// With no sentinel, recovery is a no-op rather than a truncation.
	before := readAgySettings(t, path)
	recoverAgySwap(path)
	if got := readAgySettings(t, path); got["model"] != before["model"] {
		t.Error("recoverAgySwap changed settings.json with no sentinel present")
	}
}

// Settings.json is no longer touched for model selection, so 8 concurrent
// calls (each a different model) must leave it byte-for-byte untouched.
func TestAgyExecute_ConcurrentRunsDoNotTouchSettings(t *testing.T) {
	s := testutil.NewSandbox(t)
	fakeAgy(t, s, "ok", "", 0)
	path := writeAgySettings(t, map[string]any{"model": "the-users-model", "theme": "dark"})
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = (&AgyExecutor{}).Execute(context.Background(), Request{
				Prompt: "hi", Head: agyHead("model-" + itoa(i)),
			})
		}(i)
	}
	wg.Wait()

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("settings.json changed after 8 concurrent runs:\nbefore: %s\nafter:  %s", before, after)
	}
	if _, err := os.Stat(path + agyOrigSuffix); err == nil {
		t.Error("a sentinel was written even though model selection uses --model now")
	}
}

// The #522 audit's own harness: 3 concurrent calls to a fake agy sleeping
// 500ms must complete in close to one call's duration, not 3x it. A lock held
// across cmd.Run() (the bug this fix removes) would serialize them and this
// would take ~1.5s; narrowing the lock to the settings recovery step alone
// lets them run in parallel.
func TestAgyExecute_ConcurrentCallsRunInParallel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no portable sleeping .bat")
	}
	s := testutil.NewSandbox(t)
	// Absolute path: the sandbox's $PATH is scrubbed to just BinDir, so a bare
	// "sleep" resolves to nothing and the script races ahead instead of
	// actually sleeping (matches TestAgyExecute_HonoursTheTimeout's own /bin/sleep).
	s.FakeBinary(t, "agy", "#!/bin/sh\n/bin/sleep 0.5\necho ok\n")

	const n = 3
	runOnce := func() (wall time.Duration, durations []time.Duration) {
		durations = make([]time.Duration, n)
		var wg sync.WaitGroup
		start := time.Now()
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				callStart := time.Now()
				_, _ = (&AgyExecutor{}).Execute(context.Background(), Request{
					Prompt: "hi", Head: agyHead("model-" + itoa(i)),
				})
				durations[i] = time.Since(callStart)
			}(i)
		}
		wg.Wait()
		return time.Since(start), durations
	}

	// A real sleep(0.5) syscall can't finish early, so full serialization has a
	// hard floor of n*500ms=1.5s regardless of machine speed, genuine
	// parallelism stays close to one call's duration instead. 1.2s sits well
	// below that floor. Up to 3 attempts absorb a one-off host scheduling
	// stall (which looks identical to a serializing lock in a single sample);
	// if the lock actually serializes cmd.Run(), no attempt gets close.
	const budget = 1200 * time.Millisecond
	var best time.Duration = time.Hour
	for attempt := 0; attempt < 3; attempt++ {
		wall, durations := runOnce()
		for i, d := range durations {
			t.Logf("attempt %d, call %d: dur=%v", attempt, i, d)
		}
		t.Logf("attempt %d: TOTAL WALL TIME for %d concurrent calls: %v", attempt, n, wall)
		if wall < best {
			best = wall
		}
		if wall <= budget {
			return
		}
	}
	t.Errorf("best of 3 attempts: wall=%v for %d concurrent 500ms calls, want at least "+
		"one attempt well under the n*500ms=1.5s serialized floor, the lock is "+
		"serializing cmd.Run() again", best, n)
}

// AGY_TIMEOUT accepts a Go duration or a bare integer meaning seconds; the
// second form is what a user types.
func TestAgyExecute_HonoursTheTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no portable sleeping .bat")
	}
	s := testutil.NewSandbox(t)
	s.FakeBinary(t, "agy", "#!/bin/sh\nexec /bin/sleep 30\n")
	writeAgySettings(t, map[string]any{"model": "x"})

	for _, timeout := range []string{"1s", "1"} {
		t.Run("AGY_TIMEOUT="+timeout, func(t *testing.T) {
			t.Setenv("AGY_TIMEOUT", timeout)

			start := time.Now()
			_, err := (&AgyExecutor{}).Execute(context.Background(), Request{
				Prompt: "hi", Head: agyHead("m"),
			})
			elapsed := time.Since(start)

			if err == nil {
				t.Fatal("a timed-out run reported success")
			}
			if elapsed > 10*time.Second {
				t.Errorf("took %v with AGY_TIMEOUT=%s, the timeout was not applied "+
					"(a bare integer means seconds)", elapsed, timeout)
			}
		})
	}
}

// An unparsable AGY_TIMEOUT must fall back to the default rather than to zero,
// which would cancel every run immediately.
func TestAgyExecute_UnparsableTimeoutFallsBackToTheDefault(t *testing.T) {
	s := testutil.NewSandbox(t)
	fakeAgy(t, s, "ok", "", 0)
	writeAgySettings(t, map[string]any{"model": "x"})
	t.Setenv("AGY_TIMEOUT", "not-a-duration")

	if _, err := (&AgyExecutor{}).Execute(context.Background(), Request{
		Prompt: "hi", Head: agyHead("m"),
	}); err != nil {
		t.Fatalf("a garbage AGY_TIMEOUT cancelled the run: %v", err)
	}
}
