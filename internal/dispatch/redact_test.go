// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/testutil"
)

// Made-up values only.
const (
	redactEmail  = "sam.lee@example.com"
	redactPrompt = "reply to " + redactEmail + " about the refund"
)

func TestDispatch_RedactSendsPlaceholdersToRemote(t *testing.T) {
	testutil.NewSandbox(t)
	away, rec := recordingHead(t, "away", "done")

	if _, err := liveDispatcher(away).Dispatch(context.Background(), redactPrompt, Options{Redact: true}); err != nil {
		t.Fatal(err)
	}
	if got := rec.sent(); strings.Contains(got, redactEmail) || !strings.Contains(got, "<<EMAIL_1>>") {
		t.Errorf("the remote head was sent %q, want the email as <<EMAIL_1>>", got)
	}
}

// [policies.pii] action = "redact" turns it on without the flag.
func TestDispatch_RedactPolicyActionSendsPlaceholders(t *testing.T) {
	testutil.NewSandbox(t)
	away, rec := recordingHead(t, "away", "done")
	d := liveDispatcher(away)
	d.policy = policy.New(policy.DefaultRules(policy.PIIRedact))

	if _, err := d.Dispatch(context.Background(), redactPrompt, Options{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rec.sent(), redactEmail) {
		t.Error("the pii-redact rule fired but the remote head saw the email")
	}
}

func TestDispatch_RedactSendsOriginalToLocal(t *testing.T) {
	testutil.NewSandbox(t)
	home, rec := recordingHead(t, "home", "done")
	home.LocalOnly = true

	res, err := liveDispatcher(home).Dispatch(context.Background(), redactPrompt, Options{Redact: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.sent(); got != redactPrompt {
		t.Errorf("the local head was sent %q, want the original", got)
	}
	if res.Redacted != 0 {
		t.Errorf("Redacted = %d for a local head, want 0", res.Redacted)
	}
}

// Checks judge the answer the user will read, so the placeholder is gone by then.
func TestDispatch_RedactRestoresBeforeChecks(t *testing.T) {
	testutil.NewSandbox(t)
	away, _ := recordingHead(t, "away", "I wrote to <<EMAIL_1>> and cc'd <<EMAIL_9>>.")
	var checked string
	opts := Options{Redact: true, Escalate: func(_ context.Context, r *Result) (bool, string) {
		checked = r.Output
		return false, ""
	}}

	res, err := liveDispatcher(away).Dispatch(context.Background(), redactPrompt, opts)
	if err != nil {
		t.Fatal(err)
	}
	want := "I wrote to " + redactEmail + " and cc'd <<EMAIL_9>>."
	if checked != want || res.Output != want {
		t.Errorf("check saw %q, caller got %q, want %q", checked, res.Output, want)
	}
	if res.Redacted != 1 || len(res.Unrestored) != 1 || res.Unrestored[0] != "<<EMAIL_9>>" {
		t.Errorf("Redacted=%d Unrestored=%v, want 1 and [<<EMAIL_9>>]", res.Redacted, res.Unrestored)
	}
}

// A secret file still goes local, or is refused; redaction never relaxes that.
func TestDispatch_RedactKeepsSecretFilesLocal(t *testing.T) {
	testutil.NewSandbox(t)
	away, awayRec := recordingHead(t, "away", "done")
	home, _ := recordingHead(t, "home", "done")
	home.LocalOnly = true

	res, err := liveDispatcher(away, home).Dispatch(context.Background(), redactPrompt,
		Options{Redact: true, Resource: "deploy/.env"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Head.ID != "home" || awayRec.sent() != "" {
		t.Errorf("answered by %q; a .env resource must stay local with --redact", res.Head.ID)
	}
	if _, err := liveDispatcher(away).Dispatch(context.Background(), redactPrompt,
		Options{Redact: true, Resource: "deploy/.env"}); err == nil {
		t.Error("with nothing local, a .env resource was sent redacted instead of refused")
	}
}

func redactPair(t *testing.T, hostedAnswer string) (*Dispatcher, *promptRecorder) {
	t.Helper()
	testutil.NewSandbox(t)
	away, _ := recordingHead(t, "away", hostedAnswer)
	home, homeRec := recordingHead(t, "home", "answered locally")
	home.LocalOnly, home.CapScore = true, 40
	return liveDispatcher(away, home), homeRec
}

func TestDispatch_UnknownPlaceholderReroutesLocal(t *testing.T) {
	d, homeRec := redactPair(t, "Sent to <<EMAIL_7>>.")
	res, err := d.Dispatch(context.Background(), redactPrompt, Options{Redact: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Head.ID != "home" || homeRec.sent() != redactPrompt {
		t.Errorf("answered by %q, local head sent %q; want a local rerun on the original", res.Head.ID, homeRec.sent())
	}
	if !strings.Contains(res.RedactionMiss, "<<EMAIL_7>>") {
		t.Errorf("RedactionMiss = %q, want it to name the made-up placeholder", res.RedactionMiss)
	}
}

func TestDispatch_CannotSeeReroutesLocal(t *testing.T) {
	d, _ := redactPair(t, "I cannot see the domain of <<EMAIL_1>>, so I cannot say.")
	res, err := d.Dispatch(context.Background(), redactPrompt, Options{Redact: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Head.ID != "home" || !strings.Contains(res.RedactionMiss, "<<EMAIL_1>>") {
		t.Errorf("answered by %q with miss %q, want a local rerun naming <<EMAIL_1>>", res.Head.ID, res.RedactionMiss)
	}
}

// A plain answer that uses the placeholder is not a miss.
func TestDispatch_UsedPlaceholderStaysHosted(t *testing.T) {
	d, homeRec := redactPair(t, "Sent to <<EMAIL_1>>.")
	res, err := d.Dispatch(context.Background(), redactPrompt, Options{Redact: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Head.ID != "away" || res.RedactionMiss != "" || homeRec.sent() != "" {
		t.Errorf("answered by %q, miss %q: a usable answer was rerun", res.Head.ID, res.RedactionMiss)
	}
}

func TestDispatch_NoLocalHeadAddsNote(t *testing.T) {
	testutil.NewSandbox(t)
	away, _ := recordingHead(t, "away", "Sent to <<EMAIL_7>>.")
	res, err := liveDispatcher(away).Dispatch(context.Background(), redactPrompt, Options{Redact: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Head.ID != "away" || !strings.Contains(res.RedactionMiss, "<<EMAIL_7>>") {
		t.Errorf("answered by %q with miss %q, want the hosted answer and a note", res.Head.ID, res.RedactionMiss)
	}
}
