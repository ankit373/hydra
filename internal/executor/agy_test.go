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

// The agy executor is the only one that mutates a file the user owns: it swaps
// the model into agy's own settings.json, runs, and swaps it back. If it does
// not swap back, the user's editor is left pointing at whatever model Hydra
// last routed to — a visible, confusing change they did not make.

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

func TestAgyExecute_SwapsTheModelInAndBackOut(t *testing.T) {
	s := testutil.NewSandbox(t)
	fakeAgy(t, s, "the answer", "", 0)

	path := writeAgySettings(t, map[string]any{
		"model":     "gemini-3-pro",
		"theme":     "dark", // a setting Hydra does not own
		"telemetry": false,
	})

	resp, err := (&AgyExecutor{}).Execute(context.Background(), Request{
		Prompt: "hello", Head: agyHead("claude-sonnet-4.5"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Output != "the answer" {
		t.Errorf("Output = %q", resp.Output)
	}

	after := readAgySettings(t, path)
	if after["model"] != "gemini-3-pro" {
		t.Errorf("model = %v after the run, want the user's own %q restored",
			after["model"], "gemini-3-pro")
	}
	// Everything else must survive the round trip untouched.
	if after["theme"] != "dark" || after["telemetry"] != false {
		t.Errorf("settings.json lost the user's other keys: %+v", after)
	}
	// The sentinel backup must not be left behind — a stale one makes the next
	// run "recover" to an older state.
	if _, err := os.Stat(path + agyOrigSuffix); err == nil {
		t.Error("the sentinel backup survived a clean run")
	}
	if _, err := os.Stat(path + ".hydra-tmp"); err == nil {
		t.Error("a temp file survived a clean run")
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
		t.Fatalf("error is %T, want *AuthRequiredError — dispatch keys its fallback "+
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

// A head with no model_flag cannot be routed to agy at all — swapping in an
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

// With no settings.json at all, agy still runs — it just cannot be pinned to a
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

// swapAgyModel writes a sentinel before it mutates anything, so a SIGKILL
// between the swap and the restore is recoverable. Without it the user's agy is
// left pinned to Hydra's last model with nothing recording what it was.
func TestRecoverAgySwap_UndoesAKilledSwap(t *testing.T) {
	testutil.NewSandbox(t)

	path := writeAgySettings(t, map[string]any{"model": "the-users-model", "theme": "dark"})

	original, err := swapAgyModel(path, "hydras-model")
	if err != nil {
		t.Fatal(err)
	}
	if original != "the-users-model" {
		t.Errorf("swapAgyModel returned %q as the original", original)
	}
	if got := readAgySettings(t, path)["model"]; got != "hydras-model" {
		t.Fatalf("model = %v after the swap, want Hydra's", got)
	}
	// Simulate SIGKILL: the deferred restore never runs.
	if _, err := os.Stat(path + agyOrigSuffix); err != nil {
		t.Fatalf("no sentinel was written before the mutation: %v", err)
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

// A settings.json with no model key means the user never pinned one. Restoring
// must remove the key again rather than writing an empty string, which agy
// would read as a model named "".
func TestRestoreAgyModel_RemovesTheKeyWhenThereWasNone(t *testing.T) {
	testutil.NewSandbox(t)

	path := writeAgySettings(t, map[string]any{"theme": "dark"})

	original, err := swapAgyModel(path, "hydras-model")
	if err != nil {
		t.Fatal(err)
	}
	if original != "" {
		t.Errorf("original = %q, want empty — there was no model key", original)
	}

	restoreAgyModel(path, original)

	after := readAgySettings(t, path)
	if _, present := after["model"]; present {
		t.Errorf("model key = %v after restore, want it absent as it was before",
			after["model"])
	}
}

// A settings.json that is not JSON must not be overwritten. Hydra cannot know
// what it meant, and replacing it destroys the user's config.
func TestSwapAgyModel_RefusesToTouchUnparsableSettings(t *testing.T) {
	testutil.NewSandbox(t)

	path := agySitesPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	corrupt := []byte("{ this was hand-edited and is broken")
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := swapAgyModel(path, "m"); err == nil {
		t.Fatal("an unparsable settings.json was swapped anyway")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(corrupt) {
		t.Errorf("settings.json was modified: %q", raw)
	}
	// No sentinel may be left pointing at a file that was never swapped.
	if _, err := os.Stat(path + agyOrigSuffix); err == nil {
		t.Error("a sentinel was left behind after a refused swap; the next run would " +
			"\"recover\" a file that was never changed")
	}
}

func TestSwapAgyModel_MissingFileIsAnErrorNotASilentNoOp(t *testing.T) {
	testutil.NewSandbox(t)

	if _, err := swapAgyModel(filepath.Join(t.TempDir(), "absent.json"), "m"); err == nil {
		t.Error("swapping a settings.json that does not exist reported success")
	}
}

// agy shares one settings.json, so concurrent executions must be serialized.
// Without the mutex a swarm run corrupts it and every head runs the wrong model.
func TestAgyExecute_ConcurrentRunsLeaveSettingsIntact(t *testing.T) {
	s := testutil.NewSandbox(t)
	fakeAgy(t, s, "ok", "", 0)
	path := writeAgySettings(t, map[string]any{"model": "the-users-model", "theme": "dark"})

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

	after := readAgySettings(t, path)
	if after["model"] != "the-users-model" {
		t.Errorf("model = %v after 8 concurrent runs, want the user's own restored",
			after["model"])
	}
	if after["theme"] != "dark" {
		t.Errorf("concurrent runs corrupted settings.json: %+v", after)
	}
	if _, err := os.Stat(path + agyOrigSuffix); err == nil {
		t.Error("a sentinel survived concurrent runs")
	}
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
				t.Errorf("took %v with AGY_TIMEOUT=%s — the timeout was not applied "+
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
