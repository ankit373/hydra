// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

// agy is the one head that could not simply tee its stdout: the auth check
// reads stderr, which is unreadable until the process exits, so forwarding as
// it arrives would render an interactive sign-in prompt as the answer (#791).

// collect streams a request and returns every delta it emitted, in order.
func collect(t *testing.T, req Request) ([]string, *Response, error) {
	t.Helper()
	var deltas []string
	resp, err := (&AgyExecutor{}).ExecuteStream(context.Background(), req,
		func(d string) { deltas = append(deltas, d) })
	return deltas, resp, err
}

// The whole point. An auth prompt must never reach the user as content.
func TestAgyStream_AnAuthPromptEmitsNoDeltas(t *testing.T) {
	s := testutil.NewSandbox(t)
	fakeAgy(t, s, "Please sign in to continue\nhttps://antigravity.google/auth", "", 1)

	deltas, resp, err := collect(t, Request{Prompt: "hi", Head: agyHead("gemini-3-pro")})
	var authErr *AuthRequiredError
	if !errors.As(err, &authErr) {
		t.Fatalf("err = %v (%T), want *AuthRequiredError", err, err)
	}
	if len(deltas) != 0 {
		t.Errorf("emitted %d delta(s) for an auth prompt: %q", len(deltas), deltas)
	}
	if resp != nil {
		t.Errorf("Response = %+v on an auth failure, want nil", resp)
	}
	if authErr.AuthURL == "" {
		t.Error("AuthURL is empty though the prompt carried one")
	}
}

// The signal on stderr is the case a tee cannot catch at all, because stderr is
// only readable once the process is done.
func TestAgyStream_AnAuthSignalOnStderrAlsoEmitsNothing(t *testing.T) {
	s := testutil.NewSandbox(t)
	fakeAgy(t, s, "", "authentication required", 1)

	deltas, _, err := collect(t, Request{Prompt: "hi", Head: agyHead("gemini-3-pro")})
	var authErr *AuthRequiredError
	if !errors.As(err, &authErr) {
		t.Fatalf("err = %v, want *AuthRequiredError", err)
	}
	if len(deltas) != 0 {
		t.Errorf("emitted %d delta(s): %q", len(deltas), deltas)
	}
}

// Streaming must not change the answer. The deltas reassemble to exactly the
// Output that Execute would have produced for the same run.
func TestAgyStream_DeltasReassembleToWhatExecuteReturns(t *testing.T) {
	s := testutil.NewSandbox(t)
	const answer = "line one\nline two\nline three\nline four\nline five"
	fakeAgy(t, s, answer, "", 0)
	req := Request{Prompt: "hi", Head: agyHead("gemini-3-pro")}

	buffered, err := (&AgyExecutor{}).Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	deltas, streamed, err := collect(t, req)
	if err != nil {
		t.Fatal(err)
	}
	if streamed.Output != buffered.Output {
		t.Errorf("streamed Output = %q, buffered = %q", streamed.Output, buffered.Output)
	}
	if got := strings.TrimSpace(strings.Join(deltas, "")); got != buffered.Output {
		t.Errorf("deltas reassemble to %q, want the buffered Output %q", got, buffered.Output)
	}
}

// The held-back prefix is released, not dropped. A run whose first three lines
// are the answer must still deliver them.
func TestAgyStream_TheHeldBackPrefixIsReleased(t *testing.T) {
	s := testutil.NewSandbox(t)
	fakeAgy(t, s, "alpha\nbeta\ngamma\ndelta", "", 0)

	deltas, _, err := collect(t, Request{Prompt: "hi", Head: agyHead("gemini-3-pro")})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(deltas, "")
	for _, want := range []string{"alpha", "beta", "gamma", "delta"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%q never reached the consumer; the hold-back dropped it instead of "+
				"releasing it. got %q", want, joined)
		}
	}
}

// A short answer never reaches three lines, so the gate can only settle at
// exit. It must still be delivered rather than held forever.
func TestAgyStream_AShortAnswerIsStillDelivered(t *testing.T) {
	s := testutil.NewSandbox(t)
	fakeAgy(t, s, "42", "", 0)

	deltas, resp, err := collect(t, Request{Prompt: "hi", Head: agyHead("gemini-3-pro")})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Output != "42" {
		t.Errorf("Output = %q, want 42", resp.Output)
	}
	if strings.TrimSpace(strings.Join(deltas, "")) != "42" {
		t.Errorf("deltas = %q, want the answer released at exit", deltas)
	}
}

// A model whose first three lines say "sign in to continue" is read as an auth
// failure. That is a pre-existing false positive in the detector, not something
// streaming introduced, and the point of pinning it is **parity**: the streamed
// and buffered paths must be wrong in the same direction, or a head answers one
// way through the CLI and another through the app.
//
// Widening the detector is a separate change with its own risk, since the fix
// for a false positive is usually a false negative, and a missed auth prompt is
// the thing rendered to the user as the answer.
func TestAgyStream_MatchesExecuteOnAPrefixFalsePositive(t *testing.T) {
	s := testutil.NewSandbox(t)
	const answer = "To fix the bug, tell the user to sign in to continue\nthen retry\nand log the result\nfourth line"
	fakeAgy(t, s, answer, "", 0)
	req := Request{Prompt: "hi", Head: agyHead("gemini-3-pro")}

	_, bufErr := (&AgyExecutor{}).Execute(context.Background(), req)
	deltas, _, streamErr := collect(t, req)

	var a, b *AuthRequiredError
	if errors.As(bufErr, &a) != errors.As(streamErr, &b) {
		t.Fatalf("Execute err = %v but ExecuteStream err = %v: the two paths disagree "+
			"about what needs a sign-in", bufErr, streamErr)
	}
	// And whatever the verdict, a suspected prompt is never rendered. Stated as
	// an implication rather than a skip, so this keeps meaning something if the
	// detector is ever widened and both paths start letting this through.
	if errors.As(streamErr, &b) && len(deltas) != 0 {
		t.Errorf("emitted %d delta(s) while reporting an auth failure: %q", len(deltas), deltas)
	}
}

// Stream() routes agy through ExecuteStream now rather than falling back to one
// whole-output delta, which is what the issue asked for.
func TestAgyStream_IsRecognisedAsStreaming(t *testing.T) {
	if !CanStream(&AgyExecutor{}) {
		t.Error("AgyExecutor does not implement StreamingExecutor, so a surface still " +
			"renders its answer as one block")
	}
}

// A non-auth failure keeps reporting as a failure, and the error still carries
// stderr so the reason is visible.
func TestAgyStream_ANonAuthFailureIsStillAFailure(t *testing.T) {
	s := testutil.NewSandbox(t)
	fakeAgy(t, s, "", "model overloaded", 3)

	_, _, err := collect(t, Request{Prompt: "hi", Head: agyHead("gemini-3-pro")})
	if err == nil {
		t.Fatal("a non-zero exit was reported as success")
	}
	var authErr *AuthRequiredError
	if errors.As(err, &authErr) {
		t.Errorf("err = %v, want a plain failure rather than an auth error", err)
	}
	if !strings.Contains(err.Error(), "model overloaded") {
		t.Errorf("err = %v, want it to carry stderr", err)
	}
}
