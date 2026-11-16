// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/entity"
	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

// The measurement pins decoding, or it is not a measurement: the same head
// scored 0.82 and 0.72 on consecutive runs before this (#1041).
func TestEntityRequest_PinsGreedyDecoding(t *testing.T) {
	req := entityRequest(provider.Head{ID: "ollama/x"}, 256, entity.System, "some text")
	if req.Temperature == nil || *req.Temperature != 0 {
		t.Fatal("the request does not pin temperature 0, so every run resamples")
	}
	if req.System != entity.System || req.MaxTokens != 256 {
		t.Errorf("system %q, max tokens %d, want entity.System and 256", req.System, req.MaxTokens)
	}
}

const namesPrompt = "Draft an offer letter for Aisha Bello, start date Monday."

// namesPair is a remote head that records what it was sent and a local one
// that answers the name pass; eligible saves a passing report for it.
func namesPair(t *testing.T, localAnswer string, eligible bool) (*Dispatcher, *promptRecorder) {
	t.Helper()
	testutil.NewSandbox(t)
	away, rec := recordingHead(t, "away", "Welcome aboard, <<PERSONAL_1>>.")
	home, _ := recordingHead(t, "home", localAnswer)
	home.LocalOnly, home.CapScore = true, 40
	if eligible {
		r := entity.Report{Positives: 200, Recalled: 196, Negatives: 200}
		if err := entity.SaveReport(NERReportPath(), "home", r, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	return liveDispatcher(away, home), rec
}

func TestNamePass_AddsSpansToVault(t *testing.T) {
	d, rec := namesPair(t, `["Aisha Bello"]`, true)
	res, err := d.Dispatch(context.Background(), namesPrompt, Options{Redact: true, RedactNames: true})
	if err != nil {
		t.Fatal(err)
	}
	if sent := rec.sent(); strings.Contains(sent, "Aisha") || !strings.Contains(sent, "<<PERSONAL_1>>") {
		t.Errorf("the hosted head was sent %q", sent)
	}
	if res.Output != "Welcome aboard, Aisha Bello." {
		t.Errorf("answer = %q, want the name restored", res.Output)
	}
}

func TestNamePass_SkippedWithoutEligibleHead(t *testing.T) {
	d, rec := namesPair(t, `["Aisha Bello"]`, false)
	if _, err := d.Dispatch(context.Background(), namesPrompt, Options{Redact: true, RedactNames: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.sent(), "Aisha Bello") {
		t.Error("an unmeasured local head was asked to mark names")
	}
	events, err := ledger.Load(ledger.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	var said string
	for _, e := range events {
		if e.Provenance != nil && e.Tool == "away" && e.Action == ledger.Exec {
			said = e.Provenance.NamePass
		}
	}
	if !strings.HasPrefix(said, "skipped") {
		t.Errorf("the ledger says name pass %q, want it to say it was skipped", said)
	}
}

func TestNamePass_FailureBlocksSend(t *testing.T) {
	d, rec := namesPair(t, "I think there is a name in there", true)
	if _, err := d.Dispatch(context.Background(), namesPrompt, Options{Redact: true, RedactNames: true}); err == nil {
		t.Fatal("an unreadable name pass let the request through")
	}
	if rec.sent() != "" {
		t.Error("the hosted head was asked after the name pass failed")
	}
}
