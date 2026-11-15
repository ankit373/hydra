// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/util"
)

// promptRecorder is an OpenAI-shaped head that answers answer and keeps the
// last user message it was sent.
type promptRecorder struct {
	mu     sync.Mutex
	prompt string
}

func (p *promptRecorder) sent() string { p.mu.Lock(); defer p.mu.Unlock(); return p.prompt }

func recordingHead(t *testing.T, id, answer string) (provider.Head, *promptRecorder) {
	t.Helper()
	rec := &promptRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Role, Content string }
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		rec.mu.Lock()
		for _, m := range body.Messages {
			if m.Role == "user" {
				rec.prompt = m.Content
			}
		}
		rec.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`, answer)
	}))
	t.Cleanup(srv.Close)
	return provider.Head{ID: id, Name: id, Provider: "openai", Source: "env", Endpoint: srv.URL, AuthReady: true, CapScore: 90}, rec
}

// rejectFirst escalates past the first answer only, with reason.
func rejectFirst(reason string) func(context.Context, *Result) (bool, string) {
	n := 0
	return func(context.Context, *Result) (bool, string) {
		n++
		return n == 1, reason
	}
}

func TestRetry_NextAttemptGetsFailedAnswerAndReason(t *testing.T) {
	testutil.NewSandbox(t)
	h1, _ := recordingHead(t, "h1", `{"total": null}`)
	h2, rec2 := recordingHead(t, "h2", `{"total": 12}`)
	d := climbingDispatcher(t, climbing{h1, 96, 0.01}, climbing{h2, 98, 0.02})
	const prompt = "extract the invoice total as JSON"
	const reason = "verifier:cmd:schema failed: total must be a number"

	res, err := d.Dispatch(context.Background(), prompt, Options{
		Domain: climbClass, Escalate: rejectFirst(reason), Retry: RetryPrompt,
	})
	if err != nil || res.Head.ID != "h2" {
		t.Fatalf("Dispatch = %v, %v; want h2 to answer after h1 was rejected", res, err)
	}
	if got, want := rec2.sent(), RetryPrompt(prompt, &Result{Output: `{"total": null}`}, reason); got != want {
		t.Fatalf("h2 was sent\n%s\nwant\n%s", got, want)
	}
}

func TestRetry_NilKeepsTheSamePrompt(t *testing.T) {
	testutil.NewSandbox(t)
	h1, rec1 := recordingHead(t, "h1", "first")
	h2, rec2 := recordingHead(t, "h2", "second")
	d := climbingDispatcher(t, climbing{h1, 96, 0.01}, climbing{h2, 98, 0.02})
	const prompt = "summarise the attached minutes"

	if _, err := d.Dispatch(context.Background(), prompt, Options{
		Domain: climbClass, Escalate: rejectFirst("ungrounded number"),
	}); err != nil {
		t.Fatal(err)
	}
	if rec1.sent() != prompt || rec2.sent() != prompt {
		t.Fatalf("with no Retry the heads were sent %q and %q, want %q both times", rec1.sent(), rec2.sent(), prompt)
	}
}

func TestRetryPrompt_FencesAndCapsTheFailedAnswer(t *testing.T) {
	long := strings.Repeat("a", 6000) + "MIDDLE" + strings.Repeat("z", 6000)
	got := RetryPrompt("translate this", &Result{Output: long}, "labels disagree")

	spans := util.Unwrap(got)
	if len(spans) != 2 || spans[0].Label != "REJECTED ANSWER" || spans[1].Label != "CHECK FAILURE" {
		t.Fatalf("fenced spans = %+v, want the rejected answer then the check failure", spans)
	}
	answer := spans[0].Content
	if len(answer) > retryAnswerCap+64 || strings.Contains(answer, "MIDDLE") {
		t.Errorf("rejected answer kept %d bytes and its middle; want it capped near %d", len(answer), retryAnswerCap)
	}
	if !strings.HasPrefix(answer, "aaaa") || !strings.HasSuffix(answer, "zzzz") || !strings.Contains(answer, "bytes cut") {
		t.Errorf("the cap should keep both ends and mark the cut")
	}
	if spans[1].Content != "labels disagree" || !strings.HasPrefix(got, "translate this\n\n") {
		t.Errorf("prompt or reason lost: %q", got[:40])
	}
}

func TestCapMiddle_CutsOnRuneBoundaries(t *testing.T) {
	got := capMiddle(strings.Repeat("é", 100), 51)
	if !strings.HasPrefix(got, strings.Repeat("é", 12)) || strings.ContainsRune(got, '�') {
		t.Fatalf("capMiddle split a rune: %q", got)
	}
}

// The ceiling check prices what is sent: a retry prompt far longer than the
// original must be refused where the original would have fit.
func TestRetry_CostEstimateUsesTheNewPrompt(t *testing.T) {
	testutil.NewSandbox(t)
	h1, _ := recordingHead(t, "h1", strings.Repeat("w", 8000))
	h2, rec2 := recordingHead(t, "h2", "second")
	h1.Meta = map[string]string{"model": "test/priced"}
	h2.Meta = h1.Meta
	d := climbingDispatcher(t, climbing{h1, 96, 0.01}, climbing{h2, 98, 0.02})
	d.pricing = pricedModel(t, "test/priced", 2, 4)
	// 400 chars estimate 100 in and 50 out: $0.0004 a call, so h2 fits on the
	// original prompt and not on one carrying h1's 8000-char answer ($0.008).
	res, err := d.Dispatch(context.Background(), strings.Repeat("x", 400), Options{
		MaxCostUSD: 0.005, Domain: climbClass, Escalate: rejectFirst("rejected"), Retry: RetryPrompt,
	})
	if err != nil || res.Head.ID != "h1" {
		t.Fatalf("Dispatch = %v, %v; want h1's answer standing", res, err)
	}
	if rec2.sent() != "" {
		t.Fatal("h2 was sent a retry the ceiling should have refused on its length")
	}
}

// A secret the rejected head echoed is declared on the retry, so the egress
// gate stops it reaching a head that leaves the machine.
func TestRetry_EchoedSecretIsHeldAtTheEgressGate(t *testing.T) {
	testutil.NewSandbox(t)
	h1, _ := recordingHead(t, "h1", "the key is AKIA"+"IOSFODNN7EXAMPLE")
	h2, rec2 := recordingHead(t, "h2", "second")
	d := climbingDispatcher(t, climbing{h1, 96, 0.01}, climbing{h2, 98, 0.02})

	res, err := d.Dispatch(context.Background(), "write a deploy note", Options{
		Domain: climbClass, Escalate: rejectFirst("rejected"), Retry: RetryPrompt,
	})
	if err != nil || res.Head.ID != "h1" {
		t.Fatalf("Dispatch = %v, %v; want h1's answer standing", res, err)
	}
	if rec2.sent() != "" {
		t.Fatal("h1's echoed key was sent on to h2")
	}
	if len(res.Gates) == 0 || res.Gates[len(res.Gates)-1].Code != GateEgress {
		t.Fatalf("gates = %+v, want h2 held at the egress gate", res.Gates)
	}
}
